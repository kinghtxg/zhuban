package auth

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"docforge/server/internal/config"
	"docforge/server/internal/models"
	"docforge/server/internal/store"
)

// OIDC 这一组用一个**本地假 IdP**跑完整授权码流程，而不是断言「函数被调用了」。
//
// 为什么必须真跑：PKCE 的正确性只体现在两侧的中间值是否对得上。
// 项目历史上踩过一次「把 verifier 又哈希一遍」的坑，表现是授权码拿到了、回调报
// invalid_grant —— 只断言「请求里带了 code_challenge」根本发现不了。
// 所以这个假 IdP 会真的校验 S256(code_verifier) == 记录的 challenge。

// ---- 假 IdP ----

type issuedCode struct {
	challenge string
	sub       string
	email     string
	verified  bool
	preferred string
	name      string
}

type fakeIdP struct {
	srv       *httptest.Server
	key       *rsa.PrivateKey
	kid       string
	mu        sync.Mutex
	codes     map[string]*issuedCode
	tokenHits int
	// 记录 token 请求里是否带了 client_secret（公共客户端不该带）
	sawClientSecret bool
}

func b64u(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("生成 RSA 密钥失败：%v", err)
	}
	f := &fakeIdP{key: key, kid: "test-key-1", codes: map[string]*issuedCode{}}
	mux := http.NewServeMux()

	var issuer string
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]interface{}{
			"issuer":                                issuer,
			"authorization_endpoint":                issuer + "/authorize",
			"token_endpoint":                        issuer + "/token",
			"userinfo_endpoint":                     issuer + "/userinfo",
			"jwks_uri":                              issuer + "/jwks",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		pub := key.PublicKey
		writeJSON(w, map[string]interface{}{"keys": []map[string]string{{
			"kty": "RSA", "use": "sig", "alg": "RS256", "kid": f.kid,
			"n": b64u(pub.N.Bytes()),
			"e": b64u(big.NewInt(int64(pub.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		f.tokenHits++
		if r.Form.Get("client_secret") != "" {
			f.sawClientSecret = true
		}
		code := f.codes[r.Form.Get("code")]
		f.mu.Unlock()
		if code == nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			writeJSON(w, map[string]string{"error": "invalid_grant", "error_description": "code 无效"})
			return
		}
		// PKCE 校验：这一步是整组测试的核心
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if b64u(sum[:]) != code.challenge {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			writeJSON(w, map[string]string{"error": "invalid_grant",
				"error_description": "PKCE 校验失败：challenge 与 verifier 不匹配"})
			return
		}
		claims := map[string]interface{}{
			"iss": issuer, "sub": code.sub, "aud": r.Form.Get("client_id"),
			"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
			"email": code.email, "email_verified": code.verified,
			"preferred_username": code.preferred, "name": code.name,
		}
		writeJSON(w, map[string]interface{}{
			"access_token": "at_" + code.sub,
			"token_type":   "Bearer",
			"expires_in":   3600,
			"id_token":     f.signJWT(claims),
		})
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		sub := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer at_")
		f.mu.Lock()
		var hit *issuedCode
		for _, c := range f.codes {
			if c.sub == sub {
				hit = c
				break
			}
		}
		f.mu.Unlock()
		if hit == nil {
			w.WriteHeader(401)
			return
		}
		writeJSON(w, map[string]interface{}{
			"sub": hit.sub, "email": hit.email, "email_verified": hit.verified,
			"preferred_username": hit.preferred, "name": hit.name,
		})
	})

	f.srv = httptest.NewServer(mux)
	issuer = f.srv.URL
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIdP) signJWT(claims map[string]interface{}) string {
	h, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": f.kid})
	c, _ := json.Marshal(claims)
	input := b64u(h) + "." + b64u(c)
	sum := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum[:])
	if err != nil {
		panic(err)
	}
	return input + "." + b64u(sig)
}

// issueCode 签发一个授权码，记下客户端给出的 challenge。
func (f *fakeIdP) issueCode(challenge string, c issuedCode) string {
	c.challenge = challenge
	code := "code_" + c.sub + "_" + fmt.Sprint(time.Now().UnixNano())
	f.mu.Lock()
	f.codes[code] = &c
	f.mu.Unlock()
	return code
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// ---- 脚手架 ----

type oidcFixture struct {
	m   *Manager
	o   *OIDC
	idp *fakeIdP
}

func newOIDCFixture(t *testing.T, mutate func(*config.Config)) *oidcFixture {
	t.Helper()
	idp := newFakeIdP(t)
	dir := t.TempDir()
	cfg := &config.Config{
		DataDir:     dir,
		DatabaseURL: "sqlite://" + filepath.Join(dir, "o.db"),
		AuthSecret:  "oidc-test-secret",
		OIDCEnabled: true,
		OIDCIssuer:  idp.srv.URL,
		OIDCClientID: "zhuban-test",
		OIDCRedirect: "https://zb.example.com/api/v1/auth/oidc/callback",
		OIDCAutoProvision: "active",
		ExternalBaseURL:   "https://zb.example.com",
	}
	if mutate != nil {
		mutate(cfg)
	}
	db, err := store.Open(cfg)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	m := New(db, cfg)
	o, err := NewOIDC(m)
	if err != nil {
		t.Fatalf("NewOIDC 失败：%v", err)
	}
	if o == nil {
		t.Fatal("启用且配了 client_id 时应返回可用的 OIDC")
	}
	return &oidcFixture{m: m, o: o, idp: idp}
}

// beginAuth 走一次 Start，返回 state 与回调请求（已带上 flow cookie）。
func (f *oidcFixture) beginAuth(t *testing.T, bind string) (state, challenge string, callbackReq func(code string) *http.Request) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/auth/oidc/start", nil)
	f.o.Start(rec, req, bind)

	loc := rec.Header().Get("Location")
	if loc == "" {
		t.Fatal("Start 没有产生 302 Location")
	}
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("Location 不是合法 URL：%v", err)
	}
	q := u.Query()
	state = q.Get("state")
	challenge = q.Get("code_challenge")
	if state == "" || challenge == "" {
		t.Fatalf("授权 URL 缺 state 或 code_challenge：%s", loc)
	}
	if got := q.Get("code_challenge_method"); got != "S256" {
		t.Fatalf("PKCE 必须用 S256，实际 %q", got)
	}
	flowCookies := rec.Result().Cookies()

	return state, challenge, func(code string) *http.Request {
		r := httptest.NewRequest("GET",
			"/api/v1/auth/oidc/callback?state="+url.QueryEscape(state)+"&code="+url.QueryEscape(code), nil)
		for _, c := range flowCookies {
			r.AddCookie(c)
		}
		return r
	}
}

// ---- 主流程 ----

// 完整链路：Start 发挑战 → 回调换 token（假 IdP 校验 PKCE）→ 建号 → 签发会话。
func TestOIDC_FullFlowCreatesUserAndSession(t *testing.T) {
	f := newOIDCFixture(t, nil)
	_, challenge, callback := f.beginAuth(t, "")
	code := f.idp.issueCode(challenge, issuedCode{
		sub: "sub-001", email: "zhangsan@example.com", verified: true,
		preferred: "zhangsan", name: "张三",
	})

	rec := httptest.NewRecorder()
	if err := f.o.Callback(rec, callback(code)); err != nil {
		t.Fatalf("回调失败：%v", err)
	}

	var u models.User
	if err := f.m.DB.First(&u, "oidc_sub = ?", "sub-001").Error; err != nil {
		t.Fatalf("应自动建号并绑定 sub：%v", err)
	}
	if u.Username != "zhangsan" || u.DisplayName != "张三" || u.Email != "zhangsan@example.com" {
		t.Errorf("账号资料没按 claims 落库：%+v", u)
	}
	if u.Role != "member" || u.Status != "active" || u.Source != "oidc" {
		t.Errorf("自动建号应为 member/active/oidc，实际 %s/%s/%s", u.Role, u.Status, u.Source)
	}
	if u.LastLoginAt == nil {
		t.Error("首次登录应记录 last_login_at")
	}
	if c := findCookie(t, rec, CookieName); c.Value == "" {
		t.Error("回调成功必须签发会话 cookie")
	}
	// 公共客户端不该带 client_secret 字段（带了会被多数 IdP 判 invalid_client）
	if f.idp.sawClientSecret {
		t.Error("未配置 client_secret 时 token 请求不该带 client_secret")
	}
}

// 回调里 flow cookie 被清掉：一次性凭据不能留着被重放。
func TestOIDC_CallbackClearsFlowCookie(t *testing.T) {
	f := newOIDCFixture(t, nil)
	_, challenge, callback := f.beginAuth(t, "")
	code := f.idp.issueCode(challenge, issuedCode{sub: "sub-clear", email: "a@example.com", verified: true})

	rec := httptest.NewRecorder()
	if err := f.o.Callback(rec, callback(code)); err != nil {
		t.Fatalf("回调失败：%v", err)
	}
	c := findCookie(t, rec, flowCookie)
	if c.MaxAge >= 0 {
		t.Errorf("flow cookie 应被删除（MaxAge 为负），实际 %d", c.MaxAge)
	}
}

// state 不匹配必须拒绝。否则攻击者可以把自己的授权码塞进受害者的回调里
// （login CSRF），受害者会以攻击者的身份使用系统。
func TestOIDC_RejectsStateMismatch(t *testing.T) {
	f := newOIDCFixture(t, nil)
	_, challenge, callback := f.beginAuth(t, "")
	code := f.idp.issueCode(challenge, issuedCode{sub: "sub-x", email: "x@example.com", verified: true})

	req := callback(code)
	q := req.URL.Query()
	q.Set("state", "tampered-state")
	req.URL.RawQuery = q.Encode()

	err := f.o.Callback(httptest.NewRecorder(), req)
	if err == nil || !strings.Contains(err.Error(), "state") {
		t.Fatalf("state 不匹配应报错，实际 err=%v", err)
	}
	var cnt int64
	f.m.DB.Model(&models.User{}).Where("oidc_sub = ?", "sub-x").Count(&cnt)
	if cnt != 0 {
		t.Error("state 校验失败时不该建号")
	}
}

// 没有 flow cookie（直接访问回调地址）必须拒绝，不能凭一个 code 就换出会话。
func TestOIDC_RejectsMissingFlowCookie(t *testing.T) {
	f := newOIDCFixture(t, nil)
	req := httptest.NewRequest("GET", "/api/v1/auth/oidc/callback?state=s&code=c", nil)
	if err := f.o.Callback(httptest.NewRecorder(), req); err == nil {
		t.Fatal("没有 flow cookie 时应拒绝")
	}
}

// flow cookie 被改过（HMAC 不匹配）必须拒绝。
// 这个 cookie 里存着 verifier，能被伪造就等于 PKCE 失效。
func TestOIDC_RejectsTamperedFlowCookie(t *testing.T) {
	f := newOIDCFixture(t, nil)
	state, challenge, callback := f.beginAuth(t, "")
	code := f.idp.issueCode(challenge, issuedCode{sub: "sub-t", email: "t@example.com", verified: true})

	req := callback(code)
	c, err := req.Cookie(flowCookie)
	if err != nil {
		t.Fatal(err)
	}
	// 改掉签名部分
	parts := strings.Split(c.Value, ".")
	if len(parts) != 2 {
		t.Fatalf("flow cookie 结构不对：%q", c.Value)
	}
	req2 := httptest.NewRequest("GET",
		"/api/v1/auth/oidc/callback?state="+url.QueryEscape(state)+"&code="+url.QueryEscape(code), nil)
	req2.AddCookie(&http.Cookie{Name: flowCookie, Value: parts[0] + "." + b64u([]byte("forged-signature"))})

	if err := f.o.Callback(httptest.NewRecorder(), req2); err == nil {
		t.Fatal("HMAC 不匹配时应拒绝")
	}
}

// 已绑定的 sub 再次登录：直接复用那个账号，不再建新号。
func TestOIDC_ExistingSubReusesAccount(t *testing.T) {
	f := newOIDCFixture(t, nil)
	existing := &models.User{ID: "u_exists", Username: "old", Role: "member", Status: "active", Source: "oidc"}
	sub := "sub-existing"
	existing.OIDCSub = &sub
	f.m.DB.Create(existing)

	_, challenge, callback := f.beginAuth(t, "")
	code := f.idp.issueCode(challenge, issuedCode{sub: sub, email: "other@example.com", verified: true, preferred: "brandnew"})

	if err := f.o.Callback(httptest.NewRecorder(), callback(code)); err != nil {
		t.Fatalf("回调失败：%v", err)
	}
	var cnt int64
	f.m.DB.Model(&models.User{}).Count(&cnt)
	if cnt != 1 {
		t.Fatalf("应复用已有账号，实际有 %d 个用户", cnt)
	}
	var u models.User
	f.m.DB.First(&u, "id = ?", "u_exists")
	if u.Username != "old" {
		t.Errorf("用户名不该被 claims 改掉，实际 %q", u.Username)
	}
}

// verified email 匹配已有本地账号：把 sub 绑上去，而不是建一个重名账号。
// 这是「先用密码注册、后来接入 SSO」的迁移路径。
func TestOIDC_VerifiedEmailLinksLocalAccount(t *testing.T) {
	f := newOIDCFixture(t, nil)
	f.m.DB.Create(&models.User{ID: "u_local", Username: "zhangsan", Email: "zhangsan@example.com",
		Role: "admin", Status: "active", Source: "local"})

	_, challenge, callback := f.beginAuth(t, "")
	code := f.idp.issueCode(challenge, issuedCode{sub: "sub-link", email: "zhangsan@example.com", verified: true})

	if err := f.o.Callback(httptest.NewRecorder(), callback(code)); err != nil {
		t.Fatalf("回调失败：%v", err)
	}
	var u models.User
	f.m.DB.First(&u, "id = ?", "u_local")
	if u.OIDCSub == nil || *u.OIDCSub != "sub-link" {
		t.Errorf("应把 sub 绑到本地账号，实际 %v", u.OIDCSub)
	}
	if u.Role != "admin" {
		t.Errorf("绑定不该改角色，实际 %s", u.Role)
	}
	var cnt int64
	f.m.DB.Model(&models.User{}).Count(&cnt)
	if cnt != 1 {
		t.Errorf("不该建第二个账号，实际 %d 个", cnt)
	}
}

// email 未验证时不能按邮箱匹配：否则任何人只要在 IdP 上填一个别人的邮箱
// 就能接管那个本地账号。
func TestOIDC_UnverifiedEmailDoesNotLink(t *testing.T) {
	f := newOIDCFixture(t, nil)
	f.m.DB.Create(&models.User{ID: "u_victim", Username: "victim", Email: "victim@example.com",
		Role: "admin", Status: "active", Source: "local"})

	_, challenge, callback := f.beginAuth(t, "")
	code := f.idp.issueCode(challenge, issuedCode{sub: "sub-evil", email: "victim@example.com", verified: false, preferred: "evil"})

	if err := f.o.Callback(httptest.NewRecorder(), callback(code)); err != nil {
		t.Fatalf("回调失败：%v", err)
	}
	var victim models.User
	f.m.DB.First(&victim, "id = ?", "u_victim")
	if victim.OIDCSub != nil {
		t.Error("邮箱未验证时绝不能绑定到已有账号（等于账号接管）")
	}
}

// 自动开户策略：none 与 pending 都必须拒绝，且不建号。
func TestOIDC_AutoProvisionModes(t *testing.T) {
	cases := []struct{ mode, wantSub string }{
		{"none", "未开通"},
		{"pending", "待管理员审核"},
	}
	for _, c := range cases {
		mode := c.mode
		f := newOIDCFixture(t, func(cfg *config.Config) { cfg.OIDCAutoProvision = mode })
		_, challenge, callback := f.beginAuth(t, "")
		code := f.idp.issueCode(challenge, issuedCode{sub: "sub-" + mode, email: mode + "@example.com", verified: true})

		err := f.o.Callback(httptest.NewRecorder(), callback(code))
		if err == nil || !strings.Contains(err.Error(), c.wantSub) {
			t.Errorf("mode=%s 应报含「%s」的错误，实际 %v", mode, c.wantSub, err)
		}
		var cnt int64
		f.m.DB.Model(&models.User{}).Where("oidc_sub = ?", "sub-"+mode).Count(&cnt)
		if cnt != 0 {
			t.Errorf("mode=%s 不该建号", mode)
		}
	}
}

// 被禁用的账号即使 sub 已绑也要拦住。
func TestOIDC_DisabledUserRejected(t *testing.T) {
	f := newOIDCFixture(t, nil)
	sub := "sub-disabled"
	f.m.DB.Create(&models.User{ID: "u_dis", Username: "dis", Role: "member",
		Status: "disabled", Source: "oidc", OIDCSub: &sub})

	_, challenge, callback := f.beginAuth(t, "")
	code := f.idp.issueCode(challenge, issuedCode{sub: sub, email: "d@example.com", verified: true})

	err := f.o.Callback(httptest.NewRecorder(), callback(code))
	if err == nil || !strings.Contains(err.Error(), "禁用") {
		t.Fatalf("禁用账号应被拒绝，实际 err=%v", err)
	}
}

// 用户名冲突时自动加序号，不能因为重名就登录失败。
func TestUniqueUsername(t *testing.T) {
	f := newOIDCFixture(t, nil)
	f.m.DB.Create(&models.User{ID: "u_a", Username: "zhangsan"})
	f.m.DB.Create(&models.User{ID: "u_b", Username: "zhangsan1"})

	if got := uniqueUsername(f.m.DB, "zhangsan"); got != "zhangsan2" {
		t.Errorf("重名应递增到 zhangsan2，实际 %q", got)
	}
	if got := uniqueUsername(f.m.DB, "lisi"); got != "lisi" {
		t.Errorf("不冲突时应原样返回，实际 %q", got)
	}
}

// 名单里的用户在 OIDC 首次登录时提升为管理员（启动时那次还没有他）。
func TestOIDC_PromotesListedUsername(t *testing.T) {
	f := newOIDCFixture(t, func(cfg *config.Config) {
		cfg.AdminUsernames = []string{"boss"}
	})
	_, challenge, callback := f.beginAuth(t, "")
	code := f.idp.issueCode(challenge, issuedCode{sub: "sub-boss", email: "boss@example.com",
		verified: true, preferred: "boss"})

	if err := f.o.Callback(httptest.NewRecorder(), callback(code)); err != nil {
		t.Fatalf("回调失败：%v", err)
	}
	var u models.User
	f.m.DB.First(&u, "oidc_sub = ?", "sub-boss")
	if u.Role != "admin" {
		t.Errorf("名单内的账号应在登录时提升为 admin，实际 %s", u.Role)
	}
}

// ---- 绑定与解绑 ----

// bind 模式：把 sub 绑到当前登录用户，而不是签发新会话。
func TestOIDC_BindModeAttachesToCurrentUser(t *testing.T) {
	f := newOIDCFixture(t, nil)
	target := &models.User{ID: "u_bind", Username: "bind", Role: "member", Status: "active", Source: "local"}
	f.m.DB.Create(target)

	_, challenge, callback := f.beginAuth(t, "u_bind")
	code := f.idp.issueCode(challenge, issuedCode{sub: "sub-bind", email: "bind@example.com", verified: true, preferred: "whatever"})

	rec := httptest.NewRecorder()
	if err := f.o.Callback(rec, callback(code)); err != nil {
		t.Fatalf("绑定时回调失败：%v", err)
	}
	var u models.User
	f.m.DB.First(&u, "id = ?", "u_bind")
	if u.OIDCSub == nil || *u.OIDCSub != "sub-bind" {
		t.Errorf("sub 应绑到当前用户，实际 %v", u.OIDCSub)
	}
	// 绑定模式下不该签会话，也不该建新号
	if c := rec.Result().Cookies(); len(c) > 0 {
		for _, ck := range c {
			if ck.Name == CookieName && ck.Value != "" && ck.MaxAge >= 0 {
				t.Error("绑定模式不该签发新会话")
			}
		}
	}
	var cnt int64
	f.m.DB.Model(&models.User{}).Count(&cnt)
	if cnt != 1 {
		t.Errorf("绑定不该建新号，实际 %d 个用户", cnt)
	}
}

// 同一个 sub 已绑给别人时，先解绑别人再绑当前用户。
// 不这么做会出现「一个 sub 绑两个账号」，下次登录谁先匹配到谁就用谁的账号。
func TestOIDC_BindMovesSubFromOtherUser(t *testing.T) {
	f := newOIDCFixture(t, nil)
	sub := "sub-move"
	f.m.DB.Create(&models.User{ID: "u_other", Username: "other", Role: "member", Status: "active", OIDCSub: &sub})
	f.m.DB.Create(&models.User{ID: "u_me", Username: "me", Role: "member", Status: "active"})

	_, challenge, callback := f.beginAuth(t, "u_me")
	code := f.idp.issueCode(challenge, issuedCode{sub: sub, email: "me@example.com", verified: true})

	if err := f.o.Callback(httptest.NewRecorder(), callback(code)); err != nil {
		t.Fatalf("回调失败：%v", err)
	}
	var other, me models.User
	f.m.DB.First(&other, "id = ?", "u_other")
	f.m.DB.First(&me, "id = ?", "u_me")
	if other.OIDCSub != nil {
		t.Error("别人身上的同一 sub 应被解绑")
	}
	if me.OIDCSub == nil || *me.OIDCSub != sub {
		t.Errorf("当前用户应绑上该 sub，实际 %v", me.OIDCSub)
	}
}

// 绑定目标不存在时明确报错，不能静默成功。
func TestOIDC_BindMissingUserErrors(t *testing.T) {
	f := newOIDCFixture(t, nil)
	_, challenge, callback := f.beginAuth(t, "u_not_exist")
	code := f.idp.issueCode(challenge, issuedCode{sub: "sub-ghost", email: "g@example.com", verified: true})
	if err := f.o.Callback(httptest.NewRecorder(), callback(code)); err == nil {
		t.Fatal("绑定目标不存在时应报错")
	}
}

// 没有密码的账号不允许解绑 APSSO：解绑之后他就再也进不来了。
func TestOIDC_UnbindRequiresPassword(t *testing.T) {
	f := newOIDCFixture(t, nil)
	sub := "sub-unbind"
	noPwd := &models.User{ID: "u_nopwd", Username: "nopwd", Role: "member", Status: "active", OIDCSub: &sub}
	f.m.DB.Create(noPwd)

	if err := f.o.Unbind(noPwd); err == nil || !strings.Contains(err.Error(), "密码") {
		t.Fatalf("没有密码时应拒绝解绑，实际 err=%v", err)
	}

	hash, err := HashPassword("Zhuban@2026Adm1n")
	if err != nil {
		t.Fatal(err)
	}
	withPwd := &models.User{ID: "u_pwd", Username: "pwd", Role: "member", Status: "active",
		OIDCSub: &sub, PasswordHash: hash}
	f.m.DB.Create(withPwd)
	if err := f.o.Unbind(withPwd); err != nil {
		t.Fatalf("有密码时应允许解绑，实际 %v", err)
	}
	var got models.User
	f.m.DB.First(&got, "id = ?", "u_pwd")
	if got.OIDCSub != nil {
		t.Errorf("解绑后 oidc_sub 应为空，实际 %v", got.OIDCSub)
	}
}

// ---- 开关 ----

// 未启用或没配 client_id 时返回 nil，而不是一个会 panic 的半成品。
func TestNewOIDC_DisabledReturnsNil(t *testing.T) {
	cases := map[string]func(*config.Config){
		"未启用":    func(c *config.Config) { c.OIDCEnabled = false },
		"无 client": func(c *config.Config) { c.OIDCClientID = "" },
	}
	for name, mutate := range cases {
		dir := t.TempDir()
		cfg := &config.Config{
			DataDir: dir, DatabaseURL: "sqlite://" + filepath.Join(dir, "n.db"),
			AuthSecret: "s", OIDCEnabled: true, OIDCIssuer: "https://sso.example.com", OIDCClientID: "x",
		}
		mutate(cfg)
		db, err := store.Open(cfg)
		if err != nil {
			t.Fatal(err)
		}
		// Windows 上不关句柄，TempDir 清理会报「文件被占用」而把用例判失败
		t.Cleanup(func() {
			if sqlDB, e := db.DB(); e == nil {
				_ = sqlDB.Close()
			}
		})
		o, err := NewOIDC(New(db, cfg))
		if err != nil {
			t.Errorf("%s：不该返回错误，实际 %v", name, err)
		}
		if o != nil {
			t.Errorf("%s：应返回 nil", name)
		}
		if o.Enabled() {
			t.Errorf("%s：nil 的 Enabled() 必须是 false", name)
		}
		_ = db
	}
}

// 发现端点不可达时把错误抛出来，不要静默降级成「OIDC 不可用但也不报错」——
// 那会让「配了 SSO 却登录入口不见了」变成一次没有线索的排查。
func TestNewOIDC_DiscoveryFailureReturnsError(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		DataDir: dir, DatabaseURL: "sqlite://" + filepath.Join(dir, "e.db"),
		AuthSecret: "s", OIDCEnabled: true,
		OIDCIssuer: "http://127.0.0.1:1/no-such-idp", OIDCClientID: "x",
	}
	db, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	if _, err := NewOIDC(New(db, cfg)); err == nil {
		t.Fatal("发现失败时应返回错误")
	}
}
