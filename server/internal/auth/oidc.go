// Package auth — oidc.go：可选 APSSO OIDC（Authorization Code + PKCE S256 + state）。
// 标准 Discovery，端点禁止硬编码；与本地登录并存、不互斥。
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"docforge/server/internal/idgen"
	"docforge/server/internal/models"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
	"gorm.io/gorm"
)

// OIDC 封装发现、PKCE 授权码流程与用户关联/自动建号。
type OIDC struct {
	mgr      *Manager
	provider *oidc.Provider
	oauthCfg *oauth2.Config
	verifier *oidc.IDTokenVerifier
}

// NewOIDC 做 Discovery。未启用/未配置 client 时返回 nil。
func NewOIDC(mgr *Manager) (*OIDC, error) {
	cfg := mgr.Cfg
	if !cfg.OIDCEnabled || cfg.OIDCClientID == "" {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	prov, err := oidc.NewProvider(ctx, cfg.OIDCIssuer)
	if err != nil {
		return nil, err
	}
	o := &OIDC{mgr: mgr, provider: prov}
	// 认证方式：公共客户端（无 client secret）必须走 token_endpoint_auth_method=none，
	// 即 client_id 放在请求体、不带 client_secret。
	//
	// 这里必须显式钉住 AuthStyle，不能依赖 oauth2 的自动探测：AutoDetect 会先发一次
	// client_secret_basic（Authorization: Basic base64(id:)），公共客户端在多数 IdP
	// （含 APSSO）上会被判 invalid_client 拒绝。AuthStyleInParams 在 secret 为空时
	// 不会写 client_secret 字段，正好等价于 none。
	ep := prov.Endpoint()
	if cfg.OIDCClientSec == "" {
		ep.AuthStyle = oauth2.AuthStyleInParams
	}
	o.oauthCfg = &oauth2.Config{
		ClientID:     cfg.OIDCClientID,
		ClientSecret: cfg.OIDCClientSec,
		Endpoint:     ep,
		RedirectURL:  cfg.OIDCRedirect,
		Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
	}
	o.verifier = prov.Verifier(&oidc.Config{ClientID: cfg.OIDCClientID})
	return o, nil
}

// Enabled 是否可用。
func (o *OIDC) Enabled() bool { return o != nil && o.provider != nil }

// flowCookie 存储 state + PKCE verifier + 是否绑定模式。
const flowCookie = "df_oidc_flow"

func (o *OIDC) setFlow(w http.ResponseWriter, state, verifier, bind string) {
	raw := state + "|" + verifier + "|" + bind
	mac := hmac.New(sha256.New, o.mgr.secret())
	_, _ = mac.Write([]byte(raw))
	val := base64.RawURLEncoding.EncodeToString([]byte(raw)) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	http.SetCookie(w, &http.Cookie{Name: flowCookie, Value: val, Path: "/",
		HttpOnly: true, Secure: o.mgr.secureCookie(), SameSite: http.SameSiteLaxMode, MaxAge: 600})
}

func (o *OIDC) getFlow(r *http.Request) (state, verifier, bind string, ok bool) {
	c, err := r.Cookie(flowCookie)
	if err != nil {
		return "", "", "", false
	}
	parts := strings.Split(c.Value, ".")
	if len(parts) != 2 {
		return "", "", "", false
	}
	rawB, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", "", "", false
	}
	macB, _ := base64.RawURLEncoding.DecodeString(parts[1])
	mac := hmac.New(sha256.New, o.mgr.secret())
	_, _ = mac.Write(rawB)
	if !hmac.Equal(mac.Sum(nil), macB) {
		return "", "", "", false
	}
	fields := strings.Split(string(rawB), "|")
	if len(fields) != 3 {
		return "", "", "", false
	}
	return fields[0], fields[1], fields[2], true
}

// Start 发起授权（bindUserID 非空表示把 sub 绑到当前登录用户）。
func (o *OIDC) Start(w http.ResponseWriter, r *http.Request, bindUserID string) {
	state := idgen.B64(16)
	verifier := idgen.B64(32)
	o.setFlow(w, state, verifier, bindUserID)

	// PKCE：code_challenge 由 oauth2 内部按 BASE64URL(SHA256(verifier)) 计算。
	//
	// ⚠️ S256ChallengeOption 的参数语义是 **code_verifier 本身**，不是 challenge。
	// 这里曾经写的是 s256Challenge(verifier)，等于自己先算一遍再让 oauth2 算一遍，
	// 实际发出去的是 S256(S256(verifier))；而回调换 token 时 VerifierOption 发的是
	// 原始 verifier，APSSO 侧比对结果必然不符 —— 表现为授权码拿到了、回调却报
	// invalid_grant "PKCE 校验失败"。所以这个"自己算 challenge"的辅助函数已删除：
	// 留着它只会引诱下一个人再传一次。
	u := o.oauthCfg.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier))
	http.Redirect(w, r, u, http.StatusFound)
}

