package api

import (
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"docforge"
)

// 安装向导：未初始化时把 / 导到 /install，装完把这一整块从存在性上抹掉。
//
// 为什么不做成 SPA 里的一个路由：SPA 的首屏必须先加载 JS 才知道"要不要装"，
// 而装不了的原因往往是**服务端还没配好**——这时用户看到的是一个白屏。
// 安装页是纯静态 HTML，不依赖前端构建产物，容器起得来就能打开。
//
// 「装完自动删除」的落地方式有两层，缺一层都不算数：
//   1. 路由层：已初始化时 /install 与 /install.js 直接 404（§ installDir 也不服务）；
//   2. 文件层：首次启动时把页面释放到 <DATA_DIR>/install/，装完 os.RemoveAll 掉。
// 只做路由层，页面的 HTML 仍然躺在磁盘上等人捡；只做文件层，重启一次就复活。

// installFile 是安装页的文件名。整块目录只用这两个文件。
const installFile = "index.html"

// installDir 返回安装页在数据目录里的落盘位置。
//
// 放数据目录而不是临时目录：tmpfs 重启即空，装到一半重启会让向导"消失"；
// 放数据目录还能让运维在装之前手工改一版（比如换成公司自己的欢迎语），
// 改完刷新即可生效，不用重新打包镜像。
func (s *Server) installDir() string {
	return filepath.Join(s.Cfg.DataDir, "install")
}

// PrepareInstallPage 在未初始化时把内嵌的安装页释放到磁盘。
//
// 幂等：已存在就覆盖——运维可能改坏了想还原，重启一次就该回到出厂状态。
// 已初始化则**什么都不做**：否则每次重启都往数据目录里放回一份死页面，
// 而那个目录会跟着备份一起被拷来拷去。
// 返回错误只记日志、不影响启动：数据目录只读时向导仍可从内嵌资源直出。
func (s *Server) PrepareInstallPage() error {
	if !s.installAvailable() {
		return nil
	}
	dir := s.installDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	entries, err := fs.ReadDir(docforge.InstallFS, "install")
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := docforge.InstallFS.ReadFile("install/" + e.Name())
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, e.Name()), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// removeInstallPage 删除落盘的安装页。装完调用一次。
//
// 失败只记日志：路由层已经关了，删不掉不会让入口重新打开。
// 反过来，为了"删干净"而让安装流程失败，是拿可用性换整洁。
func (s *Server) removeInstallPage() {
	dir := s.installDir()
	if err := os.RemoveAll(dir); err != nil {
		// 用 log 而不是写进响应：这不该影响用户看到的"安装完成"。
		log.Printf("[install] 删除安装页失败（%v）；入口已关闭，可手工删除 %s", err, dir)
	}
}

// installAvailable 是「这个实例还需不需要装」的唯一判据。
func (s *Server) installAvailable() bool {
	return s.Auth != nil && !s.Auth.Initialized()
}

// handleInstallPage GET /install
//
// 已初始化 → 404。这里刻意不回 302 到登录页：
// 一个 404 就是"这里没有东西"的准确表达，而 302 会让人以为地址还能用。
func (s *Server) handleInstallPage(w http.ResponseWriter, r *http.Request) {
	if !s.installAvailable() {
		s.installGone(w)
		return
	}
	data, err := s.readInstallFile(installFile)
	if err != nil {
		http.Error(w, "安装页资源缺失，请重新构建镜像", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	// 安装页不能缓存：装完之后浏览器拿着缓存还能打开，会让人以为入口还在。
	h.Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

// handleInstallAsset GET /install.js
func (s *Server) handleInstallAsset(w http.ResponseWriter, r *http.Request) {
	if !s.installAvailable() {
		s.installGone(w)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/")
	if strings.ContainsAny(name, `/\`) || name == "" {
		http.NotFound(w, r)
		return
	}
	data, err := s.readInstallFile(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", contentTypeByExt(name))
	h.Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

// readInstallFile 先读磁盘（可能是运维改过的版本），读不到再退回内嵌资源。
func (s *Server) readInstallFile(name string) ([]byte, error) {
	if data, err := os.ReadFile(filepath.Join(s.installDir(), name)); err == nil {
		return data, nil
	}
	return docforge.InstallFS.ReadFile("install/" + name)
}

// installGone 是安装入口永久关闭后的统一响应。
func (s *Server) installGone(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">` +
		`<title>404 · 铸版</title><meta name="robots" content="noindex">` +
		`<style>body{margin:0;min-height:100vh;display:grid;place-items:center;background:#06070B;` +
		`color:#EEF1F8;font:15px/1.7 -apple-system,BlinkMacSystemFont,"PingFang SC","Microsoft YaHei",sans-serif}` +
		`div{text-align:center;padding:32px}a{color:#4D7CFF;text-decoration:none}` +
		`p{color:#6F7791;font-size:13px}</style></head><body><div>` +
		`<h1 style="font-size:20px;font-weight:650;margin:0 0 8px">这里没有东西</h1>` +
		`<p>安装向导在初始化完成后即被移除。</p>` +
		`<p style="margin-top:18px"><a href="/">前往首页</a></p>` +
		`</div></body></html>`))
}

// contentTypeByExt 只覆盖 install 目录里会出现的两种类型。
// 不用 mime.TypeByExtension：它在不同系统上的注册表不同，Windows 上
// .js 常被注册成 text/plain，浏览器于是拒绝执行脚本（页面上所有按钮失灵）。
func contentTypeByExt(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".js":
		return "application/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".html":
		return "text/html; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}
