package netguard

import (
	"context"
	"net"
	"testing"
)

// 这一组补的是 SSRF 防护的**边界枚举**。
//
// 为什么值得一行一行测：这里漏掉一个地址段，就等于给「服务端替攻击者发请求」
// 留了一扇门，而门后面是云元数据（169.254.169.254）和内网管理口。
// 这类缺陷在功能测试里完全看不出来——回调照常发出去，只是发到了不该发的地方。

// 逐个地址段枚举。isDisallowedIP 是唯一的判据，绕过它就绕过了一切。
func TestIsDisallowedIP_CoversAllSegments(t *testing.T) {
	cases := []struct {
		ip      string
		blocked bool
		why     string
	}{
		// 回环
		{"127.0.0.1", true, "IPv4 回环"},
		{"127.255.255.254", true, "回环段末端"},
		{"::1", true, "IPv6 回环"},
		// 私网
		{"10.0.0.1", true, "10/8"},
		{"172.16.0.1", true, "172.16/12 下界"},
		{"172.31.255.254", true, "172.16/12 上界"},
		{"192.168.1.1", true, "192.168/16"},
		{"fc00::1", true, "IPv6 ULA"},
		{"fd12:3456::1", true, "IPv6 ULA（fd 段）"},
		// 链路本地：云元数据就在这一段
		{"169.254.169.254", true, "AWS/GCP/Azure 元数据"},
		{"fe80::1", true, "IPv6 链路本地"},
		// 未指定 / 组播
		{"0.0.0.0", true, "未指定地址"},
		{"::", true, "IPv6 未指定"},
		{"224.0.0.1", true, "IPv4 组播"},
		{"ff02::1", true, "IPv6 组播"},
		// IPv4-mapped IPv6：不先转 v4 就会漏过去
		{"::ffff:169.254.169.254", true, "映射的元数据地址"},
		{"::ffff:127.0.0.1", true, "映射的回环"},
		{"::ffff:10.0.0.1", true, "映射的私网"},
		// 公网
		{"8.8.8.8", false, "公网 DNS"},
		{"1.1.1.1", false, "公网 DNS"},
		{"2606:4700:4700::1111", false, "公网 IPv6"},
		{"172.32.0.1", false, "172.16/12 之外（容易误伤的一段）"},
		{"192.169.0.1", false, "192.168/16 之外"},
	}
	for _, c := range cases {
		ip := net.ParseIP(c.ip)
		if ip == nil {
			t.Fatalf("测试数据有误，解析不出 IP：%s", c.ip)
		}
		if got := isDisallowedIP(ip); got != c.blocked {
			t.Errorf("%s（%s）：期望 blocked=%v，实际 %v", c.ip, c.why, c.blocked, got)
		}
	}
	// nil 必须按禁止处理：解析失败时不能因为「没地址」就被当成安全
	if !isDisallowedIP(nil) {
		t.Error("nil IP 必须判为禁止（fail-closed）")
	}
}

func TestHostAllowlistMatching(t *testing.T) {
	c := New(true, []string{"internal.example.com", ".corp.example.com", "10.0.0.5", ""})
	cases := []struct {
		host string
		want bool
	}{
		{"internal.example.com", true, },
		{"INTERNAL.EXAMPLE.COM", true, },
		{"internal.example.com.", true, },
		{"a.corp.example.com", true, },  // 后缀白名单命中子域
		{"corp.example.com", false, },   // ".corp.example.com" 不含根域本身
		{"evilcorp.example.com", false, }, // 不能因为后缀字符串包含就放行
		{"10.0.0.5", true, },
		{"10.0.0.6", false, },
		{"other.example.com", false, },
	}
	for _, tc := range cases {
		if got := c.hostAllowed(tc.host); got != tc.want {
			t.Errorf("hostAllowed(%q) = %v，期望 %v", tc.host, got, tc.want)
		}
	}

	empty := New(true, nil)
	if empty.hostAllowed("anything.com") {
		t.Error("空白名单不该放行任何 host")
	}
}

// scheme 白名单：file 能读本地文件，gopher 能构造任意 TCP 载荷，
// data 会让下载器吃到内联数据。都必须挡在门外。
func TestSchemeWhitelist_BlocksDangerousSchemes(t *testing.T) {
	g := New(false, nil)
	for _, u := range []string{
		"file:///etc/passwd",
		"gopher://127.0.0.1:6379/_INFO",
		"ftp://example.com/x",
		"data:text/html,<script>alert(1)</script>",
		"javascript:alert(1)",
		"//example.com/x", // 没有 scheme
	} {
		if err := g.Check(u); err == nil {
			t.Errorf("应拒绝：%s", u)
		}
	}
}

func TestResolve_RejectsMalformedURLs(t *testing.T) {
	g := New(false, nil)
	for _, u := range []string{
		"http://",
		"http:///path",
		"://nohost",
		"http://[::1", // 括号不闭合
	} {
		if _, err := g.Resolve(u); err == nil {
			t.Errorf("畸形 URL 应报错：%q", u)
		}
	}
}

// IPv6 字面量 + 端口：解析时不能把 [::1]:8080 拆错。
func TestResolve_IPv6LiteralWithPort(t *testing.T) {
	g := New(true, []string{"::1"})
	tg, err := g.Resolve("http://[::1]:8080/cb")
	if err != nil {
		t.Fatalf("白名单内的 IPv6 回环应放行：%v", err)
	}
	if tg.Host != "::1" || tg.Port != "8080" {
		t.Errorf("IPv6 解析错误：host=%q port=%q", tg.Host, tg.Port)
	}
	if len(tg.IPs) != 1 || !tg.IPs[0].Equal(net.ParseIP("::1")) {
		t.Errorf("应把字面量 IP 固定进 Target，实际 %v", tg.IPs)
	}

	// 不在白名单里的 IPv6 回环仍要拒
	if _, err := New(true, []string{"::2"}).Resolve("http://[::1]:8080/cb"); err == nil {
		t.Error("白名单未命中时 IPv6 回环应被拒")
	}
}

// AllowPrivate=true 但没配白名单：等于「放开内网但没说放给谁」，
// 这时必须拒绝，否则这个开关就是个一键关闭 SSRF 防护的按钮。
func TestAllowPrivateWithoutAllowlistStillBlocks(t *testing.T) {
	g := New(true, nil)
	if err := g.Check("http://10.0.0.5/x"); err == nil {
		t.Fatal("AllowPrivate=true 但没有白名单时必须拒绝内网地址")
	}
	if err := g.Check("http://127.0.0.1/x"); err == nil {
		t.Fatal("回环同样需要白名单")
	}
}

// CheckHost 是给「只有 host 没有 URL」的场景用的（比如 SMTP 服务器地址）。
func TestCheckHost(t *testing.T) {
	g := New(false, nil)
	if err := g.CheckHost("127.0.0.1"); err == nil {
		t.Error("回环 host 应被拒")
	}
	if err := g.CheckHost("169.254.169.254"); err == nil {
		t.Error("元数据地址应被拒")
	}
	if err := g.CheckHost("8.8.8.8"); err != nil {
		t.Errorf("公网 host 应放行：%v", err)
	}
	// 域名要真的解析再判断，不能看到不是字面量就放行
	if err := g.CheckHost(""); err == nil {
		t.Error("空 host 应被拒")
	}
}

// 所有候选地址都连不上时要返回错误，不能返回 (nil, nil) ——
// 那会让上层拿着一个 nil conn 去读写，panic 在很难定位的地方。
func TestDialContext_AllFailReturnsError(t *testing.T) {
	tg := &Target{Host: "x", Port: "1", IPs: []net.IP{
		net.ParseIP("127.0.0.1"), net.ParseIP("127.0.0.2"),
	}}
	conn, err := tg.DialContext(context.Background(), "tcp", "x:1")
	if err == nil {
		_ = conn.Close()
		t.Fatal("全部连不上时应返回错误")
	}
}

// 没有候选地址（理论上不该出现，但 struct 是公开的）也要报错而不是空指针。
func TestDialContext_NoIPs(t *testing.T) {
	tg := &Target{Host: "x", Port: "80"}
	if conn, err := tg.DialContext(context.Background(), "tcp", "x:80"); err == nil {
		_ = conn.Close()
		t.Fatal("没有候选地址时应返回错误")
	}
}

// 多个候选时按顺序尝试：第一个连不上就换下一个。
// 这条对应真实场景——域名解析出多个 A 记录，其中一个挂了。
func TestDialContext_TriesNextIP(t *testing.T) {
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

	tg := &Target{Host: "multi.example.invalid", Port: port, IPs: []net.IP{
		net.ParseIP("127.0.0.2"), // 大概率连不上
		net.ParseIP("127.0.0.1"), // 这个能连
	}}
	conn, err := tg.DialContext(context.Background(), "tcp", "multi.example.invalid:"+port)
	if err != nil {
		t.Fatalf("应能退到第二个候选地址：%v", err)
	}
	_ = conn.Close()
}

// 端口必须来自 URL，而不是调用方传的 addr。否则重定向到别的端口时会串。
func TestTargetKeepsPortFromURL(t *testing.T) {
	g := New(true, []string{"10.0.0.5"})
	tg, err := g.Resolve("https://10.0.0.5:8443/cb")
	if err != nil {
		t.Fatalf("白名单内应放行：%v", err)
	}
	if tg.Port != "8443" {
		t.Errorf("端口应取 URL 里的 8443，实际 %q", tg.Port)
	}
}
