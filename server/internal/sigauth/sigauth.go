// Package sigauth 实现 V3 签章授权与 fail-closed 注入：
// 1) 资源必须存在、未软删、status=approved，否则 422；
// 2) owner/admin 隐式放行不扣次数；
// 3) 否则原子扣减一条 active grant（窗口内 + 次数未满），否则 403 grant_denied；
// 4) 通过后把 PNG 转 data:URI 注入渲染 data，用户已提供同名 key 则不覆盖。
//
// V3.1 起，第 4 步有两种产物：直接读资源文件，或按 `$sig`/`$seal` 参数对象
// **现场出图**（可带出具时间）——见 sigref.go。授权口径完全不变。
package sigauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"docforge/server/internal/auditx"
	"docforge/server/internal/idgen"
	"docforge/server/internal/models"

	"gorm.io/gorm"
)

// resourceIDRe 判定一个字符串是不是签章资源 ID（sig_xxx / seal_xxx）。
//
// 用途只有一个：把「用户传的是资源 ID」和「用户传的是别的普通字符串」分开。
// 这一步非做不可——模板变量名和资源 ID 用的是同一套 sig_/seal_ 前缀，
// 光看前缀分不清哪个是 ID、哪个是模板里的占位名。
var resourceIDRe = regexp.MustCompile(`^(sig|seal)_[0-9a-zA-Z]{6,}$`)

// ScanPlaceholders 遍历 model.blocks（顶层 + table 非 loop_row 单元格），
// 识别 {type:"placeholder", kind:"image"} 且 name 以 sig_/seal_ 开头，
// 或带 signature_id/seal_id 字段的块，返回占位变量名集合（即资源 ID）。
func ScanPlaceholders(modelJSON []byte) []string {
	var doc map[string]interface{}
	if err := json.Unmarshal(modelJSON, &doc); err != nil {
		return nil
	}
	blocks, _ := doc["blocks"].([]interface{})
	seen := map[string]bool{}
	var out []string
	scanSigBlocks(blocks, seen, &out)
	return out
}

func scanSigBlocks(blocks []interface{}, seen map[string]bool, out *[]string) {
	for _, b := range blocks {
		bm, ok := b.(map[string]interface{})
		if !ok {
			continue
		}
		switch bm["type"] {
		case "placeholder":
			if bm["kind"] != "image" {
				continue
			}
			name, _ := bm["name"].(string)
			_, hasSig := bm["signature_id"]
			_, hasSeal := bm["seal_id"]
			if !strings.HasPrefix(name, "sig_") && !strings.HasPrefix(name, "seal_") && !hasSig && !hasSeal {
				continue
			}
			if name == "" {
				continue
			}
			if !seen[name] {
				seen[name] = true
				*out = append(*out, name)
			}
		case "table":
			rows, _ := bm["rows"].([]interface{})
			for _, r := range rows {
				rm, _ := r.(map[string]interface{})
				if lr, _ := rm["loop_row"].(bool); lr {
					continue
				}
				cells, _ := rm["cells"].([]interface{})
				for _, c := range cells {
					cm, _ := c.(map[string]interface{})
					inner, _ := cm["blocks"].([]interface{})
					scanSigBlocks(inner, seen, out)
				}
			}
		}
	}
}

