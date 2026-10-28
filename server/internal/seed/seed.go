// Package seed 负责内置模板的启动播种（契约 §5）。
//
// 开源版首启要回答的问题只有一个：**这东西能干什么**。所以默认播种的
// 是「铸版能力演示」模板——每个语法各一处、每处都带说明文字，新用户
// 打开就能看到全貌；「代码审计报告」作为真实业务样例一并保留，手册 §7
// 的完整示例 JSON 引用的就是它。
package seed

import (
	"context"
	"encoding/json"
	"log"
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
// `audit-report` / `demo-report` 两个 code 引用模板；内置的「主」模板
// 用一个常量指出来，种子表里第一项必须与它一致。
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
			Code:        "audit-report",
			Name:        "代码审计报告",
			Category:    "安全交付",
			Description: "由 code-audit-report v1.1 迁移而来的业务样例模板，可一键试用。",
			DOCX:        docforge.AuditTemplateDOCX,
			SampleJSON:  docforge.AuditSampleJSON,
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
