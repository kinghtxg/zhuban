package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"docforge/server/internal/config"
	"docforge/server/internal/models"
	"docforge/server/internal/store"
)

// 这一组用例守的是「登出/改密/停用之后，手里的 cookie 立刻不能用了」。
// 在引入 sessions 表之前，cookie 是自包含的 HMAC 令牌：签出去就等于
// 把 7 天的通行权交出去了，服务端没有任何收回的手段。

func newSessionFixture(t *testing.T) (*Manager, *models.User) {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{
		DataDir:     dir,
		DatabaseURL: "sqlite://" + filepath.Join(dir, "t.db"),
		AuthSecret:  "session-test-secret",
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
	u := &models.User{ID: "u_sess", Username: "sess", Role: "member", Status: "active", Source: "local"}
	if err := db.Create(u).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	return New(db, cfg), u
}

// issue 签发一枚 cookie 并返回它的值。
func issue(t *testing.T, m *Manager, u *models.User) string {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := m.IssueCookie(rec, u); err != nil {
		t.Fatalf("IssueCookie: %v", err)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == CookieName {
			return c.Value
		}
	}
	t.Fatal("没有拿到会话 cookie")
	return ""
}

// authed 用给定 cookie 走一遍认证中间件，返回是否通过。
func authed(m *Manager, token string) bool {
	r := httptest.NewRequest("GET", "/api/v1/auth/me", nil)
	if token != "" {
		r.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	}
	var passed bool
	h := m.Authenticate(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { passed = true }))
	h.ServeHTTP(httptest.NewRecorder(), r)
	return passed
}

func TestSession_ValidTokenAuthenticates(t *testing.T) {
	m, u := newSessionFixture(t)
	if !authed(m, issue(t, m, u)) {
		t.Fatal("刚签发的会话应当能通过认证")
	}
}

func TestSession_LogoutRevokesImmediately(t *testing.T) {
	m, u := newSessionFixture(t)
	token := issue(t, m, u)
	if !authed(m, token) {
		t.Fatal("前置条件不成立：新令牌应有效")
	}
	// 模拟登出：带上这枚 cookie 请求 /auth/logout。
	r := httptest.NewRequest("POST", "/api/v1/auth/logout", nil)
	r.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	m.RevokeSession(r)

	// 关键断言：拿同一个 token 再来一次必须失败。
	// 只清浏览器 cookie 的实现会在这里通过——那正是漏洞本身。
	if authed(m, token) {
		t.Fatal("登出后同一枚 cookie 仍然有效：登出只清了浏览器，没有收回凭证")
	}
}

func TestSession_ResetPasswordRevokesAll(t *testing.T) {
	m, u := newSessionFixture(t)
	a := issue(t, m, u)
	b := issue(t, m, u)
	m.RevokeAllSessions(u.ID)
	if authed(m, a) || authed(m, b) {
		t.Fatal("重置密码后所有会话都必须失效")
	}
}

func TestSession_PasswordChangeKeepsCurrent(t *testing.T) {
	m, u := newSessionFixture(t)
	other := issue(t, m, u)
	current := issue(t, m, u)

	r := httptest.NewRequest("POST", "/api/v1/auth/password", nil)
	r.AddCookie(&http.Cookie{Name: CookieName, Value: current})
	m.RevokeOtherSessions(r, u.ID)

	if !authed(m, current) {
		t.Fatal("改密的那个会话不该被自己踢掉（用户会以为改密失败）")
	}
	if authed(m, other) {
		t.Fatal("其它设备上的会话必须失效")
	}
}

func TestSession_ExpiredRowRejected(t *testing.T) {
	m, u := newSessionFixture(t)
	token := issue(t, m, u)
	// 把库里这行改成已过期：签名仍然有效，但会话已不在有效期内。
	m.DB.Model(&models.Session{}).Where("user_id = ?", u.ID).
		Update("expires_at", time.Now().Add(-time.Minute))
	if authed(m, token) {
		t.Fatal("过期的会话不应该通过认证")
	}
}

func TestSession_DisabledUserRejected(t *testing.T) {
	m, u := newSessionFixture(t)
	token := issue(t, m, u)
	m.DB.Model(u).Update("status", "disabled")
	if authed(m, token) {
		t.Fatal("停用用户不应通过认证")
	}
}

// TestSession_LegacyTokenWithoutSIDRejected 守的是升级路径：
// 旧格式 user_id|exp 签名同样合法，但它无法被撤销，必须一律拒绝。
func TestSession_LegacyTokenWithoutSIDRejected(t *testing.T) {
	m, u := newSessionFixture(t)
	exp := time.Now().Add(time.Hour).Unix()
	payload := u.ID + "|" + strconv.FormatInt(exp, 10)
	mac := hmac.New(sha256.New, m.secret())
	_, _ = mac.Write([]byte(payload))
	token := base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	if authed(m, token) {
		t.Fatal("没有 sid 的老令牌无法撤销，必须拒绝")
	}
	if _, _, ok := m.verifyCookie(token); ok {
		t.Fatal("verifyCookie 不该接受两段载荷")
	}
}

func TestSession_TokenHashNeverStoredInPlain(t *testing.T) {
	m, u := newSessionFixture(t)
	token := issue(t, m, u)
	var rows []models.Session
	m.DB.Where("user_id = ?", u.ID).Find(&rows)
	if len(rows) == 0 {
		t.Fatal("会话必须落库，否则无从撤销")
	}
	for _, row := range rows {
		if row.TokenHash == token {
			t.Fatal("库里不该出现可直接当 cookie 使用的明文令牌")
		}
		if len(row.TokenHash) != 64 {
			t.Fatalf("TokenHash 应为 sha256 hex（64 位），实际 %d 位", len(row.TokenHash))
		}
	}
}
