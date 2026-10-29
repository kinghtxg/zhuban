package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"docforge/server/internal/auth"
	"docforge/server/internal/clientip"
	"docforge/server/internal/config"
	"docforge/server/internal/models"
	"docforge/server/internal/sign"
	"docforge/server/internal/storage"
	"docforge/server/internal/store"

	"github.com/go-chi/chi/v5"
)

// 这一组不测「某个功能对不对」，测的是**每条路由的准入边界**。
//
// 为什么值得这么做：100 条路由里，只要有一条忘了挂认证，或者某个 admin 接口
// 只在前端隐藏了入口，就是一个可被直接利用的越权。而这类缺陷在功能测试里
// 完全看不见——不登录也能调通，恰恰说明测试没覆盖到。
//
// 做法是遍历 chi 的真实路由表，而不是手写一份接口清单：
// 手写清单会漏掉「上个月新加的那条」，遍历不会。

// setupFullServer 装出带认证管理器的完整 Server（setupServer 缺 Auth，Mount 会空指针）。
func setupFullServer(t *testing.T, mutate func(*config.Config)) (*Server, *config.Config) {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{
		DataDir:      dir,
		RetentionDays: 7,
		DatabaseURL:  "sqlite://" + filepath.Join(dir, "full.db"),
		MaxImageMB:   5,
		AuthSecret:   "matrix-test-secret",
		// 用 http 基址：否则 cookie 带 Secure，测试里带不上（浏览器/客户端都不回传）
		ExternalBaseURL: "http://127.0.0.1:18100",
		SessionTTLDays:  7,
	}
	if mutate != nil {
		mutate(cfg)
	}
	db, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	ips, err := clientip.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	mgr := auth.New(db, cfg)
	mgr.IPs = ips
	s := &Server{
		DB: db, Cfg: cfg, Store: storage.New(dir), Renderer: &fakeRenderer{},
		Signer: sign.New("signed-secret", time.Hour),
		Auth:   mgr, IPs: ips,
	}
	return s, cfg
}

func buildRouter(s *Server) *chi.Mux {
	r := chi.NewRouter()
	s.Mount(r)
	return r
}

type routeEntry struct{ Method, Pattern string }

func walkRoutes(t *testing.T, r chi.Router) []routeEntry {
	t.Helper()
	var out []routeEntry
	if err := chi.Walk(r, func(method, pattern string, h http.Handler, mw ...func(http.Handler) http.Handler) error {
		out = append(out, routeEntry{Method: method, Pattern: pattern})
		return nil
	}); err != nil {
		t.Fatalf("遍历路由失败：%v", err)
	}
	return out
}

var pathParam = regexp.MustCompile(`\{[^}]+\}`)

// concretePath 把 /admin/users/{id} 变成可请求的路径。
func concretePath(pattern string) string {
	return pathParam.ReplaceAllString(pattern, "x")
}

// 允许匿名的路由。(method, pattern) 精确匹配。
//
// 每一项都要能说清「为什么可以不登录」——这份清单本质上就是系统的攻击面边界，
// 加一行等于对外开一个口子，所以宁可写得啰嗦一点。
var anonymousAllowed = map[string]string{
	"GET /api/v1/healthz":                "健康检查，容器探活用",
	"POST /api/v1/auth/login":            "登录入口本身",
	"POST /api/v1/auth/dev-login":        "开发模式登录（AUTH_DEV=false 时会拒）",
	"POST /api/v1/auth/register":         "自助注册（ALLOW_REGISTER=false 时会拒）",
	"GET /api/v1/auth/setup-status":      "初始化向导要能查到系统是否已初始化",
	"POST /api/v1/auth/setup":            "首次部署创建管理员，只在空库时有效",
	"GET /api/v1/auth/oidc/start":        "SSO 发起，还没有登录态",
	"GET /api/v1/auth/oidc/callback":     "SSO 回调，靠 state + flow cookie 校验",
	"GET /api/v1/s/{token}":              "签名直链，HMAC 签名本身就是凭证",
	"GET /api/v1/version":                "版本号不是秘密（二进制上 --version 谁都能打），登录页也要显示",
	"GET /api/v1/updates":                "更新提示要在登录页可见；只出域名与版本号，无业务数据，且有 6h 缓存",
	"GET /install":                       "安装向导本身就是给「还没有任何账号」的人看的；已初始化后恒 404",
	"GET /install.js":                    "安装向导的脚本；CSP 是 script-src 'self'，只能是外部文件，同样随初始化关闭",
	"GET /":                              "未登录给官网、已登录给工作台；两者都不含业务数据",
	"GET /welcome":                       "官网，市场页，公开可见",
	"GET /landing.js":                    "官网的脚本文件；CSP 是 script-src 'self'，只能是外部文件",
	"GET /*":                             "SPA history fallback 与静态资源",
}

// 未登录访问受保护路由必须 401，且**不能**是 200（放行）、403（说明到了 handler）、
// 500（说明认证中间件里崩了）。
func TestAuthMatrix_ProtectedRoutesRejectAnonymous(t *testing.T) {
	s, _ := setupFullServer(t, nil)
	r := buildRouter(s)
	routes := walkRoutes(t, r)
	if len(routes) < 80 {
		t.Fatalf("只遍历到 %d 条路由，路由表可能没挂全", len(routes))
	}

	var checked int
	for _, rt := range routes {
		key := rt.Method + " " + rt.Pattern
		if why, ok := anonymousAllowed[key]; ok {
			_ = why
			continue
		}
		// 静态资源与 SPA 前缀不算受保护接口
		if strings.HasPrefix(rt.Pattern, "/assets") {
			continue
		}
		p := concretePath(rt.Pattern)
		req := httptest.NewRequest(rt.Method, p, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		checked++

		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s 未登录时应返回 401，实际 %d（body=%s）",
				rt.Method, p, w.Code, truncateBody(w.Body.String()))
		}
	}
	if checked < 70 {
		t.Fatalf("实际只检查了 %d 条受保护路由，明显少于预期", checked)
	}
	t.Logf("已检查 %d 条受保护路由的匿名访问", checked)
}

