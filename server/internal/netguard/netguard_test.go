package netguard

import (
	"context"
	"net"
	"testing"
)

func TestBlockedIPs(t *testing.T) {
	g := New(false, nil)
	blocked := []string{
		"http://127.0.0.1/x",
		"http://10.1.2.3/x",
		"http://172.16.5.5/x",
		"http://192.168.1.5/x",
		"http://169.254.169.254/latest/meta-data",
		"http://[::1]/x",
		"http://[fc00::1]/x",
	}
	for _, u := range blocked {
		if err := g.Check(u); err == nil {
			t.Errorf("expected blocked: %s", u)
		}
	}
}

func TestAllowedPublic(t *testing.T) {
	g := New(false, nil)
	if err := g.Check("http://8.8.8.8/x"); err != nil {
		t.Errorf("public ip should be allowed: %v", err)
	}
}

func TestSchemeWhitelist(t *testing.T) {
	g := New(false, nil)
	if err := g.Check("ftp://8.8.8.8/x"); err == nil {
		t.Error("ftp should be blocked")
	}
}

func TestAllowPrivateWithAllowlist(t *testing.T) {
	g := New(true, []string{"10.0.0.5"})
	// 精确命中白名单的内网 IP 应放行
	if err := g.Check("http://10.0.0.5/x"); err != nil {
		t.Errorf("allowlisted private should pass: %v", err)
	}
	// 未命中白名单的内网 IP 仍拒绝
	if err := g.Check("http://10.9.9.9/x"); err == nil {
		t.Error("non-allowlisted private should be blocked")
	}
}

// ---- DNS rebinding 防护 ----

// TestResolvePinsIPs 守的是「校验过的地址就是拨号用的地址」。
// 如果 Resolve 只返回 nil 而不管解析结果，调用方就只能再解析一次，窗口就回来了。
func TestResolvePinsIPs(t *testing.T) {
	g := New(false, nil)
	tg, err := g.Resolve("http://example.com:8080/cb")
	if err != nil {
		t.Skipf("需要外网 DNS 才能验证真实解析：%v", err)
	}
	if tg.Host != "example.com" || tg.Port != "8080" {
		t.Fatalf("host/port 解析错误：%+v", tg)
	}
	if len(tg.IPs) == 0 {
		t.Fatal("Resolve 必须把解析结果带出来，否则拨号阶段还要再解析一次")
	}
}

func TestResolveDefaultPorts(t *testing.T) {
	g := New(false, nil)
	for _, c := range []struct{ url, port string }{
		{"https://example.com/cb", "443"},
		{"http://example.com/cb", "80"},
	} {
		tg, err := g.Resolve(c.url)
		if err != nil {
			continue // 无外网 DNS 时跳过
		}
		if tg.Port != c.port {
			t.Errorf("%s: port=%q want %q", c.url, tg.Port, c.port)
		}
	}
}

func TestResolveBlocksMetadataIP(t *testing.T) {
	g := New(false, nil)
	if _, err := g.Resolve("http://169.254.169.254/latest/meta-data/"); err == nil {
		t.Fatal("云元数据地址必须被拒")
	}
}

// TestDialContextIgnoresAddrArgument 是关键的一条：
// Transport 传进来的 addr 是 URL 里的 hostname，如果 DialContext 照它去拨，
// 就等于又交给 DNS 解析了一次。这里用一个随机监听端口 + 一个不存在的
// hostname，能连上就证明拨号完全由 Target.IPs 决定。
func TestDialContextIgnoresAddrArgument(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	tg := &Target{Host: "rebind.example.invalid", Port: port, IPs: []net.IP{net.ParseIP("127.0.0.1")}}
	c, err := tg.DialContext(context.Background(), "tcp", "rebind.example.invalid:80")
	if err != nil {
		t.Fatalf("应当连到固定的 127.0.0.1:%s，实际：%v", port, err)
	}
	_ = c.Close()
}
