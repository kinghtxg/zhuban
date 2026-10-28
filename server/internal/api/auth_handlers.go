package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"docforge/server/internal/auth"
	"docforge/server/internal/idgen"
	"docforge/server/internal/models"
	"docforge/server/internal/render"
)

// ---------- setup / setup-status ----------

// handleSetupStatus 公开端点：系统是否已初始化、OIDC/注册是否开启。
func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"initialized":          s.Auth.Initialized(),
		"oidc_enabled":         s.oidcAvailable(),
		"registration_enabled": s.Cfg.AllowRegister,
		"dev_login_enabled":    s.Cfg.AuthDev,
	})
}

// oidcAvailable 是「这个按钮点下去会不会成」的唯一判据。
//
// 不能用 Cfg.OIDCEnabled：那一项只说明"配置里写了启用"。NewOIDC 在启动时要做一次
// Discovery，APSSO 不可达 / issuer 配错 / client_id 为空都会让它返回 nil，此时
// OIDCEnabled 仍然是 true —— 登录页照样渲染出「使用 APSSO 登录」，用户点下去
// 拿到 500，而且看不出是自己环境的问题还是产品的问题。
//
// 对外承诺「有这个能力」的前提是它此刻真的能用，所以这里取运行时的真实状态。
func (s *Server) oidcAvailable() bool {
	return s.OIDC.Enabled()
}

// handleSetup 一次性创建首个管理员并直接登录。完成后永久 404。
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if s.Auth.Initialized() {
		writeErr(w, http.StatusNotFound, "not_found", "系统已完成初始化，该接口已关闭", nil)
		return
	}
	if !s.Auth.Guard.AllowIP(s.clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "rate_limited", "操作过于频繁，请稍后再试", nil)
		return
	}
	var body struct {
		Username    string `json:"username"`
		Password    string `json:"password"`
		DisplayName string `json:"display_name"`
		Email       string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "data_invalid", "请求体不是合法 JSON", nil)
		return
	}
	if msg := auth.ValidateUsername(body.Username); msg != "" {
		writeErr(w, http.StatusUnprocessableEntity, "data_invalid", msg, []render.FieldError{{Path: "username", Message: msg}})
		return
	}
	if msg := auth.ValidatePasswordRule(body.Password); msg != "" {
		writeErr(w, http.StatusUnprocessableEntity, "data_invalid", msg, []render.FieldError{{Path: "password", Message: msg}})
		return
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		s.renderErr(w, err)
		return
	}
	u := models.User{
		ID: "u_" + randID(), Username: body.Username, PasswordHash: hash,
		DisplayName: orDefault(body.DisplayName, body.Username), Email: body.Email,
		Role: "admin", Status: "active", Source: "local",
	}
	if err := s.DB.Create(&u).Error; err != nil {
		writeErr(w, http.StatusConflict, "conflict", "用户名已被占用", nil)
		return
	}
	if err := s.Auth.IssueCookie(w, &u); err != nil {
		s.renderErr(w, err)
		return
	}
	// 装完了：把安装页从磁盘上删掉。
	//
	// 放在发完 cookie 之后：删页面失败不该让用户卡在"账号建好了但没登录"
	// 的状态里。路由层已经会 404（Initialized 已为真），这一步只是把
	// 磁盘上那份也清掉，不留"页面还在但打不开"的疑团。
	s.removeInstallPage()
	writeJSON(w, http.StatusOK, s.sessionPayload(&u))
}

// ---------- 本地登录 / 注册 / 登出 ----------

