package api

import (
	"net/http"
	"strings"
)

// contentSecurityPolicy 是全站的脚本执行边界。
//
// 为什么应用层也要设：以前这些头全靠外置 nginx 的 add_header。可 nginx 是
// 「部署时可选的一层」——本地 run 起来、内网直连 18100、别人 fork 后自己搭的
// compose，全都没有那层。安全头写在产品里才是默认成立的，写在部署文档里
// 只是「记得的话就成立」。
//
// 逐项说明为什么是这几个值：
//   - script-src 'self'：Vite 产物全是外部文件，index.html 里没有内联脚本，
//     所以不需要 'unsafe-inline'。这一条是整个 CSP 里最值钱的。
//   - style-src 必须带 'unsafe-inline'：Arco 与 Monaco 大量使用行内 style 属性，
//     Monaco 还会在运行期插入 <style> 标签做动态装饰。去掉它编辑器直接变白板。
//   - img-src / font-src 带 data:：Vite 会把小图标、字体子集内联成 data URI。
//   - worker-src 带 blob:：Monaco 的 JSON/Editor worker 在部分路径下用 blob 起线程。
//   - frame-ancestors 'none' + 下文的 X-Frame-Options：禁止被任何页面嵌套，
//     防点击劫持。注意它只限制「我们被谁嵌」，不限制我们自己嵌一个隐藏 iframe
//     去接导出下载——编辑器导出用的正是这个手法。
const contentSecurityPolicy = "default-src 'self'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob:; " +
	"font-src 'self' data:; " +
	"connect-src 'self'; " +
	"worker-src 'self' blob:; " +
	"frame-src 'self' blob:; " +
	"object-src 'none'; " +
	"base-uri 'self'; " +
	"form-action 'self'; " +
	"frame-ancestors 'none'"

// securityHeaders 给所有响应挂上安全头（含 JSON 与文件下载响应）。
//
// 挂全局而不是只挂 HTML：nosniff 的意义就在于「即使这个响应被当成页面打开，
// 浏览器也不会猜类型」。直链下载、预览图、错误 JSON 都可能被诱导直接打开，
// 那些路径恰恰没有页面模板可以放 meta。
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=(), payment=()")
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		// HSTS 只在确认走的是 https 时才发。
		// 明文部署下发它没有作用（浏览器只认 https 响应上的 HSTS），
		// 可一旦哪天改用 https 打开，浏览器就会把整站锁死到 https ——
		// 包括调试端口和自签证书的内网地址，锁错了要用户手动清缓存才能恢复。
		//
		// 不带 includeSubDomains 是有意的：这个产品经常被部署在
		// zb.example.com 这种子域上，把兄弟子域一起锁进 https 属于越权决定。
		if s.isHTTPS(r) {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

// isHTTPS 判断这次请求在客户端侧是否为 https。
// 转发头只在「对端是可信代理」时才看 —— 和来源 IP 用同一个信任判定，
// 否则任何人都能用一个头把响应变成「https 响应」，进而拿到 HSTS。
func (s *Server) isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if !s.IPs.TrustedProxy(r) {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}
