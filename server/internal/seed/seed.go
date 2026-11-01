// Package seed 负责内置模板的启动播种（契约 §5）。
//
// 开源版首启要回答的问题只有一个：**这东西能干什么**。所以默认播种两份，
// 分工明确：
//   - 「铸版能力演示」——每个语法各一处、每处都带说明文字，新用户打开就能看到全貌；
//   - 「月度经营分析报告」——一份像真报告一样能用的业务样例，《01·模板使用手册》§7
//     的完整示例 JSON 引用的就是它。技能演示与业务样例分开，读者才不会把演示模板
//     当成"只能这么写"。
package seed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"docforge"
	"docforge/server/internal/models"
	"docforge/server/internal/render"
	"docforge/server/internal/storage"
	"docforge/server/internal/store"

	"gorm.io/gorm"
)

// BuiltinCode 是演示模板编号。
//
// 保留这个名字（而不是改成 DemoCode）是因为文档与外部脚本里都按
// `demo-report` 这个 code 引用主演示模板；内置的「主」模板用一个常量指出来，
// 种子表里第一项必须与它一致。
//
// 注：早期版本还内置过一份 `audit-report`（代码审计报告）业务样例，
// 那份 docx 与示例数据来自另一个项目的交付物，已随 3.4.8 移除。
const BuiltinCode = "demo-report"

// builtin 描述一个内置模板。
type builtin struct {
	Code        string
	Name        string
	Category    string
	Description string
	DOCX        []byte
	SampleJSON  []byte
}

// builtins 是内置模板表。加模板往这里追加即可，播种逻辑不用动。
func builtins() []builtin {
	return []builtin{
		{
			Code:     "demo-report",
			Name:     "铸版能力演示",
			Category: "产品演示",
			Description: "每个占位符语法各演示一处：标量、默认值、日期表达式、表格行循环、" +
				"整段循环、嵌套循环、条件与 else、行内条件表达式、图片、二维码、富文本、" +
				"折线图与饼图、签章占位、页眉页脚页码、目录域。结论一栏可对照「使用手册」逐条试。",
			DOCX:       docforge.DemoTemplateDOCX,
			SampleJSON: docforge.DemoSampleJSON,
		},
		{
			Code:        "biz-report",
			Name:        "月度经营分析报告",
			Category:    "业务交付",
			Description: "一份完整的业务报告样例：5 个标量 + 核心指标表格行循环 + 两段 markdown 富文本，" +
				"《01·模板使用手册》§7 的完整示例 JSON 就照着它写。想改造成自己单位的报表，替换文字即可。",
			DOCX:        docforge.BizTemplateDOCX,
			SampleJSON:  docforge.BizSampleJSON,
		},
	}
}

// Run 在启动时播种内置模板；render 服务可能尚未就绪，扫描带重试。
func Run(ctx context.Context, db *gorm.DB, st *storage.Store, rd render.Renderer) {
	for _, b := range builtins() {
		seedOne(ctx, db, st, rd, b)
	}
}

func seedOne(ctx context.Context, db *gorm.DB, st *storage.Store, rd render.Renderer, b builtin) {
	// 用户删过内置模板就不要再塞回来。删除时 code 被改写成 deleted:<id>，
	// 所以必须走 Unscoped + original_code 判断；否则每次重启都会「复活」
	// 一个用户明确删掉的东西 —— 比不能删更让人不信任。
	var deleted models.Template
	if err := db.Unscoped().Where("original_code = ?", b.Code).First(&deleted).Error; err == nil {
		log.Printf("[seed] 内置模板 %s 已被删除，跳过播种", b.Code)
		return
	}

	var tpl models.Template
	if err := db.First(&tpl, "code = ?", b.Code).Error; err == nil {
		// 内置模板的内容是产品资产，会随版本升级而变（排版、域、示例都会改）。
		// 只按 code 判断"已存在就跳过"的话，升级后的实例永远停在上一次的 docx 上 ——
		// 3.4.10 的页码域修复正是这种情况：代码修好了，用户看到的模板还是坏的。
		if refreshed := refreshBuiltin(ctx, db, st, rd, b, &tpl); refreshed {
			return
		}
		// 已存在：若当前版本未扫描（例如上次启动时 render 未就绪），补扫一次。
		var ver models.TemplateVersion
		if db.First(&ver, "id = ?", tpl.CurrentVersionID).Error == nil && !ver.Scanned {
			if sc, ok := scanWithRetry(ctx, rd, b.DOCX); ok {
				applyScan(&ver, sc)
				if err := db.Save(&ver).Error; err == nil {
					log.Printf("[seed] 内置模板 %s 补扫完成（version %s，变量 %d 个）",
						b.Code, ver.Version, len(sc.Variables))
				}
			}
		}
		return
	}
	log.Printf("[seed] 未发现 code=%s 的模板，开始播种内置模板…", b.Code)

	tpl = models.Template{
		ID: store.NewTplID(), Code: b.Code, Name: b.Name,
		Category: b.Category, Visibility: "public", Enabled: true,
		Description: b.Description,
		CreatedBy:   "system",
	}
	ver := models.TemplateVersion{
		ID: store.NewVerID(), TemplateID: tpl.ID, Version: "1.0.0",
		UploadedBy: "system", Scanned: false,
	}

	path, err := st.TemplateVersionPath(tpl.ID, ver.ID)
	if err != nil {
		log.Printf("[seed] 建目录失败：%v", err)
		return
	}
	if err := st.WriteFile(path, b.DOCX, 0o644); err != nil {
		log.Printf("[seed] 写模板文件失败：%v", err)
		return
	}
	ver.FilePath = path
	ver.FileSize = int64(len(b.DOCX))
	ver.SampleJSON = b.SampleJSON

	if sc, ok := scanWithRetry(ctx, rd, b.DOCX); ok {
		applyScan(&ver, sc)
	} else {
		log.Printf("[seed] render /scan 在等待期内仍不可用，模板已建但标记待扫描（稍后可重启补扫）")
	}

	if err := db.Create(&ver).Error; err != nil {
		log.Printf("[seed] 写入版本失败：%v", err)
		return
	}
	tpl.CurrentVersionID = ver.ID
	if err := db.Create(&tpl).Error; err != nil {
		log.Printf("[seed] 写入模板失败：%v", err)
		return
	}
	log.Printf("[seed] 内置模板 %s 播种完成（version %s，scanned=%v，变量 %d 个）",
		b.Code, ver.Version, ver.Scanned, len(ver.Variables))
}

