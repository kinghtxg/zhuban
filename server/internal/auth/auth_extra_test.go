package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"docforge/server/internal/config"
	"docforge/server/internal/models"
	"docforge/server/internal/store"
)

// 这一组补的是「首装能不能进得去」「身份上下文有没有串」「锁定能不能解开」——
// 都是出问题时表现为「进不去系统」或「越权拿到了别人的身份」的地方。

// ---- 调用主体（context） ----

// PrincipalFrom 在没有主体时返回 nil 而不是 panic。
// 所有「可选登录」的路由（比如直链下载、公开预览）都会拿到 nil 的主体，
// 这里 panic 一次就是 500。
func TestPrincipalFrom_EmptyContextIsNil(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	if p := PrincipalFrom(r); p != nil {
		t.Fatalf("没有主体时应返回 nil，实际 %+v", p)
	}
}

// WithPrincipal / PrincipalFrom 必须原样取出同一个对象。
//
// 这里有个容易踩的坑：ctxPrincipal 是包内私有类型，中间件（api 包）无法直接拼 key，
// 只能靠这两个函数。一旦某一侧用了别的 key，取值永远是 nil —— 表现是所有接口 401，
// 但编译期毫无提示。
func TestPrincipalRoundTrip(t *testing.T) {
	u := &models.User{ID: "u_ctx", Username: "ctx", Role: "admin"}
	p := &Principal{User: u}
	req := httptest.NewRequest("GET", "/", nil).WithContext(WithPrincipal(context.Background(), p))

	got := PrincipalFrom(req)
	if got == nil || got.User == nil || got.User.ID != "u_ctx" || got.User.Role != "admin" {
		t.Fatalf("主体没有原样取出，实际 %+v", got)
	}
	if got != p {
		t.Error("应返回同一个指针，而非复制品")
	}
}

// ---- cookie 清理 ----

// 登出必须把 cookie 的过期时间设到过去并给负数 MaxAge。
// 只发一个空值不够：浏览器会把「同名空 cookie」当成有值，某些实现下反而覆盖不掉。
func TestClearCookie_ExpiresImmediately(t *testing.T) {
	m, _ := newSessionFixture(t)
	rec := httptest.NewRecorder()
	m.ClearCookie(rec)

	c := findCookie(t, rec, CookieName)
	if c.MaxAge >= 0 {
		t.Errorf("MaxAge 应为负数，实际 %d", c.MaxAge)
	}
	if !c.Expires.Before(time.Now()) {
		t.Errorf("Expires 应在过去，实际 %s", c.Expires)
	}
	if !c.HttpOnly {
		t.Error("会话 cookie 必须是 HttpOnly")
	}
	if c.Path != "/" {
		t.Errorf("Path 应为 /，否则子路径下的 cookie 删不掉，实际 %q", c.Path)
	}
}

// Secure 标志跟随 EXTERNAL_BASE_URL 的协议，而不是 r.TLS。
//
// 服务总在反向代理后面，容器内 r.TLS 恒为 nil；按它判断会让公网站点永久缺 Secure。
// 反过来内网 http 部署强行加 Secure，浏览器会直接丢弃 cookie，登录态建不起来。
func TestCookieSecureFollowsExternalBaseURL(t *testing.T) {
	cases := []struct{ base string; want bool }{
		{"https://zb.example.com", true},
		{"HTTPS://ZB.EXAMPLE.COM", true}, // 大小写不该影响判断
		{"http://192.168.1.10:18100", false},
		{"", false},
	}
	for _, c := range cases {
		m, _ := newSessionFixture(t)
		m.Cfg.ExternalBaseURL = c.base
		rec := httptest.NewRecorder()
		m.ClearCookie(rec)
		if got := findCookie(t, rec, CookieName).Secure; got != c.want {
			t.Errorf("EXTERNAL_BASE_URL=%q 时 Secure 应为 %v，实际 %v", c.base, c.want, got)
		}
	}
}

// ---- 首装与管理员提升 ----

func bootstrapFixture(t *testing.T, cfg *config.Config) (*Manager, func() int64) {
	t.Helper()
	dir := t.TempDir()
	cfg.DataDir = dir
	cfg.DatabaseURL = "sqlite://" + filepath.Join(dir, "b.db")
	if cfg.AuthSecret == "" {
		cfg.AuthSecret = "bootstrap-secret"
	}
	db, err := store.Open(cfg)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	count := func() int64 {
		var n int64
		db.Model(&models.User{}).Count(&n)
		return n
	}
	return New(db, cfg), count
}