// InjectPlaceholders 把占位名以空值合并进 data，并剔除自动示例生成的字符串值
// （避免 sample_json 把 sig_xxx 填成普通字符串绕过签章注入）。
// 已存在的图片对象（map）保留；**用户显式传入的资源 ID 也保留**。
//
// 为什么后面这半句是必须的：模板里写 `{{ sig_审核人 }}`，调用方在 data 里传
// `{"sig_审核人": "sig_a1b2c3"}` —— 这是「一条数据换一个审核人」的正常用法。
// 早期实现把所有字符串值一律删成空串，于是 Inject 只能拿**变量名**当资源 ID
// 去查，结果必然是「签名不存在」。等于逼着用户为每个审核人重新做一份模板。
func InjectPlaceholders(data []byte, names []string) []byte {
	if len(names) == 0 {
		return data
	}
	var m map[string]interface{}
	if len(data) == 0 || string(data) == "null" {
		m = map[string]interface{}{}
	} else if err := json.Unmarshal(data, &m); err != nil {
		m = map[string]interface{}{}
	}
	if m == nil {
		m = map[string]interface{}{}
	}
	for _, n := range names {
		if v, ok := m[n]; ok {
			switch t := v.(type) {
			case map[string]interface{}:
				continue // 用户显式图片对象，保留
			case string:
				if resourceIDRe.MatchString(t) {
					continue // 用户传的是资源 ID，交给 Inject 按值解析
				}
			}
			delete(m, n) // 空串/示例字符串/null → 交给 Inject 注入
		}
		m[n] = ""
	}
	b, _ := json.Marshal(m)
	return b
}

