package api

// 官网（site/）的投递。
//
// 为什么放在服务端而不是丢给前端路由：官网要被三类人看到——
//   - 第一次打开域名、还没有账号的人（此时前端 SPA 只会把他弹到 /login，
//     连"这是个什么产品"都说不清）；
//   - 从搜索引擎或分享链接点进来的人（需要一个能被抓取的真实 HTML 文档，
//     而不是靠 JS 渲染出来的空壳）；
//   - 想先看看值不值得部署的人（他连这个实例都还没建）。
//
// 所以判定放在 HTTP 这一层：/ 上没有有效会话就给官网，有会话才进工作台。
// 深链接（/templates、/jobs/...）不受影响，仍走 SPA。

import (
	"bytes"
	"net/http"
	"strings"
	"time"

	"docforge"
)

// landingPlaceholder 是官网里「指向本实例」的占位域名。
//
// 官网同时是可独立托管的静态站，所以链接只能写成绝对地址；
// 但自建实例的用户点「进入控制台」不该被送到别人的站上去。
// 所以由服务端在吐出来之前把它换成自己这一份的地址（EXTERNAL_BASE_URL）。
const landingPlaceholder = "https://zb.kinghtxg.com"

// landingPlaceholderHost 是同一个占位域名的主机部分（页面上有一处只写了主机名）。
const landingPlaceholderHost = "zb.kinghtxg.com"

// handleRoot 未登录时给官网，已登录时给工作台。
//
// 顺序是有讲究的：**没装过就先装**。一个空库实例的 / 上放官网，
// 用户会以为"这东西要注册"，然后去点登录、发现没账号、又没有注册入口——
// 而真正该做的是创建第一个管理员。所以未初始化时先送他去 /install，
// 装完才有官网/工作台这一层分流。
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		s.handleSPA(w, r)
		return
	}
	// 302 而不是 200 + 页面内跳转：安装向导必须是地址栏里真实的一个位置，
	// 否则刷新、收藏、把地址发给同事全都会回到官网页。
	if s.installAvailable() {
		http.Redirect(w, r, "/install", http.StatusFound)
		return
	}
	if s.Auth != nil && s.Auth.SessionUser(r) != nil {
		s.handleSPA(w, r)
		return
	}
	s.serveLanding(w, r)
}

// handleWelcome 永远给官网，已登录也一样。
//
// 留着它是因为「登录后还想再看一眼官网」是常见需求（比如把链接发给同事），
// 而 / 对已登录用户已经是工作台了。
func (s *Server) handleWelcome(w http.ResponseWriter, r *http.Request) {
	s.serveLanding(w, r)
}

// serveLanding 输出内嵌的官网首页。
func (s *Server) serveLanding(w http.ResponseWriter, r *http.Request) {
	data, err := docforge.SiteFS.ReadFile("site/index.html")
	if err != nil {
		// 内嵌资源读不到只可能是构建时就坏了（//go:embed 在编译期校验），
		// 所以这里不退化成 404 让访客以为站点没了，而是明确说不可用。
		http.Error(w, "官网资源缺失，请重新构建", http.StatusInternalServerError)
		return
	}
	data = bytes.ReplaceAll(data, []byte(landingPlaceholder), []byte(s.publicBaseURL(r)))
	// 页面上还有一处只写主机名的地方（首屏那句「线上运行 xxx」）。不替换的话，
	// 自建实例的首页会在最显眼的位置宣称自己运行在别人的域名上。
	if host := hostOf(s.publicBaseURL(r)); host != "" {
		data = bytes.ReplaceAll(data, []byte(landingPlaceholderHost), []byte(host))
	}

	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	// 短缓存：官网上的一句话改动要能很快生效；但也不能不缓存，
	// 否则每个未登录的访客都要走一次完整响应。
	h.Set("Cache-Control", "public, max-age=300")
	// 官网是纯静态页，不需要登录态，显式告诉中间层别按 cookie 分流缓存。
	h.Set("Vary", "Cookie")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// handleSiteAsset 提供官网目录下的静态文件（目前只有 landing.js）。
//
// 脚本必须走文件而不是内联：全站 CSP 是 script-src 'self'，内联 <script> 会被
// 浏览器拦掉——页面打得开、内容也在，但所有动效和计数不动，而且只有控制台里报错。
func (s *Server) handleSiteAsset(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/")
	// 只放行 site/ 下的单层文件名：这里不做通用文件服务，
	// 免得以后有人用 ../ 之类的路径把它变成目录遍历入口。
	if strings.ContainsAny(name, `/\`) || name == "" {
		http.NotFound(w, r)
		return
	}
	data, err := docforge.SiteFS.ReadFile("site/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	switch {
	case strings.HasSuffix(name, ".js"):
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	case strings.HasSuffix(name, ".css"):
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	case strings.HasSuffix(name, ".svg"):
		w.Header().Set("Content-Type", "image/svg+xml")
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	// 用零值 ModTime：内容来自二进制，没有有意义的修改时间，
	// 给个"现在"会让每次请求都算成刚改过，缓存头等于白写。
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
}

// publicBaseURL 返回「外部可访问的站点根地址」，用于官网里的链接改写。
//
// 优先用配置里的 EXTERNAL_BASE_URL：部署在反代/WAF 后面时，请求里的 Host
// 未必是用户实际访问的那个域名（尤其是内网直连调试时）。没配才退回到请求头。
func (s *Server) publicBaseURL(r *http.Request) string {
	if base := strings.TrimSpace(s.Cfg.ExternalBaseURL); base != "" {
		return strings.TrimSuffix(base, "/")
	}
	scheme := "http"
	if s.isHTTPS(r) {
		scheme = "https"
	}
	host := r.Host
	if host == "" {
		host = "127.0.0.1"
	}
	return scheme + "://" + host
}

// hostOf 从 base URL 里取主机名（含端口）。取不到就返回空串，调用方跳过替换。
func hostOf(base string) string {
	i := strings.Index(base, "://")
	if i < 0 {
		return ""
	}
	rest := base[i+3:]
	if j := strings.IndexByte(rest, '/'); j >= 0 {
		rest = rest[:j]
	}
	return rest
}