// 配了 ADMIN_USERNAME/PASSWORD 时建出 active admin，且密码能用来登录。
func TestBootstrapAdmin_CreatesActiveAdmin(t *testing.T) {
	cfg := &config.Config{AdminUsername: "boss", AdminPassword: "Zhuban@2026Adm1n"}
	m, count := bootstrapFixture(t, cfg)

	BootstrapAdmin(m.DB, m.Cfg)

	if n := count(); n != 1 {
		t.Fatalf("应创建 1 个用户，实际 %d", n)
	}
	var u models.User
	if err := m.DB.First(&u, "username = ?", "boss").Error; err != nil {
		t.Fatalf("管理员没建出来：%v", err)
	}
	if u.Role != "admin" || u.Status != "active" {
		t.Errorf("内置管理员应是 active/admin，实际 %s/%s", u.Status, u.Role)
	}
	if !u.HasPassword() {
		t.Error("内置管理员必须带密码哈希，否则密码登录进不去")
	}
	if ok, err := VerifyPassword("Zhuban@2026Adm1n", u.PasswordHash); err != nil || !ok {
		t.Errorf("存下来的哈希校验不过：ok=%v err=%v", ok, err)
	}
}

// 已有用户时不重复建号：重启服务不能把管理员再来一遍（用户名唯一会直接冲突）。
func TestBootstrapAdmin_IdempotentWhenUsersExist(t *testing.T) {
	cfg := &config.Config{AdminUsername: "boss", AdminPassword: "Zhuban@2026Adm1n"}
	m, count := bootstrapFixture(t, cfg)

	BootstrapAdmin(m.DB, m.Cfg)
	BootstrapAdmin(m.DB, m.Cfg)

	if n := count(); n != 1 {
		t.Fatalf("重复启动应保持 1 个用户，实际 %d", n)
	}
}

// 没配账号密码时什么都不做，等页面初始化向导。
// 这里必须留空库：随便造一个账号会把「初始化向导」永久跳过，
// 而那个向导才是设置真实管理员的地方。
func TestBootstrapAdmin_NoConfigLeavesEmpty(t *testing.T) {
	cfg := &config.Config{}
	m, count := bootstrapFixture(t, cfg)

	BootstrapAdmin(m.DB, m.Cfg)

	if n := count(); n != 0 {
		t.Fatalf("未配置时不该建号，实际建了 %d 个", n)
	}
	if m.Initialized() {
		t.Error("空库应报告未初始化，否则前端不会引导去创建首个管理员")
	}
}

// 名单里的账号要被提升；名单外的 admin 不能被降级。
//
// 只提升不降级是刻意的：改配置时手抖删掉一行，不该把正在用的管理员打回成员。
func TestEnsureAdminUsernames_PromoteOnly(t *testing.T) {
	cfg := &config.Config{AdminUsernames: []string{"zhangsan", "lisi", "notyet"}}
	m, _ := bootstrapFixture(t, cfg)
	m.DB.Create(&models.User{ID: "u1", Username: "zhangsan", Role: "member", Status: "active"})
	m.DB.Create(&models.User{ID: "u2", Username: "lisi", Role: "admin", Status: "active"})
	m.DB.Create(&models.User{ID: "u3", Username: "wangwu", Role: "admin", Status: "active"})
	m.DB.Create(&models.User{ID: "u4", Username: "zhaoliu", Role: "member", Status: "pending"})

	EnsureAdminUsernames(m.DB, m.Cfg)

	var zs models.User
	m.DB.First(&zs, "id = ?", "u1")
	if zs.Role != "admin" {
		t.Errorf("名单内的成员应被提升为 admin，实际 %s", zs.Role)
	}
	var ww models.User
	m.DB.First(&ww, "id = ?", "u3")
	if ww.Role != "admin" {
		t.Errorf("名单外的 admin 不该被降级，实际 %s", ww.Role)
	}
	var zl models.User
	m.DB.First(&zl, "id = ?", "u4")
	if zl.Role != "member" || zl.Status != "pending" {
		t.Errorf("不在名单里的账号不该被动，实际 %s/%s", zl.Status, zl.Role)
	}
	// notyet 还没登录过 → 不该凭空建号
	var cnt int64
	m.DB.Model(&models.User{}).Where("username = ?", "notyet").Count(&cnt)
	if cnt != 0 {
		t.Error("名单里尚未登录的账号不该被预创建")
	}
}

// 空名单时整段跳过，不能把所有人的角色刷一遍。
func TestEnsureAdminUsernames_EmptyListNoop(t *testing.T) {
	cfg := &config.Config{}
	m, _ := bootstrapFixture(t, cfg)
	m.DB.Create(&models.User{ID: "u1", Username: "a", Role: "member", Status: "active"})
	EnsureAdminUsernames(m.DB, m.Cfg)

	var u models.User
	m.DB.First(&u, "id = ?", "u1")
	if u.Role != "member" {
		t.Errorf("空名单不该改任何角色，实际 %s", u.Role)
	}
}