// handleLogin 本地账号密码登录。
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.Auth.Guard.AllowIP(s.clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "rate_limited", "操作过于频繁，请稍后再试", nil)
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "data_invalid", "请求体不是合法 JSON", nil)
		return
	}
	body.Username = strings.TrimSpace(body.Username)

	if left := s.Auth.Guard.LockedRemaining(body.Username); left > 0 {
		writeErr(w, http.StatusLocked, "account_locked",
			"账号已被临时锁定，请 "+strconv.Itoa(left)+" 秒后重试", nil)
		return
	}

	var u models.User
	err := s.DB.First(&u, "username = ?", body.Username).Error
	if err != nil || !u.HasPassword() {
		s.Auth.Guard.RecordFailure(body.Username)
		writeErr(w, http.StatusUnauthorized, "invalid_credentials", "用户名或密码错误", nil)
		return
	}
	ok, verr := auth.VerifyPassword(body.Password, u.PasswordHash)
	if verr != nil || !ok {
		s.Auth.Guard.RecordFailure(body.Username)
		writeErr(w, http.StatusUnauthorized, "invalid_credentials", "用户名或密码错误", nil)
		return
	}
	if u.Status == "disabled" {
		writeErr(w, http.StatusForbidden, "forbidden", "账号已被禁用，请联系管理员", nil)
		return
	}
	if u.Status == "pending" {
		writeErr(w, http.StatusForbidden, "pending", "账号待管理员审核通过后再登录", nil)
		return
	}
	s.Auth.Guard.Clear(body.Username)
	// 名单里的账号登录即提升为管理员（OIDC 建号默认 member，本地账号也可能被降过）
	auth.PromoteIfListed(s.DB, s.Cfg, &u)
	now := time.Now()
	s.DB.Model(&u).Update("last_login_at", now)
	u.LastLoginAt = &now
	if err := s.Auth.IssueCookie(w, &u); err != nil {
		s.renderErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.sessionPayload(&u))
}

// handleRegister 自助注册（pending 待审核），默认关闭返回 404。
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if !s.Cfg.AllowRegister {
		writeErr(w, http.StatusNotFound, "not_found", "自助注册未开启", nil)
		return
	}
	if !s.Auth.Guard.AllowIP(s.clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "rate_limited", "操作过于频繁，请稍后再试", nil)
		return
	}
	var body struct {
		Username    string `json:"username"`
		Password    string `json:"password"`
		Email       string `json:"email"`
		DisplayName string `json:"display_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "data_invalid", "请求体不是合法 JSON", nil)
		return
	}
	if msg := auth.ValidateUsername(body.Username); msg != "" {
		writeErr(w, http.StatusUnprocessableEntity, "data_invalid", msg, []render.FieldError{{Path: "username", Message: msg}})
		return
	}
	if msg := auth.ValidatePasswordRule(body.Password); msg != "" {
		writeErr(w, http.StatusUnprocessableEntity, "data_invalid", msg, []render.FieldError{{Path: "password", Message: msg}})
		return
	}
	var cnt int64
	s.DB.Model(&models.User{}).Where("username = ?", body.Username).Count(&cnt)
	if cnt > 0 {
		writeErr(w, http.StatusConflict, "conflict", "用户名已被占用", nil)
		return
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		s.renderErr(w, err)
		return
	}
	u := models.User{
		ID: "u_" + randID(), Username: body.Username, PasswordHash: hash,
		DisplayName: orDefault(body.DisplayName, body.Username), Email: body.Email,
		Role: "member", Status: "pending", Source: "local",
	}
	if err := s.DB.Create(&u).Error; err != nil {
		writeErr(w, http.StatusConflict, "conflict", "用户名已被占用", nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "message": "注册成功，等待管理员审核"})
}

// handleLogout 清会话。
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	// 先撤服务端那一条：只清 cookie 的话，「退出登录」只是让浏览器不再发它，
	// 被复制走的那份仍然是一张能用的门票。
	s.Auth.RevokeSession(r)
	s.Auth.ClearCookie(w)
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

// handleDevLogin 开发模式一键登录（upsert active admin）。
func (s *Server) handleDevLogin(w http.ResponseWriter, r *http.Request) {
	if !s.Cfg.AuthDev {
		writeErr(w, http.StatusForbidden, "forbidden", "当前未开启开发登录（AUTH_DEV != true）", nil)
		return
	}
	var body struct {
		Username string `json:"username"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Username == "" {
		body.Username = "dev"
	}
	u, err := s.Auth.UpsertDevForLogin(r.Context(), body.Username)
	if err != nil {
		s.renderErr(w, err)
		return
	}
	if err := s.Auth.IssueCookie(w, u); err != nil {
		s.renderErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.sessionPayload(u))
}

