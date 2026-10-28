// Package api 实现契约 §4 的全部 HTTP 接口（chi 路由，前缀 /api/v1）。
package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"sync"

	"docforge"
	"docforge/server/internal/auth"
	"docforge/server/internal/callback"
	"docforge/server/internal/clientip"
	"docforge/server/internal/config"
	"docforge/server/internal/mailer"
	"docforge/server/internal/netguard"
	"docforge/server/internal/render"
	"docforge/server/internal/sign"
	"docforge/server/internal/storage"
	"docforge/server/internal/update"
	"docforge/server/internal/worker"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

// Server 聚合全部 HTTP 依赖。
type Server struct {
	DB       *gorm.DB
	Cfg      *config.Config
	Store    *storage.Store
	Renderer render.Renderer
	Signer   *sign.Signer
	Auth     *auth.Manager
	OIDC     *auth.OIDC
	Sender   *callback.Sender
	Mailer   *mailer.Sender
	// IPs 解析来源 IP（限流与审计共用）。为 nil 时退化为「只认 RemoteAddr」。
	IPs *clientip.Resolver

	// updates / updateOnce 是更新检查器的惰性构造位（见 system_handlers.go）。
	// 放 sync.Once 而不是在 New() 里建：构造阶段不碰网络。
	updateOnce sync.Once
	updates    *update.Checker
}

// New 构造。
func New(db *gorm.DB, cfg *config.Config, st *storage.Store, rd render.Renderer, sg *sign.Signer, mgr *auth.Manager, oidc *auth.OIDC, sender *callback.Sender, mail *mailer.Sender, ips *clientip.Resolver) *Server {
	return &Server{DB: db, Cfg: cfg, Store: st, Renderer: rd, Signer: sg, Auth: mgr, OIDC: oidc, Sender: sender, Mailer: mail, IPs: ips}
}

// clientIP 取请求的真实来源 IP。解析规则见 internal/clientip。
func (s *Server) clientIP(r *http.Request) string {
	return s.IPs.ClientIP(r)
}