// 管理员专属接口对普通成员必须是 403，而不是 200 或 404。
//
// 404 也算不合格：它说明这个成员连「有这个接口」都不该知道——但真实原因是
// handler 自己去查库查不到，而不是权限中间件拦住了。两者在运维上是两件事。
func TestAuthMatrix_AdminRoutesRejectMember(t *testing.T) {
	s, _ := setupFullServer(t, nil)
	r := buildRouter(s)

	// 建一个普通成员并拿会话
	member := &models.User{ID: "u_member", Username: "member", Role: "member", Status: "active", Source: "local"}
	if err := s.DB.Create(member).Error; err != nil {
		t.Fatal(err)
	}
	cookieVal := issueSessionCookie(t, s, member)

	adminRoutes := []string{
		"/api/v1/admin/users",
		"/api/v1/admin/settings",
		"/api/v1/admin/mail/settings",
		"/api/v1/admin/audit-logs",
		"/api/v1/admin/audit-logs/verify",
		"/api/v1/admin/audit-logs/export",
		"/api/v1/fonts",
		"/api/v1/review/queue",
	}
	for _, p := range adminRoutes {
		req := httptest.NewRequest("GET", p, nil)
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: cookieVal})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s 应由普通成员访问返回 403，实际 %d（body=%s）",
				p, w.Code, truncateBody(w.Body.String()))
		}
	}
}

// 管理员访问同一批接口不能是 403 —— 否则就是「把管理员一起关在门外」。
func TestAuthMatrix_AdminRoutesAllowAdmin(t *testing.T) {
	s, _ := setupFullServer(t, nil)
	r := buildRouter(s)

	admin := &models.User{ID: "u_admin", Username: "boss", Role: "admin", Status: "active", Source: "local"}
	if err := s.DB.Create(admin).Error; err != nil {
		t.Fatal(err)
	}
	cookieVal := issueSessionCookie(t, s, admin)

	for _, p := range []string{
		"/api/v1/admin/users",
		"/api/v1/admin/settings",
		"/api/v1/admin/mail/settings",
		"/api/v1/admin/audit-logs",
		"/api/v1/review/queue",
	} {
		req := httptest.NewRequest("GET", p, nil)
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: cookieVal})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code == http.StatusForbidden || w.Code == http.StatusUnauthorized {
			t.Errorf("%s 管理员不该被拒，实际 %d（body=%s）", p, w.Code, truncateBody(w.Body.String()))
		}
	}
}

// 公开端点不能因为「公开」就变成万能入口：dev-login 在非开发模式下必须拒绝，
// 不然它就是一枚现成的免认证后门（带个 JSON 就是管理员）。
func TestDevLogin_RefusedWhenAuthDevOff(t *testing.T) {
	s, _ := setupFullServer(t, func(c *config.Config) { c.AuthDev = false })
	r := buildRouter(s)

	req := httptest.NewRequest("POST", "/api/v1/auth/dev-login",
		strings.NewReader(`{"username":"attacker"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code == http.StatusOK {
		t.Fatalf("AUTH_DEV=false 时 dev-login 绝不能成功，实际 200（body=%s）", truncateBody(w.Body.String()))
	}
	var cnt int64
	s.DB.Model(&models.User{}).Count(&cnt)
	if cnt != 0 {
		t.Errorf("拒绝的 dev-login 不该建出账号，实际 %d 个", cnt)
	}
}

// setup 只在空库时有效。否则任何人都能在系统跑起来之后「初始化」出一个新管理员。
func TestSetup_RefusedWhenAlreadyInitialized(t *testing.T) {
	s, _ := setupFullServer(t, nil)
	r := buildRouter(s)
	s.DB.Create(&models.User{ID: "u_first", Username: "first", Role: "admin", Status: "active"})

	req := httptest.NewRequest("POST", "/api/v1/auth/setup",
		strings.NewReader(`{"username":"second","password":"unitTestPassw0rd"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code == http.StatusOK {
		t.Fatalf("已有用户时 setup 必须拒绝，实际 200（body=%s）", truncateBody(w.Body.String()))
	}
	var cnt int64
	s.DB.Model(&models.User{}).Where("username = ?", "second").Count(&cnt)
	if cnt != 0 {
		t.Error("被拒的 setup 不该建出第二个管理员")
	}
}

// 签名直链：坏 token 必须 401，不能因为「免登录」就放行任何 token。
func TestSignedLink_RejectsBadToken(t *testing.T) {
	s, _ := setupFullServer(t, nil)
	r := buildRouter(s)

	for _, tok := range []string{
		"bm90LWEtdG9rZW4",                    // 随便一段 base64
		"ZG9jX3h8MTc5MDAwMDAwMHxzaWc",        // 结构像但签名不对
	} {
		req := httptest.NewRequest("GET", "/api/v1/s/"+tok, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("坏 token %q 应返回 401，实际 %d", tok, w.Code)
		}
	}
}

// ---- 工具 ----

func issueSessionCookie(t *testing.T, s *Server, u *models.User) string {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := s.Auth.IssueCookie(rec, u); err != nil {
		t.Fatalf("签发会话失败：%v", err)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.CookieName {
			return c.Value
		}
	}
	t.Fatal("没有拿到会话 cookie")
	return ""
}

func truncateBody(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}

var _ = context.Background