// ---------- me / guide / password ----------

// sessionPayload 是「本次登录拿到的主体」，/auth/me 与三个登录入口共用同一份。
//
// 契约 §4.2 写的是「login 200 返回与 /me 相同的主体」，前端 api/endpoints.ts 里
// login 的返回类型也直接声明成了 MeResponse。之前登录返回的是 publicUser
// （只有 id/username/display_name/email），role/status/source 全部缺失——
// 而「刚登录就判断自己是不是管理员」恰恰是登录后立刻要做的事，
// 照着类型读 user.role 拿到的是 undefined，前端会静默当成普通成员。
//
// 所以这里不复制字段，而是让两边调同一个函数：以后加字段只需改一处，
// 不会再出现「/me 有、登录没有」这种只在一半接口上生效的情况。
func (s *Server) sessionPayload(u *models.User) map[string]interface{} {
	return map[string]interface{}{
		"user":                 publicUserFull(u),
		"auth_mode":            authModeOf(u),
		"oidc_enabled":         s.oidcAvailable(),
		"registration_enabled": s.Cfg.AllowRegister,
		"initialized":          true,
		"guide_seen":           u.GuideSeen,
		"base_url":             s.Cfg.ExternalBaseURL,
	}
}

// handleMe 返回当前登录主体（形状见契约 §4.2）。
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFrom(r)
	if p == nil || p.User == nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "未登录", nil)
		return
	}
	writeJSON(w, http.StatusOK, s.sessionPayload(p.User))
}