// VarsSigNames 从 scan 输出的 variables 数组中提取 sig_/seal_ 前缀变量名。
// variables 元素可为 {name:"sig_xxx"} 或字符串 "sig_xxx"。
func VarsSigNames(varsJSON []byte) []string {
	if len(varsJSON) == 0 {
		return nil
	}
	var arr []interface{}
	if err := json.Unmarshal(varsJSON, &arr); err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, v := range arr {
		var name string
		switch t := v.(type) {
		case string:
			name = t
		case map[string]interface{}:
			name, _ = t["name"].(string)
		}
		if strings.HasPrefix(name, "sig_") || strings.HasPrefix(name, "seal_") {
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	return out
}

// ErrStatus 422 fail-closed（资源状态不满足）。
type ErrStatus struct {
	ResourceType string
	ResourceID   string
	Name         string
	Reason       string
}

func (e *ErrStatus) Error() string {
	return fmt.Sprintf("资源 %s/%s（%s）不可用：%s", e.ResourceType, e.ResourceID, e.Name, e.Reason)
}

// ErrDenied 403 授权拒绝。
type ErrDenied struct {
	ResourceType string
	ResourceID   string
	Name         string
	Reason       string // unauthorized | exhausted | expired | not_yet
}

func (e *ErrDenied) Error() string {
	return fmt.Sprintf("无权使用 %s/%s（%s）：%s", e.ResourceType, e.ResourceID, e.Name, e.Reason)
}

// Options 调用方上下文。
type Options struct {
	GranteeType string // user | api_key
	GranteeID   string
	IsAdmin     bool
	DryRun      bool // 编辑器 preview/apply：只校验不扣次数
	JobID       string
	ExternalRef string
	// Ctx 供「现场出图」调用渲染服务用；nil 时退回 context.Background()。
	Ctx context.Context
}

// Renderer 只声明本包用得到的两个出图方法。
//
// 刻意不直接依赖 render.Renderer 的十来个方法：这里只需要「按参数出一张 PNG」，
// 依赖面越小越好写测试替身，也避免 sigauth 被渲染客户端牵着走。
type Renderer interface {
	RenderSignature(ctx context.Context, fields map[string]string) ([]byte, error)
	RenderSeal(ctx context.Context, fields map[string]string) ([]byte, error)
}

// Resolver 签章注入与授权校验器。
type Resolver struct {
	DB *gorm.DB
	// Renderer 仅在 `$sig`/`$seal` 参数对象要现场出图时才用得到。
	// 为 nil 时读文件那条老路径照常工作，需要出图则报 422（不静默降级成旧图）。
	Renderer Renderer
}

// Inject 对渲染 data 做 fail-closed 校验并注入 data:URI 图片对象。
// inputData 为 JSON 字节；返回新的 JSON 字节。
func (r *Resolver) Inject(inputData []byte, opts Options) ([]byte, error) {
	if len(inputData) == 0 || string(inputData) == "null" {
		inputData = []byte("{}")
	}
	var m map[string]interface{}
	if err := json.Unmarshal(inputData, &m); err != nil {
		return inputData, nil
	}
	if m == nil {
		m = map[string]interface{}{}
	}
	// 找出所有 sig_*/seal_* key。资源 ID 有三个来源，优先级明确：
	//   值是参数对象（含 $sig/$seal） → 取对象里的资源 ID + 现场出图参数
	//   值给了资源 ID                 → 按值解析（模板写 {{ sig_审核人 }}，每条数据换人）
	//   值为空                        → 拿 key 当资源 ID（编辑器插入占位符时，变量名就是 ID）
	// 三种都要留：前两者是 API 用法，最后一个是既有模板的既成事实，改掉会废掉存量模板。
	for k, v := range m {
		var resourceType string
		switch {
		case strings.HasPrefix(k, "sig_"):
			resourceType = "signature"
		case strings.HasPrefix(k, "seal_"):
			resourceType = "seal"
		default:
			continue
		}

		ref, perr := parseSigRef(v, k, resourceType)
		if perr != nil {
			return inputData, perr
		}

		var id string
		switch val := v.(type) {
		case map[string]interface{}:
			if ref == nil {
				continue // 用户显式提供的图片对象：不覆盖、不扣次数
			}
			id = ref.ResourceID
		case string:
			if val == "" {
				id = k
			} else if resourceIDRe.MatchString(val) {
				id = val
			} else {
				// 既不是资源 ID 又不是空：当成用户想直接印上去的文字，尊重它。
				continue
			}
		default:
			id = k
		}

		obj, err := r.resolveOne(resourceType, id, ref, opts)
		if err != nil {
			if d, ok := err.(*ErrDenied); ok {
				r.auditDenied(d, opts)
			}
			return inputData, err
		}
		m[k] = obj
	}
	return json.Marshal(m)
}

// resMeta 是资源里「与出图/摆放相关」的那部分字段。
//
// 单独抽出来是因为：现场出图要用资源自带的字体与署名做**默认值**，而读文件
// 那条路径只关心路径与尺寸。两者共用一次 DB 查询，顺手把字段都取出来。
type resMeta struct {
	ownerID    string
	filePath   string
	widthMM    float64
	heightMM   float64
	fontID     string
	signerName string
	unitName   string
	sealName   string
	shape      string
}

// resolveOne 校验单个资源并返回 data:URI 图片对象。
func (r *Resolver) resolveOne(resourceType, id string, ref *SigRef, opts Options) (map[string]interface{}, error) {
	var meta resMeta
	if resourceType == "signature" {
		var x models.Signature
		if err := r.DB.First(&x, "id = ? AND deleted_at IS NULL", id).Error; err != nil {
			return nil, &ErrStatus{resourceType, id, id, "签名不存在或已删除"}
		}
		if x.Status != "approved" {
			return nil, &ErrStatus{resourceType, id, x.Name, "签名状态为 " + x.Status + "，需审批通过后才可使用"}
		}
		meta = resMeta{ownerID: x.OwnerID, filePath: x.FilePath, widthMM: x.WidthMM, heightMM: x.HeightMM,
			fontID: x.FontID, signerName: x.SignerName}
	} else {
		var x models.Seal
		if err := r.DB.First(&x, "id = ? AND deleted_at IS NULL", id).Error; err != nil {
			return nil, &ErrStatus{resourceType, id, id, "印章不存在或已删除"}
		}
		if x.Status != "approved" {
			return nil, &ErrStatus{resourceType, id, x.Name, "印章状态为 " + x.Status + "，需审批通过后才可使用"}
		}
		meta = resMeta{ownerID: x.OwnerID, filePath: x.FilePath, widthMM: x.WidthMM, heightMM: x.HeightMM,
			unitName: x.UnitName, sealName: x.SealName, shape: x.Shape}
	}

	// 授权通过之后才出图——顺带保证「出图失败」不会白扣次数：
	// 出图发生在三处 return 之前，扣减发生在 consumeGrant 里，顺序是对的。
	build := func() (map[string]interface{}, error) {
		return r.buildImage(resourceType, id, meta, ref, opts)
	}

	// owner / admin 隐式放行。
	if opts.IsAdmin || (opts.GranteeType == "user" && meta.ownerID == opts.GranteeID) {
		return build()
	}
	// DryRun（编辑器 apply/preview）：不扣次数，但仍要求有可用授权。
	if opts.DryRun {
		if _, err := r.peekGrant(resourceType, id, opts); err != nil {
			return nil, err
		}
		return build()
	}
	// 原子扣减。
	if _, err := r.consumeGrant(resourceType, id, opts); err != nil {
		return nil, err
	}
	return build()
}

// peekGrant 只读检查是否有可用 grant（不扣）。
func (r *Resolver) peekGrant(resourceType, resourceID string, opts Options) (*models.Grant, error) {
	now := time.Now()
	var g models.Grant
	q := r.DB.Where("resource_type=? AND resource_id=? AND grantee_type=? AND grantee_id=?",
		resourceType, resourceID, opts.GranteeType, opts.GranteeID)
	q = q.Where("(valid_from IS NULL OR valid_from<=?) AND (valid_until IS NULL OR valid_until>=?)", now, now)
	q = q.Where("max_uses=0 OR used_count<max_uses")
	if err := q.Order("created_at ASC").First(&g).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// 回查具体拒绝原因（过期/未生效/耗尽/未授权），与 consumeGrant 一致。
			return nil, &ErrDenied{resourceType, resourceID, resourceID, r.denyReason(resourceType, resourceID, opts, now)}
		}
		return nil, err
	}
	return &g, nil
}

