// Package clientip 解析「这次请求的真实来源 IP」。
//
// 这件事以前是一行代码：X-Forwarded-For 取第一段，没有就用 RemoteAddr。
// 那一行的隐含前提是「只有我们自己的代理会写这个头」，可这个头客户端也能写。
// 后果不是理论上的：登录限流按 IP 计数、审计日志按 IP 留痕，攻击者在头上
// 填一个随机 IP，每换一次就重新获得一整份额度，而审计里留下的是一串假地址。
// 限流与取证同时失效，而且失效得毫无声息。
//
// 所以这里反过来做：只有当「直连对端本身就在信任名单里」时才看转发头。
// 解析方向也要反过来——X-Forwarded-For 是「客户端, 代理1, 代理2」，
// 右边是离我们最近的一跳。从右往左找第一个不在信任链里的地址，那个才是
// 真正能确定身份的一端；从左往右取第一段等于取了攻击者自己写的那一段。
package clientip

import (
	"fmt"
	"net"
	"net/http"
	"strings"
)

// Resolver 按可信代理名单解析来源 IP。零值/nil 表示「谁都不信」，只认 RemoteAddr。
type Resolver struct {
	trusted []*net.IPNet
}

// New 解析 CIDR 或单个 IP 组成的名单。列表为空时返回的 Resolver 谁都不信任。
func New(specs []string) (*Resolver, error) {
	rv := &Resolver{}
	for _, s := range specs {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		// 允许写裸 IP：自动补成 /32 或 /128，省得每个部署都得记前缀长度。
		if !strings.Contains(s, "/") {
			ip := net.ParseIP(s)
			if ip == nil {
				return nil, fmt.Errorf("invalid trusted proxy %q", s)
			}
			bits := 128
			if ip.To4() != nil {
				bits = 32
				ip = ip.To4()
			}
			s = fmt.Sprintf("%s/%d", ip.String(), bits)
		}
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			return nil, fmt.Errorf("invalid trusted proxy %q", s)
		}
		rv.trusted = append(rv.trusted, n)
	}
	return rv, nil
}

// isTrusted 判断某个地址是否落在信任名单里。
func (rv *Resolver) isTrusted(raw string) bool {
	if rv == nil || len(rv.trusted) == 0 || raw == "" {
		return false
	}
	ip := net.ParseIP(raw)
	if ip == nil {
		return false
	}
	for _, n := range rv.trusted {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// TrustedProxy 判断这次请求的对端是不是可信代理。
// 除了解析 IP，它还有一个用处：决定「能不能相信 X-Forwarded-Proto」。
// 两者信任来源必须是同一个，否则 HSTS 这类按协议下发的响应头就能被伪造。
func (rv *Resolver) TrustedProxy(r *http.Request) bool {
	if r == nil {
		return false
	}
	return rv.isTrusted(hostOnly(r.RemoteAddr))
}

// ClientIP 返回请求的真实来源 IP。r 为 nil 时返回空串（审计写入允许没有请求）。
func (rv *Resolver) ClientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	peer := hostOnly(r.RemoteAddr)
	if !rv.isTrusted(peer) {
		// 对端不是我们的代理，它带来的转发头一律视为客户端自填的噪声。
		return peer
	}
	hops := parseXFF(r.Header.Get("X-Forwarded-For"))
	// 从右往左：跳过我们自己加的每一跳，第一个不是代理的地址就是客户端。
	for i := len(hops) - 1; i >= 0; i-- {
		if !rv.isTrusted(hops[i]) {
			return hops[i]
		}
	}
	// 走到这里说明 X-Forwarded-For 整条都是内网地址，或者干脆没有这个头。
	// 实测情形：nginx → 星盾 → docker-proxy → 容器，中间几跳把 XFF 覆盖成了
	// 自己的地址，真实客户端只留在 X-Real-IP 里（nginx 用 $remote_addr 覆盖写，
	// 客户端自己填的值在这一层就被冲掉了，所以它同样伪造不了）。
	if ip := headerIP(r, "X-Real-IP"); ip != "" {
		return ip
	}
	// 最后才退回链上最早的一跳；再不行就只能是直连对端。
	if len(hops) > 0 {
		return hops[0]
	}
	return peer
}

// headerIP 读一个单值 IP 头，非法值当没写。
func headerIP(r *http.Request, key string) string {
	v := strings.TrimSpace(r.Header.Get(key))
	if v == "" {
		return ""
	}
	v = strings.Trim(v, "[]")
	if ip := net.ParseIP(v); ip != nil {
		return ip.String()
	}
	return ""
}

// hostOnly 从 "1.2.3.4:5678" / "[::1]:5678" / "1.2.3.4" 中取出纯地址。
func hostOnly(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return strings.Trim(host, "[]")
	}
	// 没有端口的形态（测试里常见），或 IPv6 裸地址。
	return strings.Trim(addr, "[]")
}

// parseXFF 拆 X-Forwarded-For，只保留能解析成 IP 的项。
// 解析不了的项直接丢掉：留着它意味着把一个不可信的字符串写进审计日志，
// 而这个值最终会出现在页面和导出文件里。
func parseXFF(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	out := make([]string, 0, 4)
	for _, part := range strings.Split(raw, ",") {
		v := strings.TrimSpace(part)
		if v == "" {
			continue
		}
		if ip := net.ParseIP(v); ip != nil {
			out = append(out, ip.String())
		}
	}
	return out
}
