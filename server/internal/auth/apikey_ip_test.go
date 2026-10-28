package auth

import (
	"net/http/httptest"
	"testing"

	"docforge/server/internal/clientip"
	"docforge/server/internal/models"
)

// 这一组守两件事：
//  1. API Key 的「最近使用 IP」记的是真实来源，不是客户端自己写的头；
//  2. 会话有效期可配，且配了非法值时不会把所有人立刻踢下线。

func newAPIKeyFixture(t *testing.T, trusted []string) (*Manager, *models.APIKey, string) {
	t.Helper()
	m, u := newSessionFixture(t)
	rv, err := clientip.New(trusted)
	if err != nil {
		t.Fatalf("clientip.New: %v", err)
	}
	m.IPs = rv

	key := "df_testkey_for_unit_0001"
	ak := &models.APIKey{
		ID: "ak_1", UserID: u.ID, Name: "单测 Key", KeyHash: sha256Hex(key),
		Prefix: "df_test", QPSLimit: 100, MaxRecords: 100, Enabled: true,
	}
	if err := m.DB.Create(ak).Error; err != nil {
		t.Fatalf("create api key: %v", err)
	}
	return m, ak, key
}

func TestAPIKey_RecordsRealClientIP(t *testing.T) {
	// 只有 127.0.0.0/8 算可信代理，所以 203.0.113.5 这个"客户端"不可信，
	// 它自己写的 X-Forwarded-For 必须被丢掉。
	m, ak, key := newAPIKeyFixture(t, []string{"127.0.0.0/8"})

	r := httptest.NewRequest("GET", "/api/v1/jobs", nil)
	r.RemoteAddr = "203.0.113.5:41234"
	r.Header.Set("X-API-Key", key)
	r.Header.Set("X-Forwarded-For", "6.6.6.6")

	if p, code, _ := m.resolve(r); p == nil {
		t.Fatalf("API Key 鉴权应通过，实际被拒：%s", code)
	}

	var got models.APIKey
	if err := m.DB.First(&got, "id = ?", ak.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.LastUsedIP != "203.0.113.5" {
		t.Errorf("最近使用 IP 应为真实对端 203.0.113.5，实际 %q（记成转发头就等于把伪造值写进库里）", got.LastUsedIP)
	}
	if got.LastUsedAt == nil {
		t.Error("最近使用时间应同时被写入")
	}
}

func TestAPIKey_IPComesFromTrustedProxyChain(t *testing.T) {
	// 对端是可信代理时，转发头才算数——这是审计里能拿到真实客户端的前提。
	m, ak, key := newAPIKeyFixture(t, []string{"127.0.0.0/8"})

	r := httptest.NewRequest("GET", "/api/v1/jobs", nil)
	r.RemoteAddr = "127.0.0.1:41234"
	r.Header.Set("X-API-Key", key)
	r.Header.Set("X-Forwarded-For", "203.0.113.9")

	if p, _, _ := m.resolve(r); p == nil {
		t.Fatal("API Key 鉴权应通过")
	}

	var got models.APIKey
	if err := m.DB.First(&got, "id = ?", ak.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.LastUsedIP != "203.0.113.9" {
		t.Errorf("可信代理链上应取 XFF 里的客户端地址 203.0.113.9，实际 %q", got.LastUsedIP)
	}
}

func TestAPIKey_NoResolverLeavesIPEmpty(t *testing.T) {
	m, ak, key := newAPIKeyFixture(t, []string{"127.0.0.0/8"})
	m.IPs = nil // 模拟没注入解析器

	r := httptest.NewRequest("GET", "/api/v1/jobs", nil)
	r.RemoteAddr = "203.0.113.5:41234"
	r.Header.Set("X-API-Key", key)

	if p, _, _ := m.resolve(r); p == nil {
		t.Fatal("API Key 鉴权应通过")
	}

	var got models.APIKey
	if err := m.DB.First(&got, "id = ?", ak.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	// 留空而不是填 RemoteAddr：没有解析器就意味着"不知道该信哪个地址"，
	// 这时记一个看起来像真地址的值比留空更糟。
	if got.LastUsedIP != "" {
		t.Errorf("没有解析器时应留空，实际 %q", got.LastUsedIP)
	}
	if got.LastUsedAt == nil {
		t.Error("即使没有 IP，最近使用时间也应该更新")
	}
}

func TestSessionTTLFromConfig(t *testing.T) {
	m, u := newSessionFixture(t)

	m.Cfg.SessionTTLDays = 1
	if got := m.sessionTTL().Hours(); got != 24 {
		t.Errorf("配 1 天应得到 24 小时，实际 %.1f", got)
	}

	m.Cfg.SessionTTLDays = 30
	if got := m.sessionTTL().Hours(); got != 720 {
		t.Errorf("配 30 天应得到 720 小时，实际 %.1f", got)
	}

	_ = u

	// 配 0 或负数时回落到 7 天，而不是签发一张立刻过期的 cookie。
	// 后者会让用户看到「登录成功但一直未登录」，是最难查的那类故障。
	for _, bad := range []int{0, -1} {
		m.Cfg.SessionTTLDays = bad
		if got := m.sessionTTL().Hours(); got != 7*24 {
			t.Errorf("配 %d 天应回落到 168 小时，实际 %.1f", bad, got)
		}
	}
}

func TestSessionTTLAppliesToCookie(t *testing.T) {
	m, u := newSessionFixture(t)
	m.Cfg.SessionTTLDays = 2

	tok := issue(t, m, u)
	if tok == "" {
		t.Fatal("应签发 cookie")
	}

	var rec models.Session
	if err := m.DB.First(&rec).Error; err != nil {
		t.Fatalf("sessions 表应有记录：%v", err)
	}
	// 落库的过期时间必须跟配置走，否则 cookie 上写着 2 天、服务端 7 天后才放过期。
	want := 2 * 24 * 60 // 分钟
	gotMin := int(rec.ExpiresAt.Sub(rec.CreatedAt).Minutes())
	if gotMin < want-5 || gotMin > want+5 {
		t.Errorf("sessions.expires_at 应约 %d 分钟，实际 %d", want, gotMin)
	}
}