// Mount 注册全部路由。
func (s *Server) Mount(r chi.Router) {
	// 安全头放在最外层：即使请求在路由/认证阶段就被拒（404、401、429），
	// 响应也带着 nosniff 与 CSP，不会有一类"裸响应"漏出去。
	r.Use(s.securityHeaders)
	r.Use(requestID)
	r.Get("/api/v1/healthz", s.handleHealthz)
	// 版本与更新检查。公开的理由见 system_handlers.go：
	// 版本号不是秘密，且登录页也该能看见升级提示。结果有 6h 缓存，
	// 外部打不穿；UPDATE_CHECK=false 可整个关掉（隔离内网必须能用）。
	r.Get("/api/v1/version", s.handleVersion)
	r.Get("/api/v1/updates", s.handleUpdates)

	// 安装向导（未初始化时才有内容，装完这两个路由永久 404，见 install.go）
	r.Get("/install", s.handleInstallPage)
	r.Get("/install.js", s.handleInstallAsset)

	// 公开认证端点（在 Authenticate 之外）
	r.Post("/api/v1/auth/login", s.handleLogin)
	r.Post("/api/v1/auth/dev-login", s.handleDevLogin)
	r.Post("/api/v1/auth/register", s.handleRegister)
	r.Get("/api/v1/auth/setup-status", s.handleSetupStatus)
	r.Post("/api/v1/auth/setup", s.handleSetup)
	r.Get("/api/v1/auth/oidc/start", s.handleOIDCStart)
	r.Get("/api/v1/auth/oidc/callback", s.handleOIDCCallback)

	api := chi.NewRouter()
	api.Use(s.Auth.Authenticate)

	// 登录态认证端点
	api.Post("/auth/logout", s.handleLogout)
	api.Get("/auth/me", s.handleMe)
	api.Put("/auth/guide", s.handleGuide)
	api.Post("/auth/password", s.handlePassword)
	api.Get("/auth/oidc/bind/start", s.handleOIDCBindStart)
	api.Post("/auth/oidc/unbind", s.handleOIDCUnbind)

	// 管理员
	api.Get("/admin/users", s.handleListUsers)
	api.Post("/admin/users", s.handleCreateUser)
	api.Post("/admin/users/{id}/approve", s.handleApproveUser)
	api.Post("/admin/users/{id}/disable", s.handleDisableUser)
	api.Post("/admin/users/{id}/enable", s.handleEnableUser)
	api.Post("/admin/users/{id}/role", s.handleSetRole)
	api.Post("/admin/users/{id}/reset-password", s.handleResetPassword)
	api.Get("/admin/settings", s.handleAdminSettings)
	api.Put("/admin/settings", s.handleAdminSettingsPut)
	// V3.2 邮件服务（SMTP 只在服务端配置）
	api.Get("/admin/mail/settings", s.handleMailSettings)
	api.Put("/admin/mail/settings", s.handleMailSettingsPut)
	api.Post("/admin/mail/test", s.handleMailTest)
	api.Get("/admin/audit-logs/verify", s.handleAuditVerify)
	api.Get("/admin/audit-logs/export", s.handleAuditExport)

	// 审计日志
	api.Get("/audit-logs", s.handleAuditLogs)
	api.Get("/admin/audit-logs", s.handleAuditLogsAdmin)

	// V3 签名/印章素材库
	api.Post("/signatures/render-preview", s.handleRenderSignaturePreview)
	// usable 必须注册在 /{id} 之前：静态段优先由 chi 保证，但显式排前面更不容易
	// 在以后换路由库时踩坑。
	api.Get("/signatures/usable", func(w http.ResponseWriter, r *http.Request) {
		s.handleUsableAssets(w, r, "signature")
	})
	api.Get("/signatures", s.handleListSignatures)
	api.Post("/signatures", s.handleCreateSignature)
	api.Get("/signatures/{id}", s.handleGetSignature)
	api.Delete("/signatures/{id}", s.handleDeleteSignature)
	api.Get("/signatures/{id}/file", s.handleSignatureFile)
	api.Get("/signatures/{id}/image", s.handleSignatureFile)
	api.Put("/signatures/{id}", s.handleUpdateSignature)
	api.Post("/seals/render-preview", s.handleRenderSealPreview)
	api.Get("/seals/usable", func(w http.ResponseWriter, r *http.Request) {
		s.handleUsableAssets(w, r, "seal")
	})
	api.Get("/seals", s.handleListSeals)
	api.Post("/seals", s.handleCreateSeal)
	api.Get("/seals/{id}", s.handleGetSeal)
	api.Delete("/seals/{id}", s.handleDeleteSeal)
	api.Get("/seals/{id}/file", s.handleSealFile)
	api.Get("/seals/{id}/image", s.handleSealFile)
	api.Put("/seals/{id}", s.handleUpdateSeal)
	api.Get("/render/fonts", s.handleFonts)
	// 字体导入（admin）：上传 → 落盘 → 让渲染服务刷新 fontconfig 缓存
	api.Get("/fonts", s.handleListFonts)
	api.Post("/fonts", s.handleUploadFont)
	api.Delete("/fonts/{name}", s.handleDeleteFont)

	// V3 审核
	api.Get("/review/queue", s.handleReviewQueue)
	api.Post("/review/{resource_type}/{id}/approve", s.handleReviewApprove)
	api.Post("/review/{resource_type}/{id}/reject", s.handleReviewReject)

	// V3.1 签章授权
	api.Get("/grants", s.handleMyGrants)
	api.Post("/grants/{id}/renew", s.handleRenewGrant)
	api.Post("/grants/{id}/revoke", s.handleRevokeGrant)
	api.Post("/signatures/{id}/grants", s.handleCreateSignatureGrants)
	api.Get("/signatures/{id}/grants", s.handleListSignatureGrants)
	api.Post("/seals/{id}/grants", s.handleCreateSealGrants)
	api.Get("/seals/{id}/grants", s.handleListSealGrants)

	// templates
	api.Post("/templates", s.handleCreateTemplate)
	api.Get("/templates", s.handleListTemplates)
	api.Get("/templates/code/{code}", s.handleTemplateByCode)
	api.Get("/templates/{id}", s.handleTemplateDetail)
	api.Post("/templates/{id}/versions", s.handleAddVersion)
	api.Get("/templates/{id}/versions", s.handleListVersions)
	api.Post("/templates/{id}/versions/{vid}/activate", s.handleActivateVersion)
	api.Post("/templates/{id}/versions/{vid}/rollback", s.handleRollbackVersion)
	api.Post("/templates/{id}/disable", s.handleDisableTemplate)
	api.Post("/templates/{id}/enable", s.handleEnableTemplate)
	api.Delete("/templates/{id}", s.handleDeleteTemplate)
	api.Get("/templates/{id}/thumbnail", s.handleThumbnail)
	api.Get("/templates/{id}/versions/{vid}/download", s.handleDownloadVersion)

	// 在线编辑器（V2）
	api.Get("/templates/{id}/editor/model", s.handleEditorModel)
	api.Post("/templates/{id}/editor/apply", s.handleEditorApply)
	api.Post("/templates/{id}/editor/save", s.handleEditorSave)
	api.Post("/templates/{id}/editor/export", s.handleEditorExport)

	// 资源库（V2）
	api.Get("/templates/{id}/assets", s.handleListAssets)
	api.Post("/templates/{id}/assets", s.handleUploadAsset)
	api.Delete("/templates/{id}/assets", s.handleDeleteAssets)
	api.Get("/templates/{id}/assets/{aid}/file", s.handleAssetFile)
	api.Delete("/templates/{id}/assets/{aid}", s.handleDeleteAsset)

	// 变量元数据（V2）
	api.Get("/templates/{id}/variables/meta", s.handleGetVarMeta)
	api.Put("/templates/{id}/variables/meta", s.handlePutVarMeta)

	api.Get("/wizard/blank-template.docx", s.handleBlankTemplate)

	// generate / render / preview / validate
	api.Post("/generate", s.handleGenerate)
	api.Post("/render", s.handleRender)
	api.Post("/preview", s.handlePreview)
	api.Get("/preview/{pv}/page/{n}", s.handlePreviewPage)
	api.Post("/validate", s.handleValidate)

	// jobs / documents
	api.Get("/jobs", s.handleListJobs)
	api.Get("/jobs/{id}", s.handleJobDetail)
	api.Get("/jobs/{id}/download", s.handleJobDownload)
	api.Post("/jobs/{id}/retry", s.handleJobRetry)
	api.Post("/jobs/{id}/callbacks/replay", s.handleReplayCallback)
	api.Get("/callback-logs", s.handleCallbackLogs)

	api.Get("/documents", s.handleListDocuments)
	api.Get("/documents/{id}", s.handleDocumentDetail)
	api.Get("/documents/{id}/download", s.handleDocumentDownload)
	api.Post("/documents/{id}/regenerate", s.handleDocumentRegenerate)
	api.Post("/signed-url", s.handleSignedURL)

	// api keys
	api.Get("/keys", s.handleListKeys)
	api.Post("/keys", s.handleCreateKey)
	api.Post("/keys/{id}/disable", s.handleDisableKey)
	api.Post("/keys/{id}/enable", s.handleEnableKey)
	api.Delete("/keys/{id}", s.handleDeleteKey)

	r.Mount("/api/v1", api)

	// 签名直链（无需鉴权，HMAC 自带凭证）
	r.Get("/api/v1/s/{token}", s.handleSignedDownload)

	// SPA fallback
	// / 要单独判：未登录时给官网，已登录才进工作台（判定见 landing.go）。
	r.Get("/", s.handleRoot)
	// 官网静态资源（目前只有 landing.js）。CSP 是 script-src 'self'，
	// 所以它必须是文件，不能内联在 index.html 里。
	r.Get("/landing.js", s.handleSiteAsset)
	r.Get("/welcome", s.handleWelcome)
	r.Get("/*", s.handleSPA)
}

