package api

import (
	"net/http"
	"strings"

	"docforge/server/internal/update"
	"docforge/server/internal/version"
)

// 系统信息接口：当前版本 + 是否有新版本。
//
// 这两个接口都是**公开**的（不需要登录），原因很简单：
//   - 版本号不是秘密，二进制就在服务器上，`--version` 人人都能打；
//   - 更新提示本该在登录页也能看见（自建用户常常先进登录页才发现要升级）；
//   - 它们不返回任何业务数据，也不接受任何输入。
//
// 真正的风险是「这个接口会替访问者去打外网」。所以：
//  1. 结果有 6 小时缓存（update.Checker 里），外部打不穿；
//  2. 有 UPDATE_CHECK=false 可以整个关掉（隔离内网必须能用）；
//  3. 有 4 秒硬超时，且失败只标记 Stale，不重试、不阻塞请求。

// updateChecker 惰性构造检查器。
//
// 不用包级 sync.Once：一个进程里可能有多个 Server（测试就有一堆），
// 共用第一个实例等于让「这个实例的仓库/版本」被另一个实例复用。
// 惰性而不是在 New() 里建，是为了守住「构造时不做任何 I/O」这个约定。
func (s *Server) updateChecker() *update.Checker {
	s.updateOnce.Do(func() {
		repo := strings.TrimSpace(s.Cfg.UpdateRepo)
		if repo == "" {
			repo = "kinghtxg/zhuban"
		}
		s.updates = update.New(repo, version.Current().Version)
	})
	return s.updates
}

// handleVersion GET /api/v1/version
//
// 返回构建期注入的版本三元组。前端用它做两件事：
// ① 页脚/关于弹窗显示版本；② dev 构建时隐藏更新提示。
func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	v := version.Current()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"version":    v.Version,
		"commit":     v.Commit,
		"build_time": v.BuildTime,
		"dev":        v.Dev,
		"update_check": s.Cfg.UpdateCheck,
	})
}

// handleUpdates GET /api/v1/updates
//
// 查询 GitHub 上是否有更高的版本。
//
// 语义上有三点必须保持，改任何一条都会让这个功能变成"看起来在工作"：
//
//  1. **查失败 ≠ 没有更新**。失败时返回 stale=true 并带上一次的已知结果，
//     前端据此说"暂时无法检查更新"，而不是一声不吭——后者会让人以为
//     自己一直在最新版。
//  2. **默认走缓存**。刷新是显式动作（force=1），只有用户在弹窗里点"重新检查"
//     才会真的再打一次 GitHub。否则每个页面刷新都等于一次外网请求。
//  3. **关掉就明确关掉**。UPDATE_CHECK=false 时返回 disabled=true，
//     不是返回一个"当前已是最新"的假结果。
func (s *Server) handleUpdates(w http.ResponseWriter, r *http.Request) {
	if !s.Cfg.UpdateCheck {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"current":    version.Current().Version,
			"has_update": false,
			"dev":        version.Current().Dev,
			"disabled":   true,
			"checked_at": "",
		})
		return
	}

	force := r.URL.Query().Get("force") == "1" || r.URL.Query().Get("force") == "true"
	res := s.updateChecker().Check(r.Context(), force)
	writeJSON(w, http.StatusOK, res)
}