// Callback 处理授权回调。返回结果与错误（handler 负责 302/错误页）。
func (o *OIDC) Callback(w http.ResponseWriter, r *http.Request) error {
	state, verifier, bind, ok := o.getFlow(r)
	if !ok || state == "" || r.URL.Query().Get("state") != state {
		return fmt.Errorf("invalid oidc state")
	}
	http.SetCookie(w, &http.Cookie{Name: flowCookie, Value: "", Path: "/",
		Secure: o.mgr.secureCookie(), MaxAge: -1})

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	tok, err := o.oauthCfg.Exchange(ctx, r.URL.Query().Get("code"), oauth2.VerifierOption(verifier))
	if err != nil {
		return err
	}
	rawID, _ := tok.Extra("id_token").(string)
	if _, err := o.verifier.Verify(ctx, rawID); err != nil {
		return err
	}
	ui, err := o.provider.UserInfo(ctx, oauth2.StaticTokenSource(tok))
	if err != nil {
		return err
	}
	claims := struct {
		Email          string `json:"email"`
		EmailVerified  bool   `json:"email_verified"`
		Name           string `json:"name"`
		Preferred      string `json:"preferred_username"`
	}{}
	_ = ui.Claims(&claims)

	// 绑定模式：把 sub 绑到当前登录用户。
	if bind != "" {
		var u models.User
		if err := o.mgr.DB.First(&u, "id = ?", bind).Error; err != nil {
			return fmt.Errorf("绑定目标用户不存在")
		}
		// 若该 sub 已绑给别人，先解绑别人。
		o.mgr.DB.Model(&models.User{}).Where("oidc_sub = ? AND id <> ?", ui.Subject, bind).Update("oidc_sub", nil)
		o.mgr.DB.Model(&u).Updates(map[string]interface{}{"oidc_sub": ui.Subject})
		return nil
	}

	u, err := o.resolveUser(ui.Subject, claims.Email, claims.EmailVerified, claims.Preferred, claims.Name)
	if err != nil {
		return err
	}
	return o.mgr.IssueCookie(w, u)
}

// resolveUser 按契约顺序关联用户：oidc_sub → verified email → 自动建号。
func (o *OIDC) resolveUser(sub, email string, emailVerified bool, preferred, name string) (*models.User, error) {
	db := o.mgr.DB
	var u models.User

	// 1) oidc_sub 直配
	if err := db.First(&u, "oidc_sub = ?", sub).Error; err == nil {
		return o.postLogin(&u)
	}
	// 2) verified email 匹配本地账号（email 或 username 相同）
	if emailVerified && email != "" {
		if err := db.First(&u, "email = ? OR username = ?", email, email).Error; err == nil {
			db.Model(&u).Update("oidc_sub", sub)
			u.OIDCSub = &sub
			return o.postLogin(&u)
		}
	}
	// 3) 自动建号
	mode := strings.ToLower(o.mgr.Cfg.OIDCAutoProvision)
	switch mode {
	case "none":
		return nil, fmt.Errorf("账号未开通，请联系管理员启用 APSSO 登录")
	case "pending":
		return nil, fmt.Errorf("APSSO 账号待管理员审核")
	}
	// active（默认）
	username := preferred
	if username == "" {
		username = strings.Split(email, "@")[0]
	}
	if username == "" {
		username = "oidc_" + idgen.Hex(4)
	}
	username = uniqueUsername(db, username)
	if name == "" {
		name = username
	}
	u = models.User{
		ID: "u_" + idgen.Hex(8), Username: username, DisplayName: name,
		Email: email, Role: "member", Status: "active", Source: "oidc", OIDCSub: &sub,
	}
	if err := db.Create(&u).Error; err != nil {
		return nil, err
	}
	// 必须走 postLogin，不能直接 return：它负责两件对新账号同样成立的事——
	// 记 last_login_at（否则用户列表里这个刚登录过的人显示「从未登录」），
	// 以及按 ADMIN_USERNAMES 复查提升（启动时那次名单里还没有他，
	// 而这里正是「首次登录才建号」的人唯一一次被复查的机会）。
	return o.postLogin(&u)
}

func uniqueUsername(db *gorm.DB, base string) string {
	candidate := base
	i := 1
	for {
		var cnt int64
		db.Model(&models.User{}).Where("username = ?", candidate).Count(&cnt)
		if cnt == 0 {
			return candidate
		}
		candidate = fmt.Sprintf("%s%d", base, i)
		i++
	}
}

func (o *OIDC) postLogin(u *models.User) (*models.User, error) {
	if u.Status == "disabled" {
		return nil, fmt.Errorf("账号已被禁用，请联系管理员")
	}
	// 首次登录才建号的人，在这里复查管理员名单（启动时那次还没有他）
	PromoteIfListed(o.mgr.DB, o.mgr.Cfg, u)
	now := time.Now()
	o.mgr.DB.Model(u).Update("last_login_at", now)
	u.LastLoginAt = &now
	return u, nil
}

// Unbind 解绑当前用户的 oidc_sub（仅当 has_password）。
func (o *OIDC) Unbind(u *models.User) error {
	if !u.HasPassword() {
		return fmt.Errorf("请先设置登录密码再解绑 APSSO")
	}
	return o.mgr.DB.Model(u).Update("oidc_sub", nil).Error
}

var _ = json.Marshal