// PromoteIfListed 给「首次登录才建号」的人补一次提升，并同步内存里的对象。
// 不同步的话，紧接着的 IssueCookie 会把旧角色写进会话载荷，用户要重新登录才生效。
func TestPromoteIfListed_AffectsReturnedUser(t *testing.T) {
	cfg := &config.Config{AdminUsernames: []string{"newbie"}}
	m, _ := bootstrapFixture(t, cfg)
	u := &models.User{ID: "u_new", Username: "newbie", Role: "member", Status: "pending"}
	m.DB.Create(u)

	PromoteIfListed(m.DB, m.Cfg, u)

	if u.Role != "admin" || u.Status != "active" {
		t.Errorf("内存对象也要同步成 admin/active，实际 %s/%s", u.Status, u.Role)
	}
	var got models.User
	m.DB.First(&got, "id = ?", "u_new")
	if got.Role != "admin" || got.Status != "active" {
		t.Errorf("库里也要提升，实际 %s/%s", got.Status, got.Role)
	}
	// 已经在名单里且是 admin：再调一次不该报错
	PromoteIfListed(m.DB, m.Cfg, u)
	if u.Role != "admin" {
		t.Error("重复调用应保持 admin")
	}
	// nil 与不在名单里的都要安全返回
	PromoteIfListed(m.DB, m.Cfg, nil)
	other := &models.User{ID: "u_other", Username: "other", Role: "member"}
	PromoteIfListed(m.DB, m.Cfg, other)
	if other.Role != "member" {
		t.Error("不在名单里的账号不该被提升")
	}
}

// ---- 锁定与限流 ----

// 连续失败到阈值后锁定，锁定期间即使密码正确也进不来（防在线爆破）。
// 关键是 Clear 要能真正解开：登录成功后如果计数没清，用户会在「输对密码」的第 5 次
// 被自己上一次的手滑锁死。
func TestLoginGuard_LockAndClear(t *testing.T) {
	g := NewLoginGuard()
	const user = "zhangsan"

	if g.LockedRemaining(user) != 0 {
		t.Fatal("初始状态不该锁定")
	}
	for i := 1; i < maxFails; i++ {
		g.RecordFailure(user)
		if g.LockedRemaining(user) != 0 {
			t.Fatalf("第 %d 次失败不该触发锁定（阈值 %d）", i, maxFails)
		}
	}
	g.RecordFailure(user)
	if g.LockedRemaining(user) <= 0 {
		t.Fatalf("%d 次失败后应进入锁定", maxFails)
	}
	// 其他账号不受影响
	if g.LockedRemaining("lisi") != 0 {
		t.Error("锁定应按账号隔离，不能牵连别人")
	}

	g.Clear(user)
	if g.LockedRemaining(user) != 0 {
		t.Error("Clear 之后必须解锁，否则输对密码也进不去")
	}
}

// IP 限流：窗口内计数，超限后拒绝；不同 IP 互不影响。
func TestLoginGuard_IPLimit(t *testing.T) {
	g := NewLoginGuard()
	ip := "203.0.113.9"
	for i := 0; i < ipLimitPerMin; i++ {
		if !g.AllowIP(ip) {
			t.Fatalf("第 %d 次请求不该被限流（上限 %d）", i+1, ipLimitPerMin)
		}
	}
	if g.AllowIP(ip) {
		t.Fatalf("超过 %d 次后应被限流", ipLimitPerMin)
	}
	if !g.AllowIP("198.51.100.7") {
		t.Error("限流应按 IP 隔离，不能因为一个 IP 被刷就全站拒绝")
	}
}

// ---- dev 模式 ----

// X-Dev-User 只在 AUTH_DEV=true 时生效。这是本系统最危险的一个开关：
// 打开后任何人带一个请求头就是管理员。所以两种情况都要钉住。
func TestDevPrincipalOnlyInDevMode(t *testing.T) {
	cfg := &config.Config{AuthDev: true}
	m, count := bootstrapFixture(t, cfg)
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Dev-User", "tester")

	p, code, _ := m.resolve(req)

	if code != "" || p == nil || p.User == nil {
		t.Fatalf("dev 模式下应凭请求头登录成功，实际 code=%q principal=%+v", code, p)
	}
	if p.User.Role != "admin" {
		t.Errorf("dev 用户应是 admin，实际 %s", p.User.Role)
	}
	if count() != 1 {
		t.Errorf("首次应建出 1 个 dev 账号，实际 %d", count())
	}

	// 再请求一次：复用同一个账号，不重复建
	p2, _, _ := m.resolve(req)
	if p2 == nil || p2.User.ID != p.User.ID {
		t.Error("同一个 dev 用户名应复用账号")
	}
	if count() != 1 {
		t.Errorf("第二次请求不该再建号，实际 %d 个用户", count())
	}
}

