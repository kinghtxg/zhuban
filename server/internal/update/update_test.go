package update

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"docforge/server/internal/version"
)

// fakeGitHub 起一个假 GitHub API。
//
// release 为 nil 时 /releases/latest 返回 404（模拟「只打了 tag、没建 Release」）。
// 返回的计数指针用来断言"到底打了几次网"——缓存用例全靠它。
func fakeGitHub(t *testing.T, release map[string]interface{}, tags []string) (*httptest.Server, *int32) {
	t.Helper()
	var hits int32
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if r.Header.Get("User-Agent") == "" {
			// 真 GitHub 对无 UA 的请求返回 403，这里同样拦，防止代码里把 UA 弄丢。
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if release == nil {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(release)
	})
	mux.HandleFunc("/repos/o/r/tags", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		items := make([]map[string]string, 0, len(tags))
		for _, n := range tags {
			items = append(items, map[string]string{"name": n})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(items)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &hits
}

func newTestChecker(srv *httptest.Server, current string) *Checker {
	c := New("o/r", current)
	c.APIBase = srv.URL
	return c
}

func TestCheck_ReleaseNewer(t *testing.T) {
	srv, _ := fakeGitHub(t, map[string]interface{}{
		"tag_name":     "v3.5.0",
		"name":         "铸版 3.5.0",
		"body":         "新增 install 向导",
		"html_url":     "https://github.com/o/r/releases/tag/v3.5.0",
		"published_at": "2026-10-30T00:00:00Z",
	}, nil)
	c := newTestChecker(srv, "3.4.0")

	r := c.Check(context.Background(), false)
	if !r.HasUpdate {
		t.Fatalf("3.4.0 遇到 3.5.0 应该提示更新，实际 %+v", r)
	}
	if r.Latest != "v3.5.0" || r.URL == "" || r.Name == "" {
		t.Fatalf("结果字段不全：%+v", r)
	}
	if r.Stale {
		t.Fatal("成功拿到结果不该标 stale")
	}
	if r.CheckedAt == "" {
		t.Fatal("CheckedAt 必须填，前端靠它显示「刚刚检查」")
	}
}

// 同版本或更低版本一律不提示。这条最容易写反成 "Latest != Current"，
// 那样「服务器上跑着 3.5.0-pre、GitHub 上是 3.4.0」也会弹更新。
func TestCheck_SameOrOlderNoUpdate(t *testing.T) {
	for _, tag := range []string{"v3.4.0", "v3.3.9", "3.4.0", "v3.4.0-rc1"} {
		srv, _ := fakeGitHub(t, map[string]interface{}{"tag_name": tag, "html_url": "x"}, nil)
		c := newTestChecker(srv, "3.4.0")
		if r := c.Check(context.Background(), false); r.HasUpdate {
			t.Errorf("当前 3.4.0、最新 %s 不该提示更新，实际 %+v", tag, r)
		}
	}
}

// dev 构建不参与比较：版本号没有可比性，弹出来只会让开发者困惑。
func TestCheck_DevBuildNeverNotifies(t *testing.T) {
	old := version.Version
	version.Version = "dev"
	defer func() { version.Version = old }()

	srv, _ := fakeGitHub(t, map[string]interface{}{"tag_name": "v99.0.0", "html_url": "x"}, nil)
	c := newTestChecker(srv, "dev")

	r := c.Check(context.Background(), false)
	if !r.Dev {
		t.Fatal("应标记 dev")
	}
	if r.HasUpdate {
		t.Fatalf("dev 构建不该提示更新，实际 %+v", r)
	}
}

// prerelease 不提示：服务器升级应该是明确动作，不该被 rc 打断。
func TestCheck_PrereleaseIgnored(t *testing.T) {
	srv, _ := fakeGitHub(t, map[string]interface{}{
		"tag_name": "v3.5.0-rc1", "prerelease": true, "html_url": "x",
	}, nil)
	c := newTestChecker(srv, "3.4.0")
	r := c.Check(context.Background(), false)
	if r.HasUpdate {
		t.Fatalf("prerelease 不该提示，实际 %+v", r)
	}
}

// 只打了 tag、没建 Release 时必须退回 tags，且只认识 semver 形状的 tag。
func TestCheck_FallsBackToTags(t *testing.T) {
	srv, _ := fakeGitHub(t, nil, []string{
		"v3.3.0", "v3.6.2", "nightly", "release-2026", "3.5", "v3.10.0",
	})
	c := newTestChecker(srv, "3.4.0")

	r := c.Check(context.Background(), false)
	if !r.HasUpdate {
		t.Fatalf("tags 里最高是 3.10.0，应该提示更新，实际 %+v", r)
	}
	// 字符串排序会把 3.6.2 当成最高；正确结果是 3.10.0。
	if strings.TrimPrefix(r.Latest, "v") != "3.10.0" {
		t.Fatalf("应取数值最大的 tag 3.10.0，实际 %q", r.Latest)
	}
}

func TestCheck_NoUsableTags(t *testing.T) {
	srv, _ := fakeGitHub(t, nil, []string{"nightly", "main"})
	c := newTestChecker(srv, "3.4.0")
	r := c.Check(context.Background(), false)
	if r.HasUpdate || r.Stale {
		t.Fatalf("没有可用 tag 时既不该提示更新也不该标 stale，实际 %+v", r)
	}
}

// 缓存：TTL 内重复调用不得再打网。这是「公开接口不能变成外网放大器」的底线。
func TestCheck_CachesWithinTTL(t *testing.T) {
	srv, hits := fakeGitHub(t, map[string]interface{}{"tag_name": "v3.4.1", "html_url": "x"}, nil)
	c := newTestChecker(srv, "3.4.0")

	for i := 0; i < 5; i++ {
		c.Check(context.Background(), false)
	}
	if n := atomic.LoadInt32(hits); n != 1 {
		t.Fatalf("TTL 内应只请求 1 次，实际 %d 次", n)
	}

	// force 必须真的绕过缓存（用户在弹窗里点「重新检查」）。
	c.Check(context.Background(), true)
	if n := atomic.LoadInt32(hits); n != 2 {
		t.Fatalf("force=true 应再请求一次，实际累计 %d 次", n)
	}
}

// 刷新失败必须保留上一次的结果并标 stale，而不是把 has_update 清成 false。
// 把「没查成」当成「没有更新」，用户会以为更新检查在工作，其实一直是哑的。
func TestCheck_FailureKeepsPreviousAndMarksStale(t *testing.T) {
	var fail atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusForbidden) // 限流
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"tag_name": "v3.5.0", "html_url": "https://example/x", "name": "3.5.0",
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestChecker(srv, "3.4.0")
	first := c.Check(context.Background(), false)
	if !first.HasUpdate {
		t.Fatalf("首次应成功并提示更新：%+v", first)
	}

	fail.Store(true)
	second := c.Check(context.Background(), true)
	if !second.Stale {
		t.Fatalf("刷新失败应标 stale：%+v", second)
	}
	if !second.HasUpdate || second.Latest != first.Latest {
		t.Fatalf("失败时应保留上一次结果（has_update=%v latest=%q），实际 %+v",
			second.HasUpdate, second.Latest, second)
	}
}

// 第一次就失败：不能 panic，也要标 stale，且不该伪造一个「已是最新」。
func TestCheck_FirstFailureIsStale(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := newTestChecker(srv, "3.4.0")
	r := c.Check(context.Background(), false)
	if !r.Stale {
		t.Fatalf("首次失败必须标 stale：%+v", r)
	}
	if r.HasUpdate {
		t.Fatalf("失败时不该声称有更新：%+v", r)
	}
	if r.Current != "3.4.0" {
		t.Fatalf("Current 必须回填，前端要拿它显示版本：%+v", r)
	}
}

// 网络整体不通（连接被拒）：同样只标 stale，不能阻塞超过超时。
func TestCheck_NetworkDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // 拿到地址立刻关掉 → 连接被拒

	c := New("o/r", "3.4.0")
	c.APIBase = url

	done := make(chan Result, 1)
	go func() { done <- c.Check(context.Background(), false) }()
	select {
	case r := <-done:
		if !r.Stale {
			t.Fatalf("网络不通应标 stale：%+v", r)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("网络不通时 Check 超过 8 秒未返回，超时保护没生效")
	}
}

func TestLooksLikeSemver(t *testing.T) {
	yes := []string{"3.4.0", "3.4", "10.0.0", "3.4.0-rc1"}
	no := []string{"", "v3.4.0", "nightly", "main", "release-2026", "3", "3.x"}
	for _, s := range yes {
		if !looksLikeSemver(s) {
			t.Errorf("%q 应被认作版本号", s)
		}
	}
	for _, s := range no {
		if looksLikeSemver(s) {
			t.Errorf("%q 不该被认作版本号", s)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("abc", 5); got != "abc" {
		t.Fatalf("短串不该截断：%q", got)
	}
	got := truncate(strings.Repeat("中", 10), 3)
	if got != "中中中…" {
		t.Fatalf("应按字符截断并加省略号，实际 %q", got)
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", "  ", "x", "y"); got != "x" {
		t.Fatalf("actual %q", got)
	}
	if got := firstNonEmpty("", ""); got != "" {
		t.Fatalf("actual %q", got)
	}
}