// consumeGrant 原子扣减一条 grant。
func (r *Resolver) consumeGrant(resourceType, resourceID string, opts Options) (*models.Grant, error) {
	now := time.Now()
	var g models.Grant
	q := r.DB.Where("resource_type=? AND resource_id=? AND grantee_type=? AND grantee_id=?",
		resourceType, resourceID, opts.GranteeType, opts.GranteeID)
	q = q.Where("(valid_from IS NULL OR valid_from<=?) AND (valid_until IS NULL OR valid_until>=?)", now, now)
	q = q.Where("max_uses=0 OR used_count<max_uses")
	if err := q.Order("created_at ASC").First(&g).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// 回查原因。
			reason := r.denyReason(resourceType, resourceID, opts, now)
			return nil, &ErrDenied{resourceType, resourceID, resourceID, reason}
		}
		return nil, err
	}
	res := r.DB.Model(&models.Grant{}).
		Where("id=? AND (valid_from IS NULL OR valid_from<=?) AND (valid_until IS NULL OR valid_until>=?) AND (max_uses=0 OR used_count<max_uses)",
			g.ID, now, now).
		UpdateColumn("used_count", g.UsedCount+1)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, &ErrDenied{resourceType, resourceID, resourceID, r.denyReason(resourceType, resourceID, opts, now)}
	}
	// 写 grant event。
	evt := models.GrantEvent{
		ID: "gev_" + idgen.Hex(6), GrantID: g.ID, ResourceType: resourceType, ResourceID: resourceID,
		Action: "use", ActorID: opts.GranteeID, CreatedAt: time.Now(),
	}
	detail, _ := json.Marshal(map[string]interface{}{
		"used_count_after": g.UsedCount + 1, "remaining": remaining(&g, g.UsedCount+1),
		"job_id": opts.JobID, "external_ref": opts.ExternalRef,
	})
	evt.Detail = detail
	_ = r.DB.Create(&evt).Error
	return &g, nil
}

func remaining(g *models.Grant, used int) interface{} {
	if g.MaxUses == 0 {
		return -1
	}
	rem := g.MaxUses - used
	if rem < 0 {
		rem = 0
	}
	return rem
}

