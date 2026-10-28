package api

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"docforge/server/internal/clientip"

	"github.com/go-chi/chi/v5"
)

// 这一组用例守的是「安全头默认成立」：以前它们只存在于部署时的 nginx 配置里，
// 本地直连、内网直连、别人 fork 后自己搭的 compose 全都没有。

func newHeaderServer(t *testing.T, trusted ...string) *Server {
	t.Helper()
	rv, err := clientip.New(trusted)
	if err != nil {
		t.Fatalf("clientip.New: %v", err)
	}
	return &Server{IPs: rv}
}

// serve 走一遍真实中间件链，返回响应头。
func serve(s *Server, path string, mutate func(*http.Request)) *http.Response {
	r := chi.NewRouter()
	r.Use(s.securityHeaders)
	r.Get("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	r.Get("/boom", func(w http.ResponseWriter, _ *http.Request) {
		writeErr(w, http.StatusInternalServerError, "internal_error", "x", nil)
	})

	req := httptest.NewRequest("GET", path, nil)
	if mutate != nil {
		mutate(req)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec.Result()
}

func TestSecurityHeaders_PresentOnSuccessAndError(t *testing.T) {
	s := newHeaderServer(t, "127.0.0.0/8")
	for _, path := range []string{"/", "/boom", "/not-registered"} {
		resp := serve(s, path, nil)
		for _, k := range []string{
			"X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy",
			"Permissions-Policy", "Content-Security-Policy",
		} {
			if resp.Header.Get(k) == "" {
				t.Errorf("%s 缺少响应头 %s（404/500 也要带，否则错误响应就成了唯一没有防护的那类）", path, k)
			}
		}
	}
}

func TestSecurityHeaders_CSPKeepsScriptStrict(t *testing.T) {
	resp := serve(newHeaderServer(t, "127.0.0.0/8"), "/", nil)
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self';") {
		t.Errorf("script-src 必须是 'self'，实际 CSP=%s", csp)
	}
	// script-src 里出现 unsafe-inline / unsafe-eval 等于把 XSS 防护让出去。
	// style-src 允许 unsafe-inline 是 Monaco 的行内样式需要，两者要分开看。
	if strings.Contains(csp, "script-src 'self' 'unsafe-inline'") ||
		strings.Contains(csp, "unsafe-eval") {
		t.Errorf("CSP 的 script-src 不该放开 inline/eval：%s", csp)
	}
	if !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("缺少 frame-ancestors 'none'：%s", csp)
	}
	if !strings.Contains(csp, "object-src 'none'") {
		t.Errorf("缺少 object-src 'none'：%s", csp)
	}
}

func TestSecurityHeaders_HSTSOnlyOverHTTPS(t *testing.T) {
	s := newHeaderServer(t, "127.0.0.0/8")

	// 明文 + 不可信对端：不发 HSTS。
	plain := serve(s, "/", func(r *http.Request) { r.RemoteAddr = "203.0.113.9:1234" })
	if got := plain.Header.Get("Strict-Transport-Security"); got != "" {
		t.Errorf("明文请求不该发 HSTS，实际 %q", got)
	}

	// 明文 + 公网对端自填 X-Forwarded-Proto=https：不能采信。
	spoofed := serve(s, "/", func(r *http.Request) {
		r.RemoteAddr = "203.0.113.9:1234"
		r.Header.Set("X-Forwarded-Proto", "https")
	})
	if got := spoofed.Header.Get("Strict-Transport-Security"); got != "" {
		t.Errorf("不可信对端带来的 X-Forwarded-Proto 不该被采信，实际 %q", got)
	}

	// 可信代理报 https：发。
	proxied := serve(s, "/", func(r *http.Request) {
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("X-Forwarded-Proto", "https")
	})
	if proxied.Header.Get("Strict-Transport-Security") == "" {
		t.Error("可信代理声明 https 时应发 HSTS")
	}

	// 直接 TLS：发。
	tlsReq := serve(s, "/", func(r *http.Request) {
		r.RemoteAddr = "203.0.113.9:1234"
		r.TLS = &tls.ConnectionState{}
	})
	if tlsReq.Header.Get("Strict-Transport-Security") == "" {
		t.Error("TLS 直连时应发 HSTS")
	}
}

func TestClientIPForRateLimit_NotSpoofable(t *testing.T) {
	// 从「限流判据」的视角再验一次：公网直连伪造 XFF，两个请求必须被算成同一个 IP，
	// 否则攻击者只要每次换一个假 IP 就能无限重试登录。
	s := newHeaderServer(t, "127.0.0.0/8")
	seen := map[string]bool{}
	for _, fake := range []string{"1.1.1.1", "2.2.2.2", "3.3.3.3"} {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "203.0.113.9:1234"
		req.Header.Set("X-Forwarded-For", fake)
		seen[s.clientIP(req)] = true
	}
	if len(seen) != 1 {
		t.Fatalf("伪造 XFF 应解析出同一个 IP，实际得到 %v", seen)
	}
}

func TestClientIPForRateLimit_BehindProxy(t *testing.T) {
	s := newHeaderServer(t, "127.0.0.0/8")
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	if got := s.clientIP(req); got != "203.0.113.9" {
		t.Fatalf("可信代理场景应取真实客户端 IP，实际 %q", got)
	}
}
