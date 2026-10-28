package api

// 安装向导的回归测试。
//
// 这一组的核心不是"页面能打开"，而是**装完之后入口是否真的没了**：
// 一个还能打开的安装页，在空库上等于"任何人可以重建管理员"。
// 所以断言分两层——
//   路由层：/install、/install.js 装完必须 404；
//   文件层：释放到数据目录的那份必须被删掉（否则"页面还在但打不开"
//           会让人以为只是链接变了，重启一次又活过来）。

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"docforge/server/internal/config"
)

func TestInstall_FreshInstanceRedirectsRoot(t *testing.T) {
	_, h := flowServer(t, nil)

	rec := doJSON(t, h, "GET", "/", nil, nil)
	if rec.Code != http.StatusFound {
		t.Fatalf("空库实例访问 / 应 302 到 /install，实际 %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/install" {
		t.Fatalf("Location 应为 /install，实际 %q", loc)
	}
}

func TestInstall_PageServedWhenNotInitialized(t *testing.T) {
	_, h := flowServer(t, nil)

	rec := doJSON(t, h, "GET", "/install", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("/install 应 200，实际 %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "安装") {
		t.Fatalf("响应不像安装页：%s", firstChars(body, 160))
	}
	// 脚本必须是外部文件：CSP 是 script-src 'self'，内联 <script> 会被拦掉。
	if !strings.Contains(body, `src="/install.js"`) {
		t.Error("安装页没有引用 /install.js，CSP 下所有按钮都会失灵")
	}
	if strings.Contains(body, "<script>") {
		t.Error("安装页里有内联 <script>，会被 CSP 静默拦掉")
	}
	// 装完还能用的缓存会让"入口已关闭"变成一句空话。
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("安装页不能缓存，实际 Cache-Control=%q", cc)
	}
}

func TestInstall_ScriptServedAsJavaScript(t *testing.T) {
	_, h := flowServer(t, nil)

	rec := doJSON(t, h, "GET", "/install.js", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("/install.js 应 200，实际 %d", rec.Code)
	}
	// 必须显式给 JS 类型：Windows 上 mime 表可能把 .js 认成 text/plain，
	// nosniff 下浏览器会拒绝执行（页面能开、按钮全死，且只在控制台报错）。
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("Content-Type 应为 javascript，实际 %q", ct)
	}
}

// 装完之后：路由 404 + 磁盘文件消失。这是本组用例的主断言。
func TestInstall_GoneAfterSetupAndFileDeleted(t *testing.T) {
	s, h := flowServer(t, nil)

	// 模拟一次正常启动：把安装页释放到数据目录。
	if err := s.PrepareInstallPage(); err != nil {
		t.Fatalf("释放安装页失败：%v", err)
	}
	pagePath := filepath.Join(s.installDir(), "index.html")
	if _, err := os.Stat(pagePath); err != nil {
		t.Fatalf("安装页应已释放到 %s：%v", pagePath, err)
	}

	// 走真实接口完成安装（不是直接改库），这样才会经过删除那一行。
	rec := doJSON(t, h, "POST", "/api/v1/auth/setup", map[string]string{
		"username": "installer", "password": "installer123",
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("安装应成功，实际 %d：%s", rec.Code, rec.Body.String())
	}

	// 文件层：磁盘上那份必须没了。
	if _, err := os.Stat(pagePath); !os.IsNotExist(err) {
		t.Fatalf("安装完成后安装页文件应被删除，实际 stat err=%v", err)
	}
	if _, err := os.Stat(s.installDir()); !os.IsNotExist(err) {
		t.Errorf("整个安装目录应被删除，实际 stat err=%v", err)
	}

	// 路由层：两个入口都必须 404。
	for _, p := range []string{"/install", "/install.js"} {
		rec = doJSON(t, h, "GET", p, nil, nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s 装完后应 404，实际 %d", p, rec.Code)
		}
		// 404 不能是 302：302 会让人以为地址还有效、只是被临时挪走了。
		if loc := rec.Header().Get("Location"); loc != "" {
			t.Errorf("%s 装完不该重定向，实际 Location=%q", p, loc)
		}
	}

	// 重复安装必须被拒，否则"入口关闭"只是视觉上的。
	rec = doJSON(t, h, "POST", "/api/v1/auth/setup", map[string]string{
		"username": "second", "password": "second1234",
	}, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("已初始化后再装应 404，实际 %d：%s", rec.Code, rec.Body.String())
	}

	// 装完之后 / 不再跳安装页，而是回到正常分流（此处未登录 → 官网）。
	rec = doJSON(t, h, "GET", "/", nil, nil)
	if rec.Code == http.StatusFound && strings.Contains(rec.Header().Get("Location"), "install") {
		t.Error("已初始化后 / 仍被导到 /install")
	}
}

// 释放必须幂等：每次重启都会跑一遍，不能因为目录里已有文件就报错。
func TestInstall_PrepareIsIdempotent(t *testing.T) {
	s, _ := flowServer(t, nil)
	for i := 0; i < 3; i++ {
		if err := s.PrepareInstallPage(); err != nil {
			t.Fatalf("第 %d 次释放失败：%v", i+1, err)
		}
	}
	entries, err := os.ReadDir(s.installDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 2 {
		t.Fatalf("应释放出 index.html 与 install.js，实际 %d 个文件", len(entries))
	}
}

// 已初始化时**不该**再释放：否则每重启一次就往数据目录里放一份死页面。
func TestInstall_NotPreparedWhenInitialized(t *testing.T) {
	s, _ := flowServer(t, nil)
	seedUserWithPW(t, s, "already", goodPW, "admin", "active")

	if err := s.PrepareInstallPage(); err != nil {
		t.Fatalf("释放失败：%v", err)
	}
	if _, err := os.Stat(s.installDir()); !os.IsNotExist(err) {
		t.Fatalf("已初始化时不该释放安装页，实际 stat err=%v", err)
	}
}

// 数据目录只读 / 释放失败时仍要能访问安装页（退回内嵌资源）。
// 少了这条兜底，一个只读挂载的数据目录会让整套部署装不起来。
func TestInstall_FallsBackToEmbeddedWhenDiskMissing(t *testing.T) {
	_, h := flowServer(t, func(c *config.Config) {
		// 指向一个不存在的目录：读盘必然失败。
		c.DataDir = filepath.Join(t.TempDir(), "nope")
	})
	rec := doJSON(t, h, "GET", "/install", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("磁盘上没有安装页时应退回内嵌资源，实际 %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "安装") {
		t.Fatal("退回的内嵌资源不是安装页")
	}
}

// 安装页里的嵌套路径必须被拒：路由层不会这么派，但多一层检查防的是
// 「哪天有人把 /* 也接到这个 handler 上」。
func TestInstall_AssetRejectsNestedPath(t *testing.T) {
	s, _ := flowServer(t, nil)
	for _, p := range []string{"/sub/install.js", "/../embed.go", "/./install.js"} {
		rec := httptest.NewRecorder()
		s.handleInstallAsset(rec, httptest.NewRequest("GET", p, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s 应 404，实际 %d", p, rec.Code)
		}
	}
}
