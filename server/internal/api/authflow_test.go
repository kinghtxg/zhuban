package api

// 登录 / 注册 / 改密 / 管理员管理用户 —— 这四条链路的端到端测试。
//
// 和 authmatrix_test.go 分工不同：那一组只证明「没登录进不去」，这一组测的是
// 「进去之后」。而权限与凭证的流转恰恰是最容易静默出错的地方：
//
//   - 禁用账号后旧 cookie 还灵不灵？（接口都返回 200，只有拿旧 cookie 再敲一次才知道）
//   - 改密码有没有把别的设备踢掉？（只换哈希等于半个动作）
//   - 登录失败 5 次到底锁不锁？锁定期间拿对的密码能不能进？
//   - 用户名不存在与密码错误，响应是否完全一致？（不一致就可以拿来枚举账号）
//
// 这些用例不做「函数被调用了」这种断言，一律看最终状态：库里的列、cookie 还能不能换到 200。

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"docforge/server/internal/auth"
	"docforge/server/internal/config"
	"docforge/server/internal/models"
	"docforge/server/internal/render"
)

// goodPW 满足 ValidatePasswordRule：≥8 位且字母数字都有。
// 写死一个常量而不是每个用例自己拼，是为了避免出现「改密成功但登录失败」
// 的时候分不清是产品问题还是用例里的密码不合规。
const goodPW = "Passw0rd!2026"

func flowServer(t *testing.T, mutate func(*config.Config)) (*Server, http.Handler) {
	t.Helper()
	s, _ := setupFullServer(t, mutate)
	return s, buildRouter(s)
}

// doJSON 发一个请求。body 传 string 时原样发出（用于「请求体不是合法 JSON」这类用例），
// 其余走 json.Marshal。
func doJSON(t *testing.T, h http.Handler, method, path string, body interface{}, ck *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	switch v := body.(type) {
	case nil:
	case string:
		rdr = strings.NewReader(v)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("序列化请求体失败：%v", err)
		}
		rdr = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	if ck != nil {
		req.AddCookie(ck)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// cookieOf 取出响应里那颗会话 cookie。
func cookieOf(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.CookieName && c.Value != "" {
			return c
		}
	}
	t.Fatalf("响应里没有 %s，状态码 %d，body=%s", auth.CookieName, rec.Code, rec.Body.String())
	return nil
}

type errResp struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Details []struct {
			Path    string `json:"path"`
			Message string `json:"message"`
		} `json:"details"`
	} `json:"error"`
}

func decodeErr(t *testing.T, rec *httptest.ResponseRecorder) errResp {
	t.Helper()
	var e errResp
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("错误响应不是合法 JSON（状态码 %d）：%s", rec.Code, rec.Body.String())
	}
	return e
}

// seedUser 直接往库里造一个账号。绕过注册接口是为了让每个用例的初始条件
// 显式可见（尤其 role/status），而不是靠「先注册再审核」这一串动作去凑。
func seedUserWithPW(t *testing.T, s *Server, username, pw, role, status string) *models.User {
	t.Helper()
	hash := ""
	if pw != "" {
		h, err := auth.HashPassword(pw)
		if err != nil {
			t.Fatalf("生成口令哈希失败：%v", err)
		}
		hash = h
	}
	u := &models.User{
		ID: "u_" + username, Username: username, PasswordHash: hash,
		DisplayName: username, Role: role, Status: status, Source: "local",
	}
	if err := s.DB.Create(u).Error; err != nil {
		t.Fatalf("造用户 %s 失败：%v", username, err)
	}
	return u
}

// loginAdmin 造一个 admin 并登录，返回它的 cookie。
func loginAdmin(t *testing.T, s *Server, h http.Handler) (*models.User, *http.Cookie) {
	t.Helper()
	u := seedUserWithPW(t, s, "boss", goodPW, "admin", "active")
	rec := doJSON(t, h, "POST", "/api/v1/auth/login", map[string]string{"username": "boss", "password": goodPW}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("管理员登录失败：%d %s", rec.Code, rec.Body.String())
	}
	return u, cookieOf(t, rec)
}

// ---------- setup ----------

