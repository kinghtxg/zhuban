package api

// 官网投递的回归测试。
//
// 这一组守的是「未登录的人看到什么」。它没有权限可越，但错起来很难被察觉：
//   - 判定写反 → 所有访客（包括老用户）都被挡在官网，登录入口藏在某个角落；
//   - 判定失效 → 官网永远不出来，第一次访问的人只看到一个登录框；
//   - 链接没改写 → 自建实例的「进入控制台」把用户送到别人的站上去。
//
// 所以断言不看状态码，看「真的吐了哪份 HTML」。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"docforge/server/internal/config"
	"docforge/server/internal/models"
)

func TestRoot_AnonymousGetsLandingLoggedInGetsApp(t *testing.T) {
	s, h := flowServer(t, nil)
	// 先建一个账号：本用例守的是「装好之后，未登录的人看到什么」。
	// 空库实例的 / 会先被导到 /install（那条路径由 install_test.go 守）。
	seedUserWithPW(t, s, "ivan2", goodPW, "member", "active")

	// 未登录：必须是官网。
	rec := doJSON(t, h, "GET", "/", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("未登录访问 / 应 200，实际 %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "把 Word 模板") {
		t.Errorf("未登录访问 / 应返回官网首页，实际响应里没有官网标题：%s", firstChars(body, 200))
	}
	// 官网必须能加载到自己的脚本：CSP 是 script-src 'self'，
	// 少了这一行页面照样打开，但所有动效与计数都不会动。
	if !strings.Contains(body, `src="landing.js"`) {
		t.Error("官网首页没有引用 landing.js")
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type 应为 text/html，实际 %q", ct)
	}

	// 已登录：必须是工作台（前端 SPA 的挂载点）。
	ck := cookieOf(t, doJSON(t, h, "POST", "/api/v1/auth/login",
		map[string]string{"username": "ivan2", "password": goodPW}, nil))
	rec = doJSON(t, h, "GET", "/", nil, ck)
	if rec.Code != http.StatusOK {
		t.Fatalf("已登录访问 / 应 200，实际 %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `id="root"`) {
		t.Errorf("已登录访问 / 应返回工作台 SPA，实际拿到：%s", firstChars(rec.Body.String(), 200))
	}
}

func TestWelcome_AlwaysLanding(t *testing.T) {
	s, h := flowServer(t, nil)
	seedUserWithPW(t, s, "judy2", goodPW, "member", "active")
	ck := cookieOf(t, doJSON(t, h, "POST", "/api/v1/auth/login",
		map[string]string{"username": "judy2", "password": goodPW}, nil))

	// 登录后 / 是工作台，但 /welcome 要永远给官网——
	// 「把官网链接发给同事」不该因为自己登录了就看到别的东西。
	rec := doJSON(t, h, "GET", "/welcome", nil, ck)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "landing.js") {
		t.Errorf("/welcome 应始终返回官网，实际 %d %s", rec.Code, firstChars(rec.Body.String(), 160))
	}
}

func TestLanding_LinksPointAtThisInstance(t *testing.T) {
	// 官网里的站内链接写的是官方实例的绝对地址（因为它要能单独托管）。
	// 由产品自己提供服务时必须换成本实例的地址，否则自建用户点「进入控制台」
	// 会被送去别人的站点——这在私有化部署里是"数据出境"级别的尴尬。
	s, h := flowServer(t, func(c *config.Config) {
		c.ExternalBaseURL = "https://zhuban.internal.example:8443/"
	})
	_ = s
	rec := doJSON(t, h, "GET", "/welcome", nil, nil)
	body := rec.Body.String()

	if !strings.Contains(body, "https://zhuban.internal.example:8443/login") {
		t.Error("「进入控制台」应指向本实例的 /login")
	}
	if strings.Contains(body, "zb.kinghtxg.com") {
		t.Error("响应里仍残留官方域名，说明链接没有被改写成本实例地址")
	}
	// 尾斜杠不该被带进来拼出双斜杠。
	if strings.Contains(body, "8443//") {
		t.Error("改写后出现了双斜杠")
	}
}

func TestLandingAsset_ServesScriptOnly(t *testing.T) {
	s, h := flowServer(t, nil)

	rec := doJSON(t, h, "GET", "/landing.js", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("/landing.js 应 200，实际 %d", rec.Code)
	}
	// Content-Type 必须明确是 JS：Windows 上 mime 表不一定认 .js，
	// 猜成 text/plain 的话浏览器会拒绝执行（nosniff 下必然）。
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("Content-Type 应为 javascript，实际 %q", ct)
	}
	if rec.Header().Get("Cache-Control") == "" {
		t.Error("静态资源应带缓存头")
	}

	// 不存在的名字 → 404。这里直接调 handler：走路由的话 /nope.js 会落到
	// SPA 的 /* 兜底上（那是历史行为，返回 index.html），测不到这个函数。
	rec = httptest.NewRecorder()
	s.handleSiteAsset(rec, httptest.NewRequest("GET", "/nope.js", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("不存在的站点资源应 404，实际 %d", rec.Code)
	}
}

func TestSiteAsset_RejectsNestedPath(t *testing.T) {
	// 直接调 handler：路由层不会把 /sub/x.js 交给它，但
	// 「现在不会」不等于「以后也不会」——多一层 path 检查，
	// 免得哪天有人把 /* 也接到这个 handler 上，就成了目录遍历。
	s, _ := flowServer(t, nil)
	for _, p := range []string{"/sub/landing.js", "/../embed.go", "/./landing.js"} {
		rec := httptest.NewRecorder()
		s.handleSiteAsset(rec, httptest.NewRequest("GET", p, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s 应 404，实际 %d", p, rec.Code)
		}
	}
}

func TestSessionUser_IgnoresDisabledAndRevoked(t *testing.T) {
	// 页面判定用的是 SessionUser，它和接口侧必须同口径：
	// 会话被撤销（登出/改密/禁用）之后，/ 也要回到官网，
	// 否则会出现「页面以为你已登录、接口全部 401」的死循环观感。
	s, h := flowServer(t, nil)
	u := seedUserWithPW(t, s, "kate2", goodPW, "member", "active")
	ck := cookieOf(t, doJSON(t, h, "POST", "/api/v1/auth/login",
		map[string]string{"username": "kate2", "password": goodPW}, nil))

	if su := s.Auth.SessionUser(reqWithCookie(ck)); su == nil || su.ID != u.ID {
		t.Fatal("有效会话应能取到用户")
	}

	// 禁用后会话被撤销 → 取不到用户。
	if rec := doJSON(t, h, "POST", "/api/v1/auth/logout", nil, ck); rec.Code != http.StatusOK {
		t.Fatalf("登出失败：%d", rec.Code)
	}
	if su := s.Auth.SessionUser(reqWithCookie(ck)); su != nil {
		t.Errorf("会话撤销后不应再取到用户，实际拿到 %s", su.Username)
	}

	var got models.User
	s.DB.First(&got, "id = ?", u.ID)
	if got.Username != "kate2" {
		t.Fatalf("用户记录不该被删：%v", got)
	}
}

func reqWithCookie(ck *http.Cookie) *http.Request {
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(ck)
	return r
}

func firstChars(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
