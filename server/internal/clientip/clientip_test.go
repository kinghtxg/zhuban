package clientip

import (
	"net/http/httptest"
	"testing"
)

// 这一组用例守的是「限流与审计 IP 不能被伪造」。
// 每条都对应一种真实打法的边界，改动这里的算法前请先看它们为什么这么写。

func mustResolver(t *testing.T, specs ...string) *Resolver {
	t.Helper()
	rv, err := New(specs)
	if err != nil {
		t.Fatalf("New(%v): %v", specs, err)
	}
	return rv
}

func TestClientIP_UntrustedPeerIgnoresXFF(t *testing.T) {
	// 公网直连 + 客户端自填 XFF：必须回对端地址，不能回 1.2.3.4。
	rv := mustResolver(t, "127.0.0.0/8")
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "203.0.113.9:5555"
	r.Header.Set("X-Forwarded-For", "1.2.3.4")
	if got := rv.ClientIP(r); got != "203.0.113.9" {
		t.Fatalf("got %q want peer addr 203.0.113.9", got)
	}
}

func TestClientIP_TrustedPeerTakesRightmostUntrusted(t *testing.T) {
	// 经典链路：客户端 → nginx(127.0.0.1) → 我们。
	rv := mustResolver(t, "127.0.0.0/8")
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:40000"
	r.Header.Set("X-Forwarded-For", "203.0.113.9")
	if got := rv.ClientIP(r); got != "203.0.113.9" {
		t.Fatalf("got %q want 203.0.113.9", got)
	}
}

func TestClientIP_SpoofedPrefixIsIgnored(t *testing.T) {
	// 攻击者在 XFF 里先塞一个假 IP，nginx 又把他的真实地址追加在后面：
	// "6.6.6.6, 203.0.113.9"（对端 127.0.0.1）。从左往右取第一段就会拿到假值，
	// 从右往左取第一个非信任项才是真的。
	rv := mustResolver(t, "127.0.0.0/8", "10.0.0.0/8")
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:40000"
	r.Header.Set("X-Forwarded-For", "6.6.6.6, 203.0.113.9, 10.1.2.3")
	if got := rv.ClientIP(r); got != "203.0.113.9" {
		t.Fatalf("got %q want 203.0.113.9（6.6.6.6 是客户端伪造的前缀）", got)
	}
}

func TestClientIP_NoXFFFromTrustedPeer(t *testing.T) {
	rv := mustResolver(t, "127.0.0.0/8")
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:40000"
	if got := rv.ClientIP(r); got != "127.0.0.1" {
		t.Fatalf("got %q want 127.0.0.1", got)
	}
}

func TestClientIP_GarbageXFFIsDropped(t *testing.T) {
	// 解析不出来的项不能进审计：那个字段会原样出现在页面上。
	rv := mustResolver(t, "127.0.0.0/8")
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:40000"
	r.Header.Set("X-Forwarded-For", "<script>, 203.0.113.9")
	if got := rv.ClientIP(r); got != "203.0.113.9" {
		t.Fatalf("got %q want 203.0.113.9", got)
	}
}

func TestClientIP_WholeChainTrustedFallsBackToFirst(t *testing.T) {
	rv := mustResolver(t, "127.0.0.0/8", "10.0.0.0/8")
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:40000"
	r.Header.Set("X-Forwarded-For", "10.0.0.5, 10.0.0.6")
	if got := rv.ClientIP(r); got != "10.0.0.5" {
		t.Fatalf("got %q want 10.0.0.5", got)
	}
}

func TestClientIP_NilResolverAndNilRequest(t *testing.T) {
	var rv *Resolver
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "203.0.113.9:5555"
	r.Header.Set("X-Forwarded-For", "1.2.3.4")
	if got := rv.ClientIP(r); got != "203.0.113.9" {
		t.Fatalf("nil resolver: got %q want 203.0.113.9", got)
	}
	if got := rv.ClientIP(nil); got != "" {
		t.Fatalf("nil request: got %q want empty", got)
	}
}

func TestClientIP_IPv6(t *testing.T) {
	rv := mustResolver(t, "::1/128")
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "[::1]:40000"
	r.Header.Set("X-Forwarded-For", "2001:db8::1")
	if got := rv.ClientIP(r); got != "2001:db8::1" {
		t.Fatalf("got %q want 2001:db8::1", got)
	}
}

func TestNew_RejectsGarbage(t *testing.T) {
	if _, err := New([]string{"not-an-ip"}); err == nil {
		t.Fatal("期望报错，实际通过：写错的可信代理名单必须让启动失败，不能静默忽略")
	}
	if _, err := New([]string{"10.0.0.0/33"}); err == nil {
		t.Fatal("期望报错，实际通过")
	}
}

func TestNew_AcceptsBareIP(t *testing.T) {
	rv := mustResolver(t, "10.1.2.3")
	if !rv.isTrusted("10.1.2.3") {
		t.Fatal("裸 IP 应当被当成 /32")
	}
	if rv.isTrusted("10.1.2.4") {
		t.Fatal("裸 IP 不该扩成整段")
	}
}

func TestTrustedProxy(t *testing.T) {
	rv := mustResolver(t, "172.16.0.0/12")
	trusted := httptest.NewRequest("GET", "/", nil)
	trusted.RemoteAddr = "172.17.0.1:1234"
	if !rv.TrustedProxy(trusted) {
		t.Fatal("docker 网关应当在信任名单内")
	}
	untrusted := httptest.NewRequest("GET", "/", nil)
	untrusted.RemoteAddr = "203.0.113.9:1234"
	if rv.TrustedProxy(untrusted) {
		t.Fatal("公网地址不该被当成代理")
	}
}

// TestClientIP_FallsBackToXRealIP 守的是一个线上真实形态：
// nginx → WAF → docker-proxy → 容器。中间几跳把 X-Forwarded-For 覆盖成了
// 自己的内网地址，真实客户端只剩在 X-Real-IP 里。不兜这一手的话，
// 审计里所有人都是 172.19.0.1，登录限流也退化成全体共用一个额度。
func TestClientIP_FallsBackToXRealIP(t *testing.T) {
	rv := mustResolver(t, "172.16.0.0/12")
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "172.19.0.1:5555"
	r.Header.Set("X-Forwarded-For", "172.19.0.1, 172.19.0.1")
	r.Header.Set("X-Real-IP", "203.0.113.9")
	if got := rv.ClientIP(r); got != "203.0.113.9" {
		t.Fatalf("got %q want 203.0.113.9（X-Real-IP 兜底）", got)
	}
}

// 但对端不可信时，X-Real-IP 一样是客户端自己写的，必须无视。
func TestClientIP_XRealIPIgnoredFromUntrustedPeer(t *testing.T) {
	rv := mustResolver(t, "172.16.0.0/12")
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "203.0.113.9:5555"
	r.Header.Set("X-Real-IP", "1.2.3.4")
	if got := rv.ClientIP(r); got != "203.0.113.9" {
		t.Fatalf("got %q want 203.0.113.9", got)
	}
}