// scanWithRetry 在最多 60 秒内反复尝试 /scan（render 容器可能晚于主服务启动）。
func scanWithRetry(ctx context.Context, rd render.Renderer, docx []byte) (*render.ScanResult, bool) {
	deadline := time.Now().Add(60 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		sc, err := rd.Scan(ctx, docx)
		if err == nil {
			return sc, true
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return nil, false
		case <-time.After(2 * time.Second):
		}
	}
	if lastErr != nil {
		log.Printf("[seed] render /scan 等待 60s 后仍失败：%v", lastErr)
	}
	return nil, false
}

// applyScan 把 /scan 结果写回版本字段。
func applyScan(ver *models.TemplateVersion, sc *render.ScanResult) {
	if b, err := json.Marshal(sc.Variables); err == nil {
		ver.Variables = b
	}
	if b, err := json.Marshal(sc.JSONSchema); err == nil {
		ver.JSONSchema = b
	}
	if b, err := json.Marshal(sc.Diagnostics); err == nil {
		ver.Diagnostics = b
	}
	if len(ver.SampleJSON) == 0 {
		if b, err := json.Marshal(sc.SampleJSON); err == nil {
			ver.SampleJSON = b
		}
	}
	ver.Scanned = true
}



// refreshBuiltin 在内置模板内容与随包资产不一致时，为它发一个新版本。
//
// 为什么不是「覆盖旧文件」：版本历史是用户看得见的事实，同一个 1.0.0 前后装着
// 两份不同的 docx 会让人不敢再信版本号。发新版本还顺带解决了缩略图缓存 ——
// 缩略图按 (模板, 版本) 存，换了版本自然会重新生成。
//
// 什么情况下不动：
//   - 用户自己传过版本（UploadedBy 不是 system）。那时这份模板已经「是他的」，
//     产品没有资格把它换掉；
//   - 内容与随包资产逐字节相同。
func refreshBuiltin(ctx context.Context, db *gorm.DB, st *storage.Store, rd render.Renderer, b builtin, tpl *models.Template) bool {
	var ver models.TemplateVersion
	if err := db.First(&ver, "id = ?", tpl.CurrentVersionID).Error; err != nil {
		return false
	}
	if ver.UploadedBy != "system" {
		return false
	}
	// 先比大小（便宜），大小相同再逐字节比，免得「大小巧合相同」时漏更新。
	if ver.FileSize == int64(len(b.DOCX)) {
		if old, err := os.ReadFile(ver.FilePath); err == nil && bytes.Equal(old, b.DOCX) {
			return false
		}
	}

	nv := models.TemplateVersion{
		ID:         store.NewVerID(),
		TemplateID: tpl.ID,
		Version:    pickNextVersion(db, tpl.ID),
		UploadedBy: "system",
		Scanned:    false,
		SampleJSON: b.SampleJSON,
	}
	path, err := st.TemplateVersionPath(tpl.ID, nv.ID)
	if err != nil {
		log.Printf("[seed] 内置模板 %s 更新失败（建目录）：%v", b.Code, err)
		return false
	}
	if err := st.WriteFile(path, b.DOCX, 0o644); err != nil {
		log.Printf("[seed] 内置模板 %s 更新失败（写文件）：%v", b.Code, err)
		return false
	}
	nv.FilePath = path
	nv.FileSize = int64(len(b.DOCX))
	if sc, ok := scanWithRetry(ctx, rd, b.DOCX); ok {
		applyScan(&nv, sc)
	}
	if err := db.Create(&nv).Error; err != nil {
		log.Printf("[seed] 内置模板 %s 更新失败（写版本）：%v", b.Code, err)
		return false
	}
	if err := db.Model(tpl).Update("current_version_id", nv.ID).Error; err != nil {
		log.Printf("[seed] 内置模板 %s 更新失败（切版本）：%v", b.Code, err)
		return false
	}
	tpl.CurrentVersionID = nv.ID
	// 旧版本的缩略图按 (模板, 版本) 存，换了版本就不会再被取到，删掉免得留垃圾。
	if old, err := st.ThumbPath(tpl.ID, ver.ID); err == nil {
		_ = os.Remove(old)
	}
	log.Printf("[seed] 内置模板 %s 内容有更新，已发布新版本 %s（原 %s）", b.Code, nv.Version, ver.Version)
	return true
}

// pickNextVersion 取该模板现有版本里最大的补丁号 +1，与 API 侧同口径：
// 只增补丁号，不猜主次版本。
func pickNextVersion(db *gorm.DB, tplID string) string {
	var vers []models.TemplateVersion
	db.Where("template_id = ?", tplID).Select("version").Find(&vers)
	maxPatch := 0
	for _, v := range vers {
		parts := strings.Split(v.Version, ".")
		if len(parts) == 0 {
			continue
		}
		if n, err := strconv.Atoi(parts[len(parts)-1]); err == nil && n > maxPatch {
			maxPatch = n
		}
	}
	return fmt.Sprintf("1.0.%d", maxPatch+1)
}