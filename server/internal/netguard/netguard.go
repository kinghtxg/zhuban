// Package netguard 实现回调/图片下载的 SSRF 防护（契约 §5、PRD §17）。
// scheme 白名单 http/https；DNS 解析后拒绝回环/内网/链路本地/IPv4-mapped；
// CALLBACK_ALLOW_PRIVATE=true 时必须命中 host 白名单。
package netguard

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// Checker 校验目标地址是否可由服务端主动发起请求。
type Checker struct {
	AllowPrivate bool
	HostAllow    []string
}

// New 构造 Checker。
func New(allowPrivate bool, allowlist []string) *Checker {
	return &Checker{AllowPrivate: allowPrivate, HostAllow: allowlist}
}

// isDisallowedIP 判断 IP 是否为回环/内网/链路本地等不可外发地址。
func isDisallowedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	// IPv4-mapped IPv6（如 ::ffff:169.254.169.254）先转成 v4 判断。
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
	if ip.IsPrivate() { // 覆盖 10/8、172.16/12、192.168/16、fc00::/7
		return true
	}
	return false
}

// hostAllowed 判断 host 是否命中精确白名单或后缀白名单。
func (c *Checker) hostAllowed(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	for _, allow := range c.HostAllow {
		allow = strings.ToLower(allow)
		if allow == "" {
			continue
		}
		if h == allow {
			return true
		}
		// 后缀：.example.com 匹配 a.example.com
		if strings.HasPrefix(allow, ".") && strings.HasSuffix(h, allow) {
			return true
		}
	}
	return false
}

// Check 校验一个目标 URL：scheme 合法、host 解析出的 IP 均不命中禁止段。
//
// 只做校验、不保留结果，于是校验和真正拨号之间会各解析一次 DNS。
// 攻击者控制的域名可以让第一次解析返回公网地址、第二次返回 169.254.169.254，
// 这就是 DNS rebinding。需要发请求的调用方请改用 Resolve + Target.DialContext，
// 那边把解析结果钉死在拨号器里，没有第二个窗口。
func (c *Checker) Check(rawURL string) error {
	_, err := c.Resolve(rawURL)
	return err
}

// Target 是一次校验通过、并且解析结果已经钉死的目标。
type Target struct {
	Host string   // 原始 hostname（TLS SNI 与证书校验仍按它进行）
	Port string   // 连接端口
	IPs  []net.IP // 校验通过的地址，拨号只从这里挑
}

// Resolve 校验 URL 并把当前解析结果固定下来，供 Target.DialContext 使用。
func (c *Checker) Resolve(rawURL string) (*Target, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid url")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf("scheme not allowed: %s", scheme)
	}
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("empty host")
	}
	port := u.Port()
	if port == "" {
		if scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}

	// 字面量 IP 直接判断，不需要解析。
	if ip := net.ParseIP(host); ip != nil {
		if isDisallowedIP(ip) {
			if !c.AllowPrivate || len(c.HostAllow) == 0 || !c.hostAllowed(host) {
				return nil, fmt.Errorf("disallowed ip: %s", ip)
			}
		}
		return &Target{Host: host, Port: port, IPs: []net.IP{ip}}, nil
	}

	// 域名：DNS 解析后逐个 IP 判断。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("dns resolve failed: %s", host)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("dns resolve empty: %s", host)
	}
	blocked := false
	for _, ip := range ips {
		if isDisallowedIP(ip) {
			blocked = true
			break
		}
	}
	if blocked {
		if !c.AllowPrivate {
			return nil, fmt.Errorf("resolved to private/loopback address (SSRF)")
		}
		// 放开内网时必须命中 host 白名单。
		if len(c.HostAllow) == 0 || !c.hostAllowed(host) {
			return nil, fmt.Errorf("private address requires host allowlist match")
		}
	}
	return &Target{Host: host, Port: port, IPs: ips}, nil
}

// DialContext 只连接 Resolve 当时固定的那些 IP，不再做第二次 DNS 解析。
//
// 为什么不用 net.Dialer 原样传 host：那等于把「连哪儿」重新交给 DNS，
// 校验和连接之间就又出现了一次可以被换掉的解析。这里按 IP 直连，
// https 的 SNI 与证书校验不受影响 —— 那是 Transport 按 URL 里的 hostname
// 设置的，和我们拨到哪个 IP 是两回事。
func (t *Target) DialContext(ctx context.Context, network, _ string) (net.Conn, error) {
	d := &net.Dialer{Timeout: 10 * time.Second}
	var lastErr error
	for _, ip := range t.IPs {
		conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), t.Port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no resolved address")
	}
	return nil, lastErr
}

// CheckHost 只校验 host（供测试）。
func (c *Checker) CheckHost(host string) error {
	if ip := net.ParseIP(host); ip != nil {
		if isDisallowedIP(ip) {
			return fmt.Errorf("disallowed ip: %s", ip)
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return fmt.Errorf("dns failed")
	}
	for _, ip := range ips {
		if isDisallowedIP(ip) {
			return fmt.Errorf("disallowed ip: %s", ip)
		}
	}
	return nil
}
