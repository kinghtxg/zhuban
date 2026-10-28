// Package models 定义 GORM 数据模型，严格对齐契约 §5 的字段、索引与唯一约束。
// jsonb 字段统一用 JSONB（底层 []byte）在 PG 存 jsonb、在 sqlite 存 text，双库兼容。
package models

import (
	"database/sql/driver"
	"errors"
	"time"

	"gorm.io/gorm"
)

// JSONB 是兼容 PG(jsonb)/SQLite(text) 的 JSON 字段类型。读写为原始 JSON 字节。
type JSONB []byte

// Scan 实现 sql.Scanner。
func (j *JSONB) Scan(src interface{}) error {
	if src == nil {
		*j = nil
		return nil
	}
	switch v := src.(type) {
	case []byte:
		b := make([]byte, len(v))
		copy(b, v)
		*j = b
	case string:
		*j = []byte(v)
	default:
		return errors.New("JSONB: unsupported scan source")
	}
	return nil
}

// Value 实现 driver.Valuer。
func (j JSONB) Value() (driver.Value, error) {
	if len(j) == 0 {
		return nil, nil
	}
	return []byte(j), nil
}

// User 本地账号 + OIDC 联合用户（契约 §4.2/§5）。
type User struct {
	ID           string    `gorm:"primaryKey;size:32" json:"id"`
	Username     string    `gorm:"size:64;uniqueIndex;not null" json:"username"`
	PasswordHash string    `gorm:"size:255" json:"-"` // argon2id PHC；纯 OIDC 账号为空
	Email        string    `gorm:"size:255" json:"email"`
	DisplayName  string    `gorm:"size:120" json:"display_name"`
	Role         string    `gorm:"size:16;default:member" json:"role"`     // admin | member
	Status       string    `gorm:"size:16;default:pending" json:"status"` // pending | active | disabled
	Source       string    `gorm:"size:16;default:local" json:"source"`   // local | oidc
	// OIDCSub 的列名必须显式写成 oidc_sub。
	//
	// 不写的话 GORM 的默认命名策略会把 OIDCSub 转成 o_id_c_sub（它只认识 ID/URL/API
	// 这类常见缩写，不认识 OIDC），而 auth/oidc.go 里所有手写条件都是 "oidc_sub = ?"。
	// 后果是查不到已有绑定 → 每次 OIDC 登录都当新用户建号 → 第二次撞唯一索引直接失败。
	OIDCSub     *string    `gorm:"column:oidc_sub;size:255;uniqueIndex" json:"oidc_sub"`
	GuideSeen    bool      `gorm:"default:false" json:"guide_seen"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	LastLoginAt  *time.Time `json:"last_login_at"`
}

// HasPassword 是否设置了本地密码。
func (u *User) HasPassword() bool { return u.PasswordHash != "" }

// IsAdmin 是否管理员。
func (u *User) IsAdmin() bool { return u.Role == "admin" }

// Template 模板主表。code 全局唯一、创建后不可改。
type Template struct {
	ID               string    `gorm:"primaryKey;size:32" json:"id"`
	Code             string    `gorm:"size:64;uniqueIndex;not null" json:"code"`
	Name             string    `gorm:"size:255;not null" json:"name"`
	Category         string    `gorm:"size:120" json:"category"`
	Description      string    `gorm:"type:text" json:"description"`
	Visibility       string    `gorm:"size:16;default:private" json:"visibility"` // private|public
	Enabled          bool      `gorm:"default:true" json:"enabled"`
	CurrentVersionID string    `gorm:"size:32" json:"current_version_id"`
	CreatedBy        string    `gorm:"size:32" json:"created_by"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	// DeletedAt 软删。用 gorm.DeletedAt 而不是 *time.Time：模板在 30+ 处被查，
	// 靠人工在每个 Where 里补 deleted_at IS NULL 一定会漏（漏一处就等于没删干净）。
	// 交给 GORM 自动过滤，只在这类"必须看到已删数据"的场景显式 Unscoped()。
	DeletedAt gorm.DeletedAt `gorm:"index" json:"deleted_at,omitempty"`
	// OriginalCode 删除时把 code 释放出来（置为 deleted:<id>）前的原值。
	// 不释放 code 的话，删掉模板后重建同名会撞 code 的唯一索引——这是软删最常见的坑。
	OriginalCode string `gorm:"size:64" json:"original_code,omitempty"`
}