func TestSetupStatus_ReflectsRuntimeNotConfig(t *testing.T) {
	s, h := flowServer(t, func(c *config.Config) {
		// 配置里写了开启注册，但 initialized / oidc 仍要看运行时真实状态。
		c.AllowRegister = true
	})
	var got map[string]interface{}
	rec := doJSON(t, h, "GET", "/api/v1/auth/setup-status", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 %d", rec.Code)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["initialized"] != false {
		t.Errorf("空库时 initialized 应为 false，实际 %v", got["initialized"])
	}
	if got["registration_enabled"] != true {
		t.Errorf("registration_enabled 应跟随配置为 true，实际 %v", got["registration_enabled"])
	}
	// OIDC 没配置时必须是 false：登录页据此决定要不要画那个按钮。
	// 如果这里跟着 Cfg.OIDCEnabled 走，用户会看到一个点下去必 500 的按钮。
	if got["oidc_enabled"] != false {
		t.Errorf("OIDC 未配置时 oidc_enabled 应为 false，实际 %v", got["oidc_enabled"])
	}

	seedUserWithPW(t, s, "someone", goodPW, "member", "active")
	rec = doJSON(t, h, "GET", "/api/v1/auth/setup-status", nil, nil)
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["initialized"] != true {
		t.Errorf("已有用户后 initialized 应为 true，实际 %v", got["initialized"])
	}
}

func TestSetup_CreatesAdminThenClosesItself(t *testing.T) {
	s, h := flowServer(t, nil)
	rec := doJSON(t, h, "POST", "/api/v1/auth/setup", map[string]string{
		"username": "root", "password": goodPW,
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("首次初始化应成功，实际 %d %s", rec.Code, rec.Body.String())
	}
	ck := cookieOf(t, rec)

	var u models.User
	if err := s.DB.First(&u, "username = ?", "root").Error; err != nil {
		t.Fatalf("管理员没落库：%v", err)
	}
	if u.Role != "admin" || u.Status != "active" {
		t.Errorf("首个账号应为 admin/active，实际 %s/%s", u.Role, u.Status)
	}

	// 第二次必须 404（不是 403）：这个接口在初始化完成后就该「不存在」，
	// 403 等于告诉外面"这里有东西，只是你不能用"。
	rec = doJSON(t, h, "POST", "/api/v1/auth/setup", map[string]string{
		"username": "root2", "password": goodPW,
	}, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("重复初始化应 404，实际 %d", rec.Code)
	}

	// 刚建出来的账号必须能直接用 cookie 调到 /auth/me。
	rec = doJSON(t, h, "GET", "/api/v1/auth/me", nil, ck)
	if rec.Code != http.StatusOK {
		t.Errorf("初始化后签发的 cookie 应可用，实际 %d %s", rec.Code, rec.Body.String())
	}
	// 也能用口令重新登录。
	rec = doJSON(t, h, "POST", "/api/v1/auth/login", map[string]string{"username": "root", "password": goodPW}, nil)
	if rec.Code != http.StatusOK {
		t.Errorf("初始化创建的账号应能用口令登录，实际 %d %s", rec.Code, rec.Body.String())
	}
}

func TestSetup_RejectsBadInput(t *testing.T) {
	_, h := flowServer(t, nil)
	cases := []struct {
		name    string
		body    interface{}
		status  int
		field   string
	}{
		{"用户名太短", map[string]string{"username": "ab", "password": goodPW}, http.StatusUnprocessableEntity, "username"},
		{"密码太短", map[string]string{"username": "root", "password": "a1"}, http.StatusUnprocessableEntity, "password"},
		{"密码纯字母", map[string]string{"username": "root", "password": "abcdefgh"}, http.StatusUnprocessableEntity, "password"},
		{"请求体不是 JSON", "{{{", http.StatusBadRequest, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := doJSON(t, h, "POST", "/api/v1/auth/setup", c.body, nil)
			if rec.Code != c.status {
				t.Fatalf("应 %d，实际 %d %s", c.status, rec.Code, rec.Body.String())
			}
			if c.field == "" {
				return
			}
			// 字段级错误必须指到具体字段：前端要拿 path 去高亮那一格。
			e := decodeErr(t, rec)
			if len(e.Error.Details) == 0 || e.Error.Details[0].Path != c.field {
				t.Errorf("details 应指向 %s，实际 %+v", c.field, e.Error.Details)
			}
		})
	}
}

// ---------- 登录 ----------