func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 8)
		_, _ = rand.Read(b)
		rid := hex.EncodeToString(b)
		w.Header().Set("X-Request-ID", rid)
		next.ServeHTTP(w, r)
	})
}

// spaFS 是内嵌的 web/dist 文件系统（静态资源服务）。
var spaFS = func() http.Handler {
	sub, err := fsSubDir()
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	}
	return http.FileServer(http.FS(sub))
}()

func fsSubDir() (fs.FS, error) {
	return fs.Sub(docforge.WebFS, "web/dist")
}

func (s *Server) handleSPA(w http.ResponseWriter, r *http.Request) {
	// /assets/* 等真实文件直接服务；不存在则回退 index.html（history fallback）。
	if r.URL.Path != "/" {
		if f, err := docforge.WebFS.Open("web/dist" + r.URL.Path); err == nil {
			st, _ := f.Stat()
			_ = f.Close()
			if !st.IsDir() {
				spaFS.ServeHTTP(w, r)
				return
			}
		}
	}
	data, err := docforge.WebFS.ReadFile("web/dist/index.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// ---- 错误信封 ----

type apiErr struct {
	Status  int
	Code    string
	Message string
	Details []render.FieldError
}

func (e *apiErr) Error() string { return e.Message }

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, message string, details []render.FieldError) {
	if details == nil {
		details = []render.FieldError{}
	}
	writeJSON(w, status, map[string]interface{}{
		"error": map[string]interface{}{
			"code":    code,
			"message": message,
			"details": details,
		},
	})
}

func errBadRequest(msg string) *apiErr  { return &apiErr{http.StatusBadRequest, "data_invalid", msg, nil} }
func errNotFound(msg string) *apiErr   { return &apiErr{http.StatusNotFound, "not_found", msg, nil} }
func errLocked(msg string) *apiErr      { return &apiErr{http.StatusLocked, "template_disabled", msg, nil} }

func (s *Server) renderErr(w http.ResponseWriter, err error) {
	// 用 errors.As 而不是类型断言：错误可能被上层用 %w 包过一层来说明「是哪个
	// 签章/哪次调用出的错」，类型断言一旦遇上包装就会掉到下面的 500 分支，
	// 把用户的输入错误（422）说成「服务内部错误」。
	var ae *apiErr
	if errors.As(err, &ae) {
		writeErr(w, ae.Status, ae.Code, ae.Message, ae.Details)
		return
	}
	var re *render.Error
	if errors.As(err, &re) {
		status := re.StatusCode
		if status == 0 || status >= 600 {
			status = http.StatusBadGateway
		}
		writeErr(w, status, re.Code, re.Message, re.Details)
		return
	}
	log.Printf("[api] internal_error: %v", err)
	writeErr(w, http.StatusInternalServerError, "internal_error", "服务内部错误，请稍后重试", nil)
}

// ---- 分页工具 ----

type pageQuery struct {
	Page     int
	PageSize int
}

func getPage(r *http.Request) pageQuery {
	p, _ := strconv.Atoi(r.URL.Query().Get("page"))
	ps, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	if p < 1 {
		p = 1
	}
	if ps < 1 || ps > 100 {
		ps = 20
	}
	return pageQuery{Page: p, PageSize: ps}
}

func (s *Server) guardPrivate() *netguard.Checker {
	return netguard.New(s.Cfg.CallbackAllowPrivate, s.Cfg.CallbackHostAllow)
}

var _ = worker.BuildFilename
