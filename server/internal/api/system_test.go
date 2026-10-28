package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"docforge/server/internal/config"
	"docforge/server/internal/version"
)

// 这两个接口是**公开**的，用例因此直接打路由而不带 cookie。
// 它们被匿名放行的理由写在 authmatrix_test.go 的白名单里，这里只验行为。

func getJSON(t *testing.T, s *Server, path string) (int, map[string]interface{}) {
	t.Helper()
	r := buildRouter(s)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var body map[string]interface{}
	if w.Body.Len() > 0 {
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s 返回的不是 JSON：%s", path, w.Body.String())
		}
	}
	return w.Code, body
}

func TestVersionEndpoint_Anonymous(t *testing.T) {
	oldV, oldC, oldB := version.Version, version.Commit, version.BuildTime
	version.Version, version.Commit, version.BuildTime = "3.4.0", "abc1234", "2026-10-29T00:00:00Z"
	defer func() { version.Version, version.Commit, version.BuildTime = oldV, oldC, oldB }()

	s, _ := setupFullServer(t, nil)
	code, body := getJSON(t, s, "/api/v1/version")
	if code != http.StatusOK {
		t.Fatalf("匿名访问版本接口应 200，实际 %d", code)
	}
	if body["version"] != "3.4.0" || body["commit"] != "abc1234" || body["build_time"] != "2026-10-29T00:00:00Z" {
		t.Fatalf("版本三元组透传有误：%v", body)
	}
	if dev, _ := body["dev"].(bool); dev {
		t.Fatalf("3.4.0 不该被判成 dev：%v", body)
	}
}

// UPDATE_CHECK=false 时必须明确返回 disabled，而不是伪造一个「已是最新」。
// 这两者对用户的含义完全不同：前者是「这台机器不检查更新」，
// 后者是「我替你查过了，你没事」。
func TestUpdatesEndpoint_Disabled(t *testing.T) {
	s, _ := setupFullServer(t, func(c *config.Config) { c.UpdateCheck = false })
	code, body := getJSON(t, s, "/api/v1/updates")
	if code != http.StatusOK {
		t.Fatalf("应 200（这是个配置状态，不是错误），实际 %d", code)
	}
	if disabled, _ := body["disabled"].(bool); !disabled {
		t.Fatalf("UPDATE_CHECK=false 应返回 disabled=true：%v", body)
	}
	if up, ok := body["has_update"]; !ok || up != false {
		t.Fatalf("禁用时也要显式给 has_update=false：%v", body)
	}
}

// 开启时把检查器指到一个不存在的地址：必须 200 + stale，不能 5xx。
// 更新检查失败是「这个功能此刻没数据」，不是「服务器出故障」。
func TestUpdatesEndpoint_UnreachableIsStaleNotError(t *testing.T) {
	s, _ := setupFullServer(t, func(c *config.Config) {
		c.UpdateCheck = true
		c.UpdateRepo = "o/r"
	})
	// 直接替换惰性构造出来的检查器：把 API 指到一个已关闭的端口。
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	c := s.updateChecker()
	c.APIBase = deadURL
	c.TTL = 0 // 保证这次真的去请求，不吃缓存

	code, body := getJSON(t, s, "/api/v1/updates")
	if code != http.StatusOK {
		t.Fatalf("检查失败不该变成 5xx，实际 %d（body=%v）", code, body)
	}
	if stale, _ := body["stale"].(bool); !stale {
		t.Fatalf("网络不通应标 stale=true：%v", body)
	}
	if up, _ := body["has_update"].(bool); up {
		t.Fatalf("查失败时不该声称有更新：%v", body)
	}
	if _, ok := body["current"]; !ok {
		t.Fatalf("失败时也要带上当前版本，前端要靠它显示：%v", body)
	}
}