func TestLogin_UnknownUserAndWrongPasswordLookIdentical(t *testing.T) {
	s, h := flowServer(t, nil)
	seedUserWithPW(t, s, "alice", goodPW, "member", "active")

	a := doJSON(t, h, "POST", "/api/v1/auth/login", map[string]string{"username": "alice", "password": "wrong-one"}, nil)
	b := doJSON(t, h, "POST", "/api/v1/auth/login", map[string]string{"username": "nobody", "password": "wrong-one"}, nil)

	if a.Code != http.StatusUnauthorized || b.Code != http.StatusUnauthorized {
		t.Fatalf("两种情况都应 401，实际 %d / %d", a.Code, b.Code)
	}
	ea, eb := decodeErr(t, a), decodeErr(t, b)
	// 状态码相同还不够：code 或 message 只要有差别，就能拿来判断「这个用户名是否存在」。
	if ea.Error.Code != eb.Error.Code || ea.Error.Message != eb.Error.Message {
		t.Errorf("响应不一致会暴露账号是否存在：%+v vs %+v", ea.Error, eb.Error)
	}
}

func TestLogin_DisabledAndPendingAreBlockedWithDistinctCodes(t *testing.T) {
	s, h := flowServer(t, nil)
	seedUserWithPW(t, s, "gone", goodPW, "member", "disabled")
	seedUserWithPW(t, s, "wait", goodPW, "member", "pending")

	rec := doJSON(t, h, "POST", "/api/v1/auth/login", map[string]string{"username": "gone", "password": goodPW}, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("禁用账号应 403，实际 %d", rec.Code)
	}
	if c := decodeErr(t, rec).Error.Code; c != "forbidden" {
		t.Errorf("禁用账号的 code 应为 forbidden，实际 %q", c)
	}

	rec = doJSON(t, h, "POST", "/api/v1/auth/login", map[string]string{"username": "wait", "password": goodPW}, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("待审核账号应 403，实际 %d", rec.Code)
	}
	// pending 与 forbidden 必须分开：前端提示语完全不一样
	// （「等审核」vs「找管理员」），混成一个用户就不知道该做什么。
	if c := decodeErr(t, rec).Error.Code; c != "pending" {
		t.Errorf("待审核账号的 code 应为 pending，实际 %q", c)
	}
}

func TestLogin_LocksAfterFiveFailuresAndIgnoresCorrectPassword(t *testing.T) {
	s, h := flowServer(t, nil)
	seedUserWithPW(t, s, "bob", goodPW, "member", "active")

	for i := 0; i < 5; i++ {
		rec := doJSON(t, h, "POST", "/api/v1/auth/login", map[string]string{"username": "bob", "password": "bad"}, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次失败应 401，实际 %d", i+1, rec.Code)
		}
	}
	// 锁定期间即使口令正确也必须被拒，否则暴力破解只需要"猜中前先撞一次对的"。
	rec := doJSON(t, h, "POST", "/api/v1/auth/login", map[string]string{"username": "bob", "password": goodPW}, nil)
	if rec.Code != http.StatusLocked {
		t.Fatalf("锁定期间应 423，实际 %d %s", rec.Code, rec.Body.String())
	}
	e := decodeErr(t, rec)
	if e.Error.Code != "account_locked" {
		t.Errorf("code 应为 account_locked，实际 %q", e.Error.Code)
	}
	// 提示里要带剩余秒数：只写"已锁定"的话，用户会一两秒后重试，然后再被拒。
	if !strings.Contains(e.Error.Message, "秒") {
		t.Errorf("锁定提示应给出剩余时间，实际 %q", e.Error.Message)
	}
}

func TestLogin_SuccessClearsFailureCounterAndStampsLastLogin(t *testing.T) {
	s, h := flowServer(t, nil)
	seedUserWithPW(t, s, "carol", goodPW, "member", "active")

	for i := 0; i < 4; i++ {
		doJSON(t, h, "POST", "/api/v1/auth/login", map[string]string{"username": "carol", "password": "bad"}, nil)
	}
	rec := doJSON(t, h, "POST", "/api/v1/auth/login", map[string]string{"username": "carol", "password": goodPW}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("第 5 次用正确口令应成功，实际 %d %s", rec.Code, rec.Body.String())
	}
	// 计数必须清零，否则「成功登录过一次」之后的 4 次误输入会直接把人锁掉。
	if left := s.Auth.Guard.LockedRemaining("carol"); left > 0 {
		t.Errorf("登录成功后失败计数应清零，实际仍锁定 %d 秒", left)
	}

	var u models.User
	s.DB.First(&u, "username = ?", "carol")
	if u.LastLoginAt == nil {
		t.Error("登录成功后 last_login_at 必须落库，否则用户列表里永远显示「从未登录」")
	}
}