// handleGuide 持久化首次引导标记。
func (s *Server) handleGuide(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFrom(r)
	if p == nil || p.User == nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "未登录", nil)
		return
	}
	var body struct {
		Seen bool `json:"seen"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.DB.Model(&models.User{}).Where("id = ?", p.User.ID).Update("guide_seen", body.Seen)
	writeJSON(w, http.StatusOK, map[string]interface{}{"guide_seen": body.Seen})
}

// handlePassword 修改本人密码（无密码 OIDC 账号 old_password 可空）。
func (s *Server) handlePassword(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFrom(r)
	if p == nil || p.User == nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "未登录", nil)
		return
	}
	var body struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "data_invalid", "请求体不是合法 JSON", nil)
		return
	}
	if p.User.HasPassword() {
		ok, err := auth.VerifyPassword(body.OldPassword, p.User.PasswordHash)
		if err != nil || !ok {
			writeErr(w, http.StatusBadRequest, "invalid_credentials", "原密码不正确", nil)
			return
		}
	}
	if msg := auth.ValidatePasswordRule(body.NewPassword); msg != "" {
		writeErr(w, http.StatusUnprocessableEntity, "data_invalid", msg, []render.FieldError{{Path: "new_password", Message: msg}})
		return
	}
	hash, err := auth.HashPassword(body.NewPassword)
	if err != nil {
		s.renderErr(w, err)
		return
	}
	s.DB.Model(p.User).Update("password_hash", hash)
	// 改密之后把其它设备上的登录踢掉。保留当前这条：否则用户改完密码
	// 立刻被弹回登录页，会被当成"改密失败"。
	s.Auth.RevokeOtherSessions(r, p.User.ID)
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

// ---------- OIDC ----------

// handleOIDCStart 发起 APSSO 登录授权。
func (s *Server) handleOIDCStart(w http.ResponseWriter, r *http.Request) {
	if s.OIDC == nil || !s.OIDC.Enabled() {
		writeErr(w, http.StatusForbidden, "forbidden", "APSSO 未启用", nil)
		return
	}
	s.OIDC.Start(w, r, "")
}

// handleOIDCBindStart 已登录态发起绑定。
func (s *Server) handleOIDCBindStart(w http.ResponseWriter, r *http.Request) {
	if s.OIDC == nil || !s.OIDC.Enabled() {
		writeErr(w, http.StatusForbidden, "forbidden", "APSSO 未启用", nil)
		return
	}
	p := auth.PrincipalFrom(r)
	if p == nil || p.User == nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "未登录", nil)
		return
	}
	s.OIDC.Start(w, r, p.User.ID)
}

// handleOIDCCallback 处理授权回调。
func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	if s.OIDC == nil || !s.OIDC.Enabled() {
		writeErr(w, http.StatusForbidden, "forbidden", "APSSO 未启用", nil)
		return
	}
	if err := s.OIDC.Callback(w, r); err != nil {
		// 不要把 err.Error() 直接吐给浏览器。三个理由：
		//   1) 用户看不懂 —— "oauth2: invalid_grant PKCE 校验失败" 只会让人以为是自己
		//      账号或密码错了，于是反复重试、来问"我密码没问题啊"；
		//   2) 把 IdP 的内部错误码和实现细节暴露到公网；
		//   3) http.Error 输出的是裸文本，页面上没有任何出路，用户只能关标签页。
		// 所以页面只给一句人话 + 回登录的入口，真正的错误用 trace 号到服务端日志对。
		trace := idgen.B64(6)
		log.Printf("[auth] OIDC 回调失败 trace=%s err=%v", trace, err)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>单点登录未完成 · 铸版</title>
<style>body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;
background:#f7f8fa;color:#1d2129;font:15px/1.7 -apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC","Microsoft YaHei",sans-serif}
.card{background:#fff;border:1px solid #e5e6eb;border-radius:10px;padding:32px 36px;max-width:460px;
box-shadow:0 8px 24px rgba(31,35,41,.08)}h1{margin:0 0 12px;font-size:18px}
p{margin:0 0 10px;color:#4e5969}code{background:#f2f3f5;padding:2px 8px;border-radius:4px;
font-size:13px;color:#86909c}a{display:inline-block;margin-top:18px;padding:9px 22px;
background:#165dff;color:#fff;border-radius:6px;text-decoration:none;font-size:14px}</style>
</head><body><div class="card"><h1>单点登录未完成</h1>
<p>APSSO 没有完成这次授权。常见原因是登录已过期，或浏览器拦截了跨站 Cookie。</p>
<p>请返回重新登录；如果反复出现，把下面的编号发给管理员。</p>
<p><code>%s</code></p>
<a href="/">返回登录</a></div></body></html>`, trace)
		return
	}
	// 绑定成功不重定向到首页（由前端 toast）；登录成功已种 cookie。
	http.Redirect(w, r, "/", http.StatusFound)
}

// handleOIDCUnbind 解绑当前用户 APSSO。
func (s *Server) handleOIDCUnbind(w http.ResponseWriter, r *http.Request) {
	if s.OIDC == nil || !s.OIDC.Enabled() {
		writeErr(w, http.StatusForbidden, "forbidden", "APSSO 未启用", nil)
		return
	}
	p := auth.PrincipalFrom(r)
	if p == nil || p.User == nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "未登录", nil)
		return
	}
	if err := s.OIDC.Unbind(p.User); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

// ---------- 输出辅助 ----------

func publicUser(u *models.User) map[string]interface{} {
	return map[string]interface{}{
		"id": u.ID, "username": u.Username, "display_name": u.DisplayName, "email": u.Email,
	}
}

func publicUserFull(u *models.User) map[string]interface{} {
	return map[string]interface{}{
		"id": u.ID, "username": u.Username, "display_name": u.DisplayName, "email": u.Email,
		"role": u.Role, "status": u.Status, "source": u.Source,
		"oidc_bound": u.OIDCSub != nil && *u.OIDCSub != "", "has_password": u.HasPassword(),
		"last_login_at": u.LastLoginAt,
		"created_at": u.CreatedAt, "updated_at": u.UpdatedAt,
	}
}

func authModeOf(u *models.User) string {
	switch u.Source {
	case "oidc":
		return "oidc"
	case "local":
		return "local"
	}
	return "local"
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}