// TemplateVersion 模板版本。(template_id, version) 唯一。
type TemplateVersion struct {
	ID          string    `gorm:"primaryKey;size:32" json:"id"`
	TemplateID  string    `gorm:"size:32;index;not null;uniqueIndex:uniq_tpl_ver" json:"template_id"`
	Version     string    `gorm:"size:32;not null;uniqueIndex:uniq_tpl_ver" json:"version"`
	FilePath    string    `gorm:"size:500" json:"file_path"`
	FileSize    int64     `json:"file_size"`
	Variables   JSONB     `gorm:"type:text" json:"variables,omitempty"`
	SampleJSON  JSONB     `gorm:"type:text" json:"sample_json,omitempty"`
	JSONSchema  JSONB     `gorm:"type:text" json:"json_schema,omitempty"`
	Diagnostics JSONB     `gorm:"type:text" json:"diagnostics,omitempty"`
	// Scanned 标记是否已成功调 render /scan（内置播种时 render 不通可能为 false）。
	Scanned   bool      `gorm:"default:true" json:"scanned"`
	UploadedBy string    `gorm:"size:32" json:"uploaded_by"`
	// V2：版本来源。upload=上传 docx；online_edit=在线编辑器保存；rollback=由历史版本回滚复制而来。
	Source string `gorm:"size:16;default:upload" json:"source"`
	// ChangeSummary 在线编辑/回滚时的人读摘要（可空）。
	ChangeSummary string `gorm:"size:500" json:"change_summary,omitempty"`
	// VariableMeta 该版本上用户维护的变量元数据（map[name]VariableMetaItem，jsonb）。
	VariableMeta JSONB `gorm:"type:text" json:"variable_meta,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// VariableMetaItem 单个变量的用户维护元数据（契约 V2 §9）。
type VariableMetaItem struct {
	Name        string `json:"name"`
	Kind        string `json:"kind,omitempty"`
	Type        string `json:"type,omitempty"`
	Required    *bool  `json:"required,omitempty"`
	Default     string `json:"default,omitempty"`
	Description string `json:"description,omitempty"`
	// Status 仅用于合并后输出：active（scan 仍存在）| removed（scan 已删，保留记录）。
	Status string `json:"status,omitempty"`
}

// TemplateAsset 模板资源库（图片/logo 等，V2 §9）。
type TemplateAsset struct {
	ID        string    `gorm:"primaryKey;size:32" json:"id"`
	TemplateID string   `gorm:"size:32;index;not null" json:"template_id"`
	Name      string    `gorm:"size:255;not null" json:"name"`
	Type      string    `gorm:"size:16;default:image" json:"type"` // image | qrcode
	FilePath  string    `gorm:"size:500" json:"-"`
	MIME      string    `gorm:"size:64" json:"mime"`
	WidthMM   float64   `json:"width_mm"`
	HeightMM  float64   `json:"height_mm"`
	// BindVar 绑定到的 docx 占位变量名（如 image_logo）；空表示未绑定。
	// 渲染时由 assetimg.Resolver.MergeDefaults 注入 data:URI 图片对象。
	BindVar   string    `gorm:"size:128;index" json:"bind_var,omitempty"`
	CreatedBy string    `gorm:"size:32" json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

// AuditLog 审计日志（V3：哈希链 WORM，只 INSERT）。
// 保留 V2 旧列 actor_user_id/actor_name/action/object_type/object_id/detail/created_at。
type AuditLog struct {
	ID         string    `gorm:"primaryKey;size:32" json:"id"`
	Seq        int64     `gorm:"uniqueIndex" json:"seq"`
	ActorType  string    `gorm:"size:16;index" json:"actor_type"` // user | api_key
	ActorUserID string   `gorm:"size:32;index" json:"actor_user_id"`
	ActorName  string    `gorm:"size:120" json:"actor_name"`
	Action     string    `gorm:"size:64;index" json:"action"`
	ObjectType string    `gorm:"size:32;index" json:"object_type"`
	ObjectID   string    `gorm:"size:64;index" json:"object_id"`
	TemplateCode string `gorm:"size:64;index" json:"template_code,omitempty"`
	ExternalRef string    `gorm:"size:255;index" json:"external_ref,omitempty"`
	SignatureID string   `gorm:"size:32;index" json:"signature_id,omitempty"`
	SealID      string    `gorm:"size:32;index" json:"seal_id,omitempty"`
	SourceIP   string    `gorm:"size:64" json:"source_ip,omitempty"`
	Endpoint   string    `gorm:"size:255" json:"endpoint,omitempty"`
	Detail     JSONB     `gorm:"type:text" json:"detail,omitempty"`
	OccurredAt time.Time `gorm:"index" json:"occurred_at"`
	CreatedAt  time.Time `gorm:"index" json:"created_at"`
	PrevHash   string    `gorm:"size:64" json:"prev_hash"`
	SelfHash   string    `gorm:"size:64;uniqueIndex" json:"self_hash"`
}

// Signature 电子签名素材库（V3 §3.2）。
type Signature struct {
	ID         string    `gorm:"primaryKey;size:32" json:"id"`
	Name       string    `gorm:"size:255;not null" json:"name"`
	OwnerID    string    `gorm:"size:32;index" json:"owner_id"`
	Source     string    `gorm:"size:16" json:"source"` // upload | generated
	SignerName string    `gorm:"size:120" json:"signer_name,omitempty"`
	FontID     string    `gorm:"size:64" json:"font_id,omitempty"`
	Params     JSONB     `gorm:"type:text" json:"params,omitempty"`
	FilePath   string    `gorm:"size:500" json:"-"`
	WidthMM    float64   `json:"width_mm"`
	HeightMM   float64   `json:"height_mm"`
	Status     string    `gorm:"size:16;index" json:"status"` // pending|approved|rejected|disabled
	ReviewedBy string    `gorm:"size:32" json:"reviewed_by,omitempty"`
	ReviewedAt *time.Time `json:"reviewed_at,omitempty"`
	ReviewNote string    `gorm:"type:text" json:"review_note,omitempty"`
	DeletedAt  *time.Time `gorm:"index" json:"deleted_at,omitempty"` // 软删
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Seal 电子印章素材库（V3 §3.3）。
type Seal struct {
	ID         string    `gorm:"primaryKey;size:32" json:"id"`
	Name       string    `gorm:"size:255;not null" json:"name"`
	OwnerID    string    `gorm:"size:32;index" json:"owner_id"`
	Source     string    `gorm:"size:16" json:"source"` // upload | generated
	UnitName   string    `gorm:"size:255" json:"unit_name,omitempty"`
	SealName   string    `gorm:"size:255" json:"seal_name,omitempty"`
	Shape      string    `gorm:"size:16" json:"shape,omitempty"` // circle|ellipse
	Params     JSONB     `gorm:"type:text" json:"params,omitempty"`
	FilePath   string    `gorm:"size:500" json:"-"`
	WidthMM    float64   `json:"width_mm"`
	HeightMM   float64   `json:"height_mm"`
	Status     string    `gorm:"size:16;index" json:"status"`
	ReviewedBy string    `gorm:"size:32" json:"reviewed_by,omitempty"`
	ReviewedAt *time.Time `json:"reviewed_at,omitempty"`
	ReviewNote string    `gorm:"type:text" json:"review_note,omitempty"`
	DeletedAt  *time.Time `gorm:"index" json:"deleted_at,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Review 审核流水（V3 §3.4）。
type Review struct {
	ID           string    `gorm:"primaryKey;size:32" json:"id"`
	ResourceType string    `gorm:"size:16;index" json:"resource_type"` // signature|seal
	ResourceID   string    `gorm:"size:32;index" json:"resource_id"`
	Action       string    `gorm:"size:16" json:"action"` // submit|approve|reject|disable|enable
	OperatorID   string    `gorm:"size:32" json:"operator_id"`
	Note         string    `gorm:"type:text" json:"note,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// Setting key-value 系统设置（V3 §3.6）。
type Setting struct {
	Key       string    `gorm:"primaryKey;size:128" json:"key"`
	Value     string    `gorm:"size:1024" json:"value"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Grant 签章授权（V3.1 §3.7）。status 列只存 active，其他状态查询时计算。
type Grant struct {
	ID          string     `gorm:"primaryKey;size:32" json:"id"`
	ResourceType string    `gorm:"size:16;index" json:"resource_type"` // signature|seal
	ResourceID  string     `gorm:"size:32;index" json:"resource_id"`
	GrantedBy   string     `gorm:"size:32" json:"granted_by"`
	GranteeType string     `gorm:"size:16;index" json:"grantee_type"` // user|api_key
	GranteeID   string     `gorm:"size:64;index" json:"grantee_id"`
	GranteeName string     `gorm:"size:120" json:"grantee_name"`
	MaxUses     int        `json:"max_uses"` // 0=不限
	UsedCount   int        `json:"used_count"`
	ValidFrom   *time.Time `gorm:"index" json:"valid_from,omitempty"`
	ValidUntil  *time.Time `gorm:"index" json:"valid_until,omitempty"`
	Note        string     `gorm:"type:text" json:"note,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// GrantEvent 授权事件流水（V3.1 §3.8）。
type GrantEvent struct {
	ID           string    `gorm:"primaryKey;size:32" json:"id"`
	GrantID      string    `gorm:"size:32;index" json:"grant_id"`
	ResourceType string    `gorm:"size:16" json:"resource_type"`
	ResourceID   string    `gorm:"size:32" json:"resource_id"`
	Action       string    `gorm:"size:32;index" json:"action"`
	ActorID      string    `gorm:"size:32" json:"actor_id"`
	Detail       JSONB     `gorm:"type:text" json:"detail,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// CallbackLog 回调投递记录（一次 sendWithRetry 生命周期一条；重放再记一条）。
type CallbackLog struct {
	ID           string    `gorm:"primaryKey;size:32" json:"id"`
	JobID        string    `gorm:"size:32;index" json:"job_id"`
	CallbackURL  string    `gorm:"size:500" json:"callback_url"`
	Status       string    `gorm:"size:16;index" json:"status"` // succeeded | failed
	Attempts     int       `json:"attempts"`
	ErrorSummary string    `gorm:"size:500" json:"error_summary"`
	CreatedAt    time.Time `gorm:"index" json:"created_at"`
}

// APIKey X-API-Key 凭证。仅创建时返回明文，存储 sha256(key)。
type APIKey struct {
	ID          string     `gorm:"primaryKey;size:32" json:"id"`
	UserID      string     `gorm:"size:32;index" json:"user_id"`
	Name        string     `gorm:"size:255" json:"name"`
	KeyHash     string     `gorm:"size:64;uniqueIndex;not null" json:"-"`
	Prefix      string     `gorm:"size:16" json:"prefix"`
	QPSLimit    int        `gorm:"default:5" json:"qps_limit"`
	MaxRecords  int        `gorm:"default:1000" json:"max_records"`
	Enabled     bool       `gorm:"default:true" json:"enabled"`
	LastUsedAt  *time.Time `json:"last_used_at"`
	// LastUsedIP 最近一次用这把 Key 调接口的来源 IP。
	// 排查「这把 Key 是谁在跑」「额度被谁吃掉了」时，只有时间是不够的。
	// 取值口径与限流、审计一致（见 internal/clientip），伪造不了。
	LastUsedIP string    `gorm:"size:64" json:"last_used_ip,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// Job 一次生成任务（UI/同步 API/投递即生成/批量统一管道）。
type Job struct {
	ID               string     `gorm:"primaryKey;size:32" json:"id"`
	TemplateID       string     `gorm:"size:32;index" json:"template_id"`
	VersionID        string     `gorm:"size:32" json:"version_id"`
	CreatedByUserID  string     `gorm:"size:32;index" json:"created_by_user_id"`
	APIKeyID         string     `gorm:"size:32;index" json:"api_key_id"`
	Source           string     `gorm:"size:16" json:"source"` // ui|sync_api|auto_api|batch
	ExternalRef      string     `gorm:"size:255;index" json:"external_ref"`
	IdempotencyKey   string     `gorm:"size:64;index" json:"idempotency_key"`
	CallbackURL      string     `gorm:"size:500" json:"callback_url"`
	CallbackStatus   string     `gorm:"size:16" json:"callback_status"` // ""|pending|succeeded|failed
	CallbackAttempts int        `gorm:"default:0" json:"callback_attempts"`
	Status           string     `gorm:"size:16;index" json:"status"` // queued|running|succeeded|partial|failed
	Total            int        `json:"total"`
	Succeeded        int        `json:"succeeded"`
	Failed           int        `json:"failed"`
	Format           string     `gorm:"size:16" json:"format"` // docx|pdf|both
	FilenameRule     string     `gorm:"size:500" json:"filename_rule"`
	ZipPath          string     `gorm:"size:500" json:"zip_path"`
	// EmailSpec 整任务一封的邮件规格（V3.2）。JSON 形式见 mailer.EmailSpec。
	// 为空表示这次任务不发邮件。每条记录各发一封的规格在 Document.EmailSpec 上。
	EmailSpec    JSONB  `gorm:"type:text" json:"email_spec,omitempty"`
	EmailStatus  string `gorm:"size:16" json:"email_status,omitempty"`   // ""|pending|sent|failed|skipped|disabled
	EmailError   string `gorm:"size:500" json:"email_error,omitempty"`
	EmailAttempts int   `json:"email_attempts"`
	ErrorDetails     JSONB      `gorm:"type:text" json:"error_details,omitempty"`
	Expired          bool       `gorm:"default:false;index" json:"expired"`
	CreatedAt        time.Time  `json:"created_at"`
	StartedAt        *time.Time `json:"started_at"`
	FinishedAt       *time.Time `json:"finished_at"`
	ExpiresAt        *time.Time `json:"expires_at"`
}

// Document 单份产物记录（文档生成中心的最小单位）。
type Document struct {
	ID           string     `gorm:"primaryKey;size:32" json:"id"`
	JobID        string     `gorm:"size:32;index" json:"job_id"`
	RecordIndex  int        `json:"record_index"`
	Filename     string     `gorm:"size:500" json:"filename"`
	DocxPath     string     `gorm:"size:500" json:"docx_path"`
	PdfPath      string     `gorm:"size:500" json:"pdf_path"`
	Status       string     `gorm:"size:16;index" json:"status"` // queued|running|succeeded|failed
	Error        string     `gorm:"type:text" json:"error"`
	PathErr      string     `gorm:"size:200" json:"path_err"` // 字段路径（定位用）
	InputData    JSONB      `gorm:"type:text" json:"input_data,omitempty"`
	// EmailSpec 这一份单独发一封的规格（V3.2），为空则跟随任务的整包邮件。
	EmailSpec     JSONB  `gorm:"type:text" json:"email_spec,omitempty"`
	EmailStatus   string `gorm:"size:16" json:"email_status,omitempty"`
	EmailError    string `gorm:"size:500" json:"email_error,omitempty"`
	EmailAttempts int    `json:"email_attempts"`
	DownloadCount int       `json:"download_count"`
	DurationMS   int64      `json:"duration_ms"`
	Expired      bool       `gorm:"default:false;index" json:"expired"`
	CreatedAt    time.Time  `json:"created_at"`
	StartedAt    *time.Time `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at"`
}

// Session 服务端会话（df_session cookie → token 哈希）。
type Session struct {
	TokenHash string    `gorm:"primaryKey;size:64" json:"-"`
	UserID    string    `gorm:"size:32;index" json:"user_id"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// 邮件投递**没有**独立表：投递结果写进审计链（action=mail.deliver，
// object_type=email，结果在 detail 里）。
//
// 曾经这里有过一张 EmailLog 表，后来删掉了，理由写在 mailer/sender.go 的
// record() 上——同一个事实存两处迟早对不上，而「谁在什么时候把什么发给了谁」
// 本来就属于审计该回答的问题，不该放到哈希链外面。
// 任务维度还需要「这次生成有没有发出去过」，那个状态留在 job.email_status
// 和 document.email_status 上，是状态机而不是日志。

// Preview 工作台预览的一次性 pv 映射（内存缓存 30 分钟，落库仅为重启可恢复）。
type Preview struct {
	PV         string    `gorm:"primaryKey;size:64" json:"preview_id"`
	TemplateID string    `gorm:"size:32" json:"template_id"`
	Data       JSONB     `gorm:"type:text" json:"data,omitempty"`
	PageCount  int       `json:"page_count"`
	RenderedAt time.Time `json:"rendered_at"`
}

// AllModels 返回需要自动迁移的全部模型。
func AllModels() []interface{} {
	return []interface{}{
		&User{}, &Template{}, &TemplateVersion{}, &APIKey{},
		&Job{}, &Document{}, &Session{}, &Preview{},
		&TemplateAsset{}, &AuditLog{}, &CallbackLog{},
		&Signature{}, &Seal{}, &Review{}, &Setting{},
		&Grant{}, &GrantEvent{},
	}
}