func TestLogin_PromotesUsernameInAdminList(t *testing.T) {
	s, h := flowServer(t, func(c *config.Config) {
		c.AdminUsernames = []string{"dave"}
	})
	seedUserWithPW(t, s, "dave", goodPW, "member", "active")

	rec := doJSON(t, h, "POST", "/api/v1/auth/login", map[string]string{"username": "dave", "password": goodPW}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("登录失败：%d %s", rec.Code, rec.Body.String())
	}
	// 名单里的账号登录即提升，且响应里就应该是 admin——
	// 前端拿这个字段决定要不要渲染管理入口，晚一步生效会让用户以为没权限。
	var got struct {
		User struct {
			Role string `json:"role"`
		} `json:"user"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.User.Role != "admin" {
		t.Errorf("名单里的账号登录后响应应为 admin，实际 %q", got.User.Role)
	}
	var u models.User
	s.DB.First(&u, "username = ?", "dave")
	if u.Role != "admin" {
		t.Errorf("role 应落库为 admin，实际 %q", u.Role)
	}
}

// ---------- 注册 / 登出 ----------

func TestRegister_ClosedByDefaultThenPendingUntilApproved(t *testing.T) {
	s, h := flowServer(t, nil)
	body := map[string]string{"username": "erin", "password": goodPW, "display_name": "艾琳"}

	rec := doJSON(t, h, "POST", "/api/v1/auth/register", body, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("默认应关闭注册（404），实际 %d %s", rec.Code, rec.Body.String())
	}

	// 打开注册后重来一次。
	s.Cfg.AllowRegister = true
	rec = doJSON(t, h, "POST", "/api/v1/auth/register", body, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("开启后应能注册，实际 %d %s", rec.Code, rec.Body.String())
	}
	var u models.User
	if err := s.DB.First(&u, "username = ?", "erin").Error; err != nil {
		t.Fatalf("注册没落库：%v", err)
	}
	if u.Status != "pending" || u.Role != "member" {
		t.Errorf("自助注册应落成 pending/member，实际 %s/%s", u.Status, u.Role)
	}

	// 重复注册要 409，而不是再建一个同名账号（用户名唯一索引会拦，但要给 409 不是 500）。
	rec = doJSON(t, h, "POST", "/api/v1/auth/register", body, nil)
	if rec.Code != http.StatusConflict {
		t.Errorf("重复注册应 409，实际 %d %s", rec.Code, rec.Body.String())
	}

	// 审核前登不进去。
	rec = doJSON(t, h, "POST", "/api/v1/auth/login", map[string]string{"username": "erin", "password": goodPW}, nil)
	if rec.Code != http.StatusForbidden {
		t.Errorf("待审核账号应 403，实际 %d", rec.Code)
	}

	// 管理员审核通过之后就能登录了。
	_, admin := loginAdmin(t, s, h)
	rec = doJSON(t, h, "POST", "/api/v1/admin/users/"+u.ID+"/approve", nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("审核接口失败：%d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, h, "POST", "/api/v1/auth/login", map[string]string{"username": "erin", "password": goodPW}, nil)
	if rec.Code != http.StatusOK {
		t.Errorf("审核通过后应能登录，实际 %d %s", rec.Code, rec.Body.String())
	}
}

func TestLogout_RevokesSessionNotJustCookie(t *testing.T) {
	s, h := flowServer(t, nil)
	seedUserWithPW(t, s, "frank", goodPW, "member", "active")
	rec := doJSON(t, h, "POST", "/api/v1/auth/login", map[string]string{"username": "frank", "password": goodPW}, nil)
	ck := cookieOf(t, rec)

	if rec = doJSON(t, h, "GET", "/api/v1/auth/me", nil, ck); rec.Code != http.StatusOK {
		t.Fatalf("登录后 /auth/me 应 200，实际 %d", rec.Code)
	}
	if rec = doJSON(t, h, "POST", "/api/v1/auth/logout", nil, ck); rec.Code != http.StatusOK {
		t.Fatalf("登出应 200，实际 %d", rec.Code)
	}
	// 关键点：拿着退出前复制出来的那颗 cookie 再敲，必须失效。
	// 只清浏览器的 cookie 而不撤服务端会话，等于"退出"只是本地动作，
	// 被复制走的那份仍然是能用的门票。
	rec = doJSON(t, h, "GET", "/api/v1/auth/me", nil, ck)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("登出后旧 cookie 应失效（401），实际 %d %s", rec.Code, rec.Body.String())
	}
}

// ---------- 改密 ----------

func TestPassword_RequiresOldPasswordAndRevokesOtherDevices(t *testing.T) {
	s, h := flowServer(t, nil)
	seedUserWithPW(t, s, "grace", goodPW, "member", "active")

	// 两台设备各登录一次。
	ckA := cookieOf(t, doJSON(t, h, "POST", "/api/v1/auth/login", map[string]string{"username": "grace", "password": goodPW}, nil))
	ckB := cookieOf(t, doJSON(t, h, "POST", "/api/v1/auth/login", map[string]string{"username": "grace", "password": goodPW}, nil))

	// 原密码错：必须拒。
	rec := doJSON(t, h, "POST", "/api/v1/auth/password", map[string]string{"old_password": "nope", "new_password": "NewPassw0rd!"}, ckA)
	if rec.Code != http.StatusBadRequest || decodeErr(t, rec).Error.Code != "invalid_credentials" {
		t.Fatalf("原密码错误应 400 invalid_credentials，实际 %d %s", rec.Code, rec.Body.String())
	}

	// 新密码太弱：422，且不能改。
	rec = doJSON(t, h, "POST", "/api/v1/auth/password", map[string]string{"old_password": goodPW, "new_password": "123"}, ckA)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("弱口令应 422，实际 %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, h, "POST", "/api/v1/auth/password", map[string]string{"old_password": goodPW, "new_password": "NewPassw0rd!"}, ckA)
	if rec.Code != http.StatusOK {
		t.Fatalf("改密应成功，实际 %d %s", rec.Code, rec.Body.String())
	}

	// 当前这台的登录态要保住：否则用户改完密码立刻被弹回登录页，会当成改密失败。
	if rec = doJSON(t, h, "GET", "/api/v1/auth/me", nil, ckA); rec.Code != http.StatusOK {
		t.Errorf("改密后当前会话不应失效，实际 %d", rec.Code)
	}
	// 另一台必须被踢掉。
	if rec = doJSON(t, h, "GET", "/api/v1/auth/me", nil, ckB); rec.Code != http.StatusUnauthorized {
		t.Errorf("改密后其它设备的会话应失效（401），实际 %d", rec.Code)
	}
	// 新口令可登录、旧口令不可登录。
	if rec = doJSON(t, h, "POST", "/api/v1/auth/login", map[string]string{"username": "grace", "password": "NewPassw0rd!"}, nil); rec.Code != http.StatusOK {
		t.Errorf("新口令应能登录，实际 %d", rec.Code)
	}
	if rec = doJSON(t, h, "POST", "/api/v1/auth/login", map[string]string{"username": "grace", "password": goodPW}, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("旧口令应已失效，实际 %d", rec.Code)
	}
}

func TestMe_HidesSecretsAndReportsMode(t *testing.T) {
	s, h := flowServer(t, nil)
	seedUserWithPW(t, s, "heidi", goodPW, "member", "active")
	ck := cookieOf(t, doJSON(t, h, "POST", "/api/v1/auth/login", map[string]string{"username": "heidi", "password": goodPW}, nil))

	rec := doJSON(t, h, "GET", "/api/v1/auth/me", nil, ck)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 %d", rec.Code)
	}
	raw := rec.Body.String()
	// 口令哈希绝不能出现在任何响应里。这条看起来"显然"，但 publicUserFull 是
	// 用 map 手拼的——加字段时顺手把整个结构体塞进去就会漏。
	if strings.Contains(raw, "password_hash") || strings.Contains(raw, "$argon2") {
		t.Errorf("/auth/me 泄露了口令哈希：%s", raw)
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["auth_mode"] != "local" {
		t.Errorf("本地账号 auth_mode 应为 local，实际 %v", got["auth_mode"])
	}
	if got["initialized"] != true {
		t.Errorf("已登录时 initialized 应为 true，实际 %v", got["initialized"])
	}
	u, ok := got["user"].(map[string]interface{})
	if !ok {
		t.Fatalf("响应缺少 user 对象：%s", raw)
	}
	for _, k := range []string{"id", "username", "role", "status", "source", "has_password", "oidc_bound"} {
		if _, ok := u[k]; !ok {
			t.Errorf("user 缺少字段 %s（契约 §4.2）：%v", k, u)
		}
	}
}

// ---------- 管理员管理用户 ----------

func TestAdminUserOps_HappyPath(t *testing.T) {
	s, h := flowServer(t, nil)
	_, admin := loginAdmin(t, s, h)

	// 新建用户
	rec := doJSON(t, h, "POST", "/api/v1/admin/users", map[string]string{
		"username": "ivan", "password": goodPW, "role": "member",
	}, admin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("新建用户应 201，实际 %d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID     string `json:"id"`
		Role   string `json:"role"`
		Status string `json:"status"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if created.Role != "member" || created.Status != "active" {
		t.Errorf("管理员新建的用户应为 member/active，实际 %s/%s", created.Role, created.Status)
	}

	// 详情列表里能查到，且分页字段齐。
	rec = doJSON(t, h, "GET", "/api/v1/admin/users?q=ivan", nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("用户列表失败：%d", rec.Code)
	}
	var list struct {
		Total int `json:"total"`
		Items []struct {
			Username string `json:"username"`
		} `json:"items"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if list.Total != 1 || len(list.Items) != 1 || list.Items[0].Username != "ivan" {
		t.Errorf("按关键字过滤应只命中 ivan，实际 total=%d items=%+v", list.Total, list.Items)
	}

	// 提为管理员
	rec = doJSON(t, h, "POST", "/api/v1/admin/users/"+created.ID+"/role", map[string]string{"role": "admin"}, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("改角色失败：%d %s", rec.Code, rec.Body.String())
	}
	var u models.User
	s.DB.First(&u, "id = ?", created.ID)
	if u.Role != "admin" {
		t.Errorf("角色应已落库为 admin，实际 %q", u.Role)
	}

	// 非法角色必须拒，且不能把库里改成空值。
	rec = doJSON(t, h, "POST", "/api/v1/admin/users/"+created.ID+"/role", map[string]string{"role": "root"}, admin)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("非法角色应 422，实际 %d", rec.Code)
	}
	s.DB.First(&u, "id = ?", created.ID)
	if u.Role != "admin" {
		t.Errorf("非法角色不应改动库里的值，实际 %q", u.Role)
	}
}

func TestAdminDisable_RevokesExistingSessions(t *testing.T) {
	s, h := flowServer(t, nil)
	_, admin := loginAdmin(t, s, h)
	target := seedUserWithPW(t, s, "judy", goodPW, "member", "active")

	ck := cookieOf(t, doJSON(t, h, "POST", "/api/v1/auth/login", map[string]string{"username": "judy", "password": goodPW}, nil))
	if rec := doJSON(t, h, "GET", "/api/v1/auth/me", nil, ck); rec.Code != http.StatusOK {
		t.Fatalf("前置条件不成立：judy 应已登录")
	}

	rec := doJSON(t, h, "POST", "/api/v1/admin/users/"+target.ID+"/disable", nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("禁用失败：%d %s", rec.Code, rec.Body.String())
	}
	// 关键：被禁用的人手上那颗 cookie 必须立刻失效。
	// 只改 status 的话，下次认证虽然会拦，但那枚 cookie 仍算"有效凭证"，
	// 管理员一旦重新启用，攻击者的旧登录态会跟着活过来。
	if rec := doJSON(t, h, "GET", "/api/v1/auth/me", nil, ck); rec.Code != http.StatusUnauthorized {
		t.Errorf("禁用后旧 cookie 应失效（401），实际 %d %s", rec.Code, rec.Body.String())
	}

	// 重新启用后能登录（会话要重新签，不能自动复活）。
	rec = doJSON(t, h, "POST", "/api/v1/admin/users/"+target.ID+"/enable", nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("启用失败：%d %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, h, "GET", "/api/v1/auth/me", nil, ck); rec.Code != http.StatusUnauthorized {
		t.Errorf("启用不应让旧 cookie 复活，实际 %d", rec.Code)
	}
	if rec := doJSON(t, h, "POST", "/api/v1/auth/login", map[string]string{"username": "judy", "password": goodPW}, nil); rec.Code != http.StatusOK {
		t.Errorf("启用后应能重新登录，实际 %d %s", rec.Code, rec.Body.String())
	}
}

func TestAdminResetPassword_RevokesSessionsAndAcceptsNewPassword(t *testing.T) {
	s, h := flowServer(t, nil)
	_, admin := loginAdmin(t, s, h)
	target := seedUserWithPW(t, s, "karl", goodPW, "member", "active")
	ck := cookieOf(t, doJSON(t, h, "POST", "/api/v1/auth/login", map[string]string{"username": "karl", "password": goodPW}, nil))

	rec := doJSON(t, h, "POST", "/api/v1/admin/users/"+target.ID+"/reset-password", map[string]string{"new_password": "Reset0rd!23"}, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("重置口令失败：%d %s", rec.Code, rec.Body.String())
	}
	// 重置的语义是「这个账号可能已经泄露」，所以旧会话必须一起清掉。
	if rec := doJSON(t, h, "GET", "/api/v1/auth/me", nil, ck); rec.Code != http.StatusUnauthorized {
		t.Errorf("重置口令后旧会话应失效（401），实际 %d", rec.Code)
	}
	if rec := doJSON(t, h, "POST", "/api/v1/auth/login", map[string]string{"username": "karl", "password": "Reset0rd!23"}, nil); rec.Code != http.StatusOK {
		t.Errorf("新口令应能登录，实际 %d %s", rec.Code, rec.Body.String())
	}

	// 弱口令必须拒。
	rec = doJSON(t, h, "POST", "/api/v1/admin/users/"+target.ID+"/reset-password", map[string]string{"new_password": "abc"}, admin)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("弱口令重置应 422，实际 %d", rec.Code)
	}
}

func TestAdminCannotActOnSelf(t *testing.T) {
	s, h := flowServer(t, nil)
	me, admin := loginAdmin(t, s, h)

	// 能把自己禁掉/降权的话，一个误点就能把系统锁死在没人管的状态，
	// 而且没有第二个管理员可以救回来。所以自操作一律拒绝。
	cases := []struct{ name, method, path string; body interface{} }{
		{"禁用自己", "POST", "/api/v1/admin/users/" + me.ID + "/disable", nil},
		{"审核自己", "POST", "/api/v1/admin/users/" + me.ID + "/approve", nil},
		{"给自己降权", "POST", "/api/v1/admin/users/" + me.ID + "/role", map[string]string{"role": "member"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := doJSON(t, h, c.method, c.path, c.body, admin)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("应 400，实际 %d %s", rec.Code, rec.Body.String())
			}
		})
	}
	// 库里的自己必须原样。
	var u models.User
	s.DB.First(&u, "id = ?", me.ID)
	if u.Role != "admin" || u.Status != "active" {
		t.Errorf("自我操作被拒后不应有副作用，实际 %s/%s", u.Role, u.Status)
	}
}

func TestAdminUserTargetMissingIs404(t *testing.T) {
	s, h := flowServer(t, nil)
	_, admin := loginAdmin(t, s, h)
	for _, p := range []string{"/api/v1/admin/users/u_ghost/approve", "/api/v1/admin/users/u_ghost/disable", "/api/v1/admin/users/u_ghost/enable"} {
		rec := doJSON(t, h, "POST", p, nil, admin)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s 对不存在的用户应 404，实际 %d %s", p, rec.Code, rec.Body.String())
		}
	}
}

// ---------- 审计导出 ----------

func TestAuditExport_NeutralisesSpreadsheetFormulas(t *testing.T) {
	s, h := flowServer(t, nil)
	_, admin := loginAdmin(t, s, h)

	// 攻击路径：把显示名改成公式，等管理员导出审计 CSV 用 Excel 打开。
	// 这不是 xss，是「文件里的内容被表格软件当代码执行」——审计日志恰恰是
	// 攻击者最容易留下内容的地方，所以它是真实的攻击面而不是理论风险。
	payloads := []string{
		`=cmd|' /C calc'!A0`,
		`+1+1`,
		`-1+1`,
		`@SUM(1+1)`,
	}
	rows := make([]models.AuditLog, 0, len(payloads))
	for i, p := range payloads {
		rows = append(rows, models.AuditLog{
			ID: fmt.Sprintf("al_%d", i), Seq: int64(i + 1),
			ActorType: "user", ActorName: p, Action: "user.create",
			ObjectType: "user", ObjectID: "u_x",
			PrevHash: "prev", SelfHash: fmt.Sprintf("self_%d", i),
		})
	}
	if err := s.DB.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}

	rec := doJSON(t, h, "GET", "/api/v1/admin/audit-logs/export", nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("导出失败：%d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, p := range payloads {
		// 原样的公式不能出现在任何一行的行首字段里。
		if strings.Contains(body, ","+p+",") || strings.Contains(body, "\n"+p+",") {
			t.Errorf("导出里出现了可被表格软件执行的裸公式 %q", p)
		}
	}
	// 转义后的形态应该是前导单引号。
	if !strings.Contains(body, `"'=cmd`) {
		t.Errorf("期望看到带单引号前缀的转义结果，实际导出：\n%s", body)
	}
	// 链校验头不能丢：导出的可信度全靠它。
	for _, h2 := range []string{"# range_start_prev_hash=", "# range_end_self_hash=", "# count=4"} {
		if !strings.Contains(body, h2) {
			t.Errorf("导出缺少 %q", h2)
		}
	}
}

func TestCsvField(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain", "plain"},
		{"a,b", `"a,b"`},
		{`say "hi"`, `"say ""hi"""`},
		{"line\nbreak", "\"line\nbreak\""},
		{"cr\rreturn", "\"cr\rreturn\""},
		// 公式前缀一律加单引号。这几个字符是 Excel/WPS/Sheets 认的公式起始符。
		{"=1+1", `"'=1+1"`},
		{"+1", `"'+1"`},
		{"-1", `"'-1"`},
		{"@x", `"'@x"`},
		{"\tTAB", "\"'\tTAB\""},
	}
	for _, c := range cases {
		if got := csvField(c.in); got != c.want {
			t.Errorf("csvField(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// ---------- 错误映射 ----------

func TestRenderErr_MapsWrappedErrors(t *testing.T) {
	// 这里测的是「错误被 %w 包过一层之后还能不能认出类型」。
	// 历史上用类型断言写过，包装一层就掉到 500，把用户的输入错误说成"服务内部错误"。
	s, _ := flowServer(t, nil)

	run := func(err error) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.renderErr(rec, err)
		return rec
	}

	// 1) apiErr 被包装 → 仍是 400 + 原始 code（errBadRequest 自带 400）
	rec := run(fmt.Errorf("模板 tpl_1 校验失败: %w", errBadRequest("字段 x 不合法")))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("包装后的 apiErr 应 400，实际 %d %s", rec.Code, rec.Body.String())
	}
	if c := decodeErr(t, rec).Error.Code; c != "data_invalid" {
		t.Errorf("code 应为 data_invalid，实际 %q", c)
	}

	// 2) render.Error 被包装 → 用渲染服务给的状态码与 code
	rec = run(fmt.Errorf("渲染第 3 条失败: %w", &render.Error{StatusCode: 422, Code: "chart_series_mismatch", Message: "系列数不可增删"}))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("包装后的 render.Error 应 422，实际 %d", rec.Code)
	}
	if c := decodeErr(t, rec).Error.Code; c != "chart_series_mismatch" {
		t.Errorf("code 应为 chart_series_mismatch，实际 %q", c)
	}

	// 3) 状态码越界的 render.Error → 502 而不是原样写出去
	rec = run(&render.Error{StatusCode: 0, Code: "bad_gateway", Message: "上游炸了"})
	if rec.Code != http.StatusBadGateway {
		t.Errorf("状态码为 0 的 render.Error 应回退 502，实际 %d", rec.Code)
	}

	// 4) 未知错误 → 500，且不能把内部信息吐给调用方
	rec = run(errors.New("pq: password authentication failed for user \"docforge\""))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("未知错误应 500，实际 %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "password authentication failed") {
		t.Errorf("500 响应泄露了内部错误细节：%s", rec.Body.String())
	}
}

func TestGetPage_ClampsOutOfRange(t *testing.T) {
	cases := []struct{ q string; page, size int }{
		{"", 1, 20},
		{"?page=3&page_size=50", 3, 50},
		{"?page=0&page_size=0", 1, 20},
		{"?page=-5&page_size=-1", 1, 20},
		// 上限必须收口：page_size 不设上限时，一个请求就能把整张表拉进内存。
		{"?page_size=100000", 1, 20},
		{"?page=abc&page_size=xyz", 1, 20},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/x"+c.q, nil)
		pg := getPage(r)
		if pg.Page != c.page || pg.PageSize != c.size {
			t.Errorf("getPage(%q) = %d/%d，期望 %d/%d", c.q, pg.Page, pg.PageSize, c.page, c.size)
		}
	}
}