// auditDenied 把授权拒绝写入哈希链审计日志（无论 DryRun 还是正式渲染）。
func (r *Resolver) auditDenied(d *ErrDenied, opts Options) {
	actorType := opts.GranteeType
	if actorType == "" {
		actorType = "user"
	}
	entry := &auditx.Entry{
		ActorType:   actorType,
		ActorUserID: opts.GranteeID,
		Action:      d.ResourceType + ".grant.denied",
		ObjectType:  d.ResourceType,
		ObjectID:    d.ResourceID,
		Detail: map[string]interface{}{
			"reason":       d.Reason,
			"grantee_type": opts.GranteeType,
			"grantee_id":   opts.GranteeID,
			"dry_run":      opts.DryRun,
			"job_id":       opts.JobID,
			"external_ref": opts.ExternalRef,
		},
	}
	_, _ = (&auditx.Chain{DB: r.DB}).Write(entry)
}

// denyReason 回查拒绝原因。
func (r *Resolver) denyReason(resourceType, resourceID string, opts Options, now time.Time) string {
	var g models.Grant
	if err := r.DB.Where("resource_type=? AND resource_id=? AND grantee_type=? AND grantee_id=?",
		resourceType, resourceID, opts.GranteeType, opts.GranteeID).
		Order("created_at ASC").First(&g).Error; err != nil {
		return "unauthorized"
	}
	if g.ValidUntil != nil && now.After(*g.ValidUntil) {
		return "expired"
	}
	if g.ValidFrom != nil && now.Before(*g.ValidFrom) {
		return "not_yet"
	}
	if g.MaxUses > 0 && g.UsedCount >= g.MaxUses {
		return "exhausted"
	}
	return "unauthorized"
}

// buildImage 产出注入用的图片对象：有出图参数就走渲染服务现场生成，否则读资源文件。
func (r *Resolver) buildImage(resourceType, id string, meta resMeta, ref *SigRef, opts Options) (map[string]interface{}, error) {
	widthMM, heightMM := meta.widthMM, meta.heightMM
	if ref != nil {
		if ref.WidthMM > 0 {
			widthMM = ref.WidthMM
		}
		if ref.HeightMM > 0 {
			heightMM = ref.HeightMM
		}
	}
	if !ref.needsRender() {
		return imageObject(meta.filePath, widthMM, heightMM)
	}

	// 明确报错而不是退回旧图：用户写了 with_timestamp 却拿到一张没有时间的图，
	// 是最坏的结果（他自己看不出来，签出去的文件上时间缺失）。
	if r.Renderer == nil {
		return nil, &ErrParams{Name: id, Msg: "渲染服务不可用，无法现场生成签章（去掉 font/signer_name/with_timestamp 等参数可改用预置图）"}
	}
	fields, err := ref.fields(resourceType, meta, widthMM)
	if err != nil {
		return nil, err
	}
	ctx := opts.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	var png []byte
	if resourceType == "signature" {
		png, err = r.Renderer.RenderSignature(ctx, fields)
	} else {
		png, err = r.Renderer.RenderSeal(ctx, fields)
	}
	if err != nil {
		return nil, fmt.Errorf("现场生成%s失败（%s）：%w", cnType(resourceType), id, err)
	}
	return imageObjectBytes(png, widthMM, heightMM), nil
}

func cnType(resourceType string) string {
	if resourceType == "signature" {
		return "签名"
	}
	return "印章"
}

// imageObject 把 PNG 文件转成 richdata.resolve_context 消费的图片对象。
func imageObject(path string, widthMM, heightMM float64) (map[string]interface{}, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("签章文件不可读: %w", err)
	}
	return imageObjectBytes(b, widthMM, heightMM), nil
}

func imageObjectBytes(b []byte, widthMM, heightMM float64) map[string]interface{} {
	return map[string]interface{}{
		"type":  "image",
		"src":   "data:image/png;base64," + base64.StdEncoding.EncodeToString(b),
		"width": widthMM, "height": heightMM,
	}
}