func TestDevHeaderIgnoredWhenAuthDevOff(t *testing.T) {
	cfg := &config.Config{AuthDev: false}
	m, _ := bootstrapFixture(t, cfg)
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Dev-User", "tester")

	p, code, _ := m.resolve(req)

	if p != nil {
		t.Fatalf("生产模式下 X-Dev-User 必须被忽略，实际拿到了 %+v", p.User)
	}
	if code != "unauthorized" {
		t.Errorf("应返回 unauthorized，实际 %q", code)
	}
}

func findCookie(t *testing.T, rec *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("响应里没有 cookie %q", name)
	return nil
}

// ---------- 内存表不会被外部输入打满 ----------

// 这两张表都按外部可控的键建（提交的用户名、来源 IP），
// 且登录接口不需要登录态就能打。一次请求一个条目看起来很小，
// 但没有上限就是「拿随机用户名连几百次登录」把进程内存耗光。
func TestLoginGuard_MapsAreBounded(t *testing.T) {
	g := NewLoginGuard()

	// 故意超过上限，模拟被人拿随机用户名刷。
	for i := 0; i < guardMaxEntries+500; i++ {
		g.RecordFailure(fmt.Sprintf("bot_%d", i))
	}
	if n := len(g.fails); n > guardMaxEntries+1 {
		t.Errorf("失败记录表应被清扫到上限附近，实际 %d 条（上限 %d）", n, guardMaxEntries)
	}

	// 每个 IP 只打一次，全部落在 1 分钟窗口内，因此都算"活跃"，
	// 清扫后仍可能看到较多条目——这里只断言不会无限增长。
	for i := 0; i < guardMaxEntries+500; i++ {
		g.AllowIP(fmt.Sprintf("10.0.%d.%d", i/256, i%256))
	}
	if n := len(g.ips); n > guardMaxEntries+1 {
		t.Errorf("IP 频次表应被清扫到上限附近，实际 %d 条（上限 %d）", n, guardMaxEntries)
	}
}

// 清扫绝不能把「正在锁定中」的账号放出来。
// 这条比上一组更重要：错误地回收锁定记录 = 给正在爆破的人当场解锁。
func TestLoginGuard_SweepKeepsLockedAccount(t *testing.T) {
	g := NewLoginGuard()
	for i := 0; i < maxFails; i++ {
		g.RecordFailure("victim")
	}
	if g.LockedRemaining("victim") <= 0 {
		t.Fatal("前置条件不成立：victim 应已被锁定")
	}

	// 制造大量过期记录以触发清扫。
	for i := 0; i < guardMaxEntries+10; i++ {
		g.RecordFailure(fmt.Sprintf("filler_%d", i))
	}
	if g.LockedRemaining("victim") <= 0 {
		t.Error("清扫把锁定中的账号删掉了——等于给爆破方自动解锁")
	}
}

// IP 记录在窗口滑过之后应被回收：它们已经不可能再影响限流判断。
func TestLoginGuard_SweepDropsIdleIPs(t *testing.T) {
	g := NewLoginGuard()
	stale := time.Now().Add(-5 * time.Minute)
	g.mu.Lock()
	g.ips["1.2.3.4"] = &ipRecord{counts: []time.Time{stale, stale.Add(time.Second)}}
	// 塞满其它 IP 以超过上限、触发清扫
	for i := 0; i < guardMaxEntries; i++ {
		g.ips[fmt.Sprintf("9.9.%d.%d", i/256, i%256)] = &ipRecord{counts: []time.Time{stale}}
	}
	g.mu.Unlock()

	g.AllowIP("1.2.3.4") // 触发一次清扫

	g.mu.Lock()
	_, still := g.ips["1.2.3.4"]
	g.mu.Unlock()
	// 1.2.3.4 在这次调用里被重新写入（它自己也过期了），所以判断依据是：
	// 旧的那批 9.9.x.x 应当已被清掉。
	if still {
		var others int
		g.mu.Lock()
		for k := range g.ips {
			if strings.HasPrefix(k, "9.9.") {
				others++
			}
		}
		g.mu.Unlock()
		if others > 0 {
			t.Errorf("空闲 IP 记录应被回收，仍有 %d 条", others)
		}
	}
}
