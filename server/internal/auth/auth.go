// Package auth 实现本地账号密码（argon2id）+ 可选 APSSO OIDC 双轨认证、
// 会话 cookie、X-API-Key 鉴权、登录锁定/IP 限流与 QPS 令牌桶。
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"docforge/server/internal/clientip"
	"docforge/server/internal/config"
	"docforge/server/internal/idgen"
	"docforge/server/internal/models"

	"gorm.io/gorm"
)

type ctxKey string

const (
	CookieName   = "df_session"
	ctxPrincipal = ctxKey("principal")
)

// Principal 是鉴权后的调用主体。
type Principal struct {
	User   *models.User
	APIKey *models.APIKey
}

// Manager 集中管理鉴权。
type Manager struct {
	DB      *gorm.DB
	Cfg     *config.Config
	Guard   *LoginGuard
	Limiter *Limiter
	// IPs 解析来源 IP（与限流、审计共用同一套判据）。
	// 为 nil 时 API Key 的「最近使用 IP」留空，而不是记一个看起来像真地址的猜测值。
	IPs *clientip.Resolver
}

// New 构造。
func New(db *gorm.DB, cfg *config.Config) *Manager {
	return &Manager{DB: db, Cfg: cfg, Guard: NewLoginGuard(), Limiter: NewLimiter()}
}

// clientIP 取请求来源 IP。IPs 未注入时返回空串。
func (m *Manager) clientIP(r *http.Request) string {
	if m == nil || m.IPs == nil || r == nil {
		return ""
	}
	return m.IPs.ClientIP(r)
}

// sessionTTL 会话有效期。
// 以前是包级常量 7 天，想改只能改代码重编——而「共享电脑上会话该留多久」
// 恰恰是不同部署方会给出不同答案的策略，必须可配。
func (m *Manager) sessionTTL() time.Duration {
	days := 0
	if m != nil && m.Cfg != nil {
		days = m.Cfg.SessionTTLDays
	}
	if days <= 0 {
		days = 7 // 与历史行为一致，避免配置缺失时把所有人立刻踢下线
	}
	return time.Duration(days) * 24 * time.Hour
}

// PrincipalFrom 取上下文里的调用主体。
func PrincipalFrom(r *http.Request) *Principal {
	v := r.Context().Value(ctxPrincipal)
	if v == nil {
		return nil
	}
	return v.(*Principal)
}

// WithPrincipal 返回带调用主体的新 context（测试与中间件复用）。
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, ctxPrincipal, p)
}

// ---------- 会话令牌（HMAC 签名，载荷 user_id|exp） ----------

func (m *Manager) secret() []byte {
	s := m.Cfg.AuthSecret
	if s == "" {
		s = "insecure-dev-auth-secret-change-me"
	}
	return []byte(s)
}

// secureCookie 判断会话 cookie 是否应带 Secure 标志。
//
// 依据对外基址的协议而不是 r.TLS：服务几乎总是跑在反向代理后面，r.TLS 在容器内
// 恒为 nil，按它判断会让公网站点永久缺少 Secure（cookie 可能随明文 http 请求外泄）。
// 反过来内网 http 部署若强行加 Secure，浏览器会直接丢弃 cookie，登录态建不起来——
// 所以必须跟随 EXTERNAL_BASE_URL 的实际协议。
func (m *Manager) secureCookie() bool {
	if m.Cfg == nil {
		return false
	}
	return strings.HasPrefix(strings.ToLower(m.Cfg.ExternalBaseURL), "https://")
}

// hashSID 把会话标识换成入库形态。
// 库里只存哈希：数据库被读走时，拿到的不是一串可以直接当 cookie 用的令牌。
func hashSID(sid string) string {
	sum := sha256.Sum256([]byte(sid))
	return hex.EncodeToString(sum[:])
}

// IssueCookie 为用户签发会话 cookie，并在 sessions 表落一条可撤销记录。
//
// 为什么要落库：cookie 本身是自包含的 HMAC 令牌，一旦签出去，服务端说的话
// 就不算数了 —— 用户点「退出登录」、管理员重置密码、账号被停用，都只能等
// 7 天 TTL 自然到期。被复制走的那份 cookie 在此期间一直是有效凭证。
// 落一条 sessions 记录之后，认证从「签名对就行」变成「签名对 **且这枚令牌
// 仍在册**」，登出与改密才能立刻生效。
func (m *Manager) IssueCookie(w http.ResponseWriter, u *models.User) error {
	sid := idgen.Hex(16)
	ttl := m.sessionTTL()
	exp := time.Now().Add(ttl)
	// 顺手清掉这个用户已经过期的行：不起眼但必要，否则这张表只涨不消。
	m.DB.Where("user_id = ? AND expires_at < ?", u.ID, time.Now()).Delete(&models.Session{})
	rec := models.Session{
		TokenHash: hashSID(sid), UserID: u.ID,
		CreatedAt: time.Now(), ExpiresAt: exp,
	}
	if err := m.DB.Create(&rec).Error; err != nil {
		// 落不了库就不要签发：签一张立刻不可用的 cookie，用户看到的是
		// 「登录成功但一直未登录」，比直接报错难查得多。
		return err
	}
	payload := u.ID + "|" + strconv.FormatInt(exp.Unix(), 10) + "|" + sid
	mac := hmac.New(sha256.New, m.secret())
	_, _ = mac.Write([]byte(payload))
	token := base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: token, Path: "/",
		HttpOnly: true, Secure: m.secureCookie(), SameSite: http.SameSiteLaxMode,
		Expires: exp, MaxAge: int(ttl.Seconds()),
	})
	return nil
}

// RevokeSession 撤销这次请求携带的会话（登出）。
// 先删库再清 cookie：反过来的话，删库失败就成了「浏览器里没了、服务端还在」。
func (m *Manager) RevokeSession(r *http.Request) {
	if _, sid, ok := m.verifyCookie(cookieToken(r)); ok {
		m.DB.Where("token_hash = ?", hashSID(sid)).Delete(&models.Session{})
	}
}

// RevokeOtherSessions 撤销某用户除当前会话之外的全部会话。
// 自己改密码时用它：该把别处的登录踢掉，但不该把自己踢回登录页。
func (m *Manager) RevokeOtherSessions(r *http.Request, userID string) {
	q := m.DB.Where("user_id = ?", userID)
	if _, sid, ok := m.verifyCookie(cookieToken(r)); ok {
		q = q.Where("token_hash <> ?", hashSID(sid))
	}
	q.Delete(&models.Session{})
}

// RevokeAllSessions 撤销某用户的全部会话（管理员重置密码、停用账号时用）。
func (m *Manager) RevokeAllSessions(userID string) {
	m.DB.Where("user_id = ?", userID).Delete(&models.Session{})
}

func cookieToken(r *http.Request) string {
	if r == nil {
		return ""
	}
	if c, err := r.Cookie(CookieName); err == nil {
		return c.Value
	}
	return ""
}

// ClearCookie 清除会话。
func (m *Manager) ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Path: "/", HttpOnly: true, Secure: m.secureCookie(),
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Unix(0, 0), MaxAge: -1,
	})
}

// verifyCookie 校验令牌签名与有效期，返回用户与会话标识。
//
// 载荷是 user_id|exp|sid 三段。两段的老格式（没有 sid）一律视为无效：
// 那种令牌无法撤销，放行它等于给「登出后仍然有效」留一条后门。
// 代价是升级后所有人需要重新登录一次，这个代价是应该付的。
func (m *Manager) verifyCookie(token string) (userID, sid string, ok bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return "", "", false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", "", false
	}
	macB, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", "", false
	}
	mac := hmac.New(sha256.New, m.secret())
	_, _ = mac.Write(payload)
	if !hmac.Equal(mac.Sum(nil), macB) {
		return "", "", false
	}
	fields := strings.Split(string(payload), "|")
	if len(fields) != 3 || fields[0] == "" || fields[2] == "" {
		return "", "", false
	}
	var exp int64
	exp, err = strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return "", "", false
	}
	if time.Now().Unix() > exp {
		return "", "", false
	}
	return fields[0], fields[2], true
}

// loadSessionUser 一次查询同时确认「会话仍在册」与「用户仍可登录」。
//
// 合成一条 SQL 不是为了省事，是为了不退化：原来的实现每请求查一次 users，
// 加上会话校验之后如果写成两次查询，认证热路径的开销就翻倍了。
func (m *Manager) loadSessionUser(userID, sid string) *models.User {
	var u models.User
	err := m.DB.Model(&models.User{}).
		Joins("JOIN sessions ON sessions.user_id = users.id").
		Where("users.id = ? AND users.status = ?", userID, "active").
		Where("sessions.token_hash = ? AND sessions.expires_at > ?", hashSID(sid), time.Now()).
		First(&u).Error
	if err != nil {
		return nil
	}
	return &u
}

// SessionUser 返回这次请求携带的有效会话对应的用户，没有则返回 nil。
//
// 它只解决一个问题：**给谁看哪个页面**——未登录的人访问 / 时看到官网，
// 已登录的人看到工作台。这不是鉴权：真正的准入仍然只由 Authenticate 决定，
// 而且这里返回 nil 也不会导致任何数据被放出去（受保护接口照样 401）。
//
// 复用的是 resolve 里那条链路（签名校验 + 会话表 + 用户状态），所以
// 「cookie 签名对但会话已被撤销」「账号已被禁用」都会得到 nil —— 与接口侧同口径。
// 不用另写一套判断，否则两处迟早会不一致：页面说你已登录，接口说你未登录。
func (m *Manager) SessionUser(r *http.Request) *models.User {
	p, _, _ := m.resolve(r)
	if p == nil {
		return nil
	}
	return p.User
}

// Authenticate 中间件：依次尝试 cookie、X-API-Key、开发模式 X-Dev-User。
func (m *Manager) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, code, msg := m.resolve(r)
		if msg != "" {
			writeErr(w, statusFromCode(code), code, msg, nil)
			return
		}
		ctx := context.WithValue(r.Context(), ctxPrincipal, p)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (m *Manager) resolve(r *http.Request) (*Principal, string, string) {
	// 1) 会话 cookie
	if tok := cookieToken(r); tok != "" {
		if uid, sid, ok := m.verifyCookie(tok); ok {
			if u := m.loadSessionUser(uid, sid); u != nil {
				return &Principal{User: u}, "", ""
			}
		}
	}
	// 2) X-API-Key
	if key := strings.TrimSpace(r.Header.Get("X-API-Key")); key != "" {
		if !strings.HasPrefix(key, "df_") {
			return nil, "unauthorized", "无效的 API Key"
		}
		hash := sha256Hex(key)
		var ak models.APIKey
		if err := m.DB.Where("key_hash = ?", hash).First(&ak).Error; err != nil {
			return nil, "unauthorized", "API Key 不存在或已删除"
		}
		if !ak.Enabled {
			return nil, "unauthorized", "API Key 已被停用"
		}
		if !m.Limiter.Allow(ak.ID, ak.QPSLimit) {
			return nil, "rate_limited", "请求过于频繁，请降低调用 QPS"
		}
		// last_used_at / last_used_ip 是「展示给用户看的信息」，不是计费依据，
		// 没必要每个请求都写。按分钟节流：一个 QPS 100 的 Key 原来每分钟产生
		// 6000 次 UPDATE，每次都是同样两三个字节的写入，全部落到 WAL 与索引页上。
		// 代价是最多晚一分钟才看到「最近使用」，这个精度对排查已经足够。
		//
		// IP 和时间一起写：分两次写会让「时间更新了但 IP 还是上一分钟的」这种
		// 半新半旧的状态出现在短暂窗口里，而排查时恰恰只会看一行。
		if now := time.Now(); ak.LastUsedAt == nil || now.Sub(*ak.LastUsedAt) >= time.Minute {
			upd := map[string]interface{}{"last_used_at": now}
			if ip := m.clientIP(r); ip != "" {
				upd["last_used_ip"] = ip
			}
			m.DB.Model(&models.APIKey{}).Where("id = ?", ak.ID).Updates(upd)
			ak.LastUsedAt = &now
			if ip, ok := upd["last_used_ip"].(string); ok {
				ak.LastUsedIP = ip
			}
		}
		var u models.User
		if err := m.DB.First(&u, "id = ?", ak.UserID).Error; err != nil {
			return nil, "unauthorized", "API Key 绑定用户不存在"
		}
		if u.Status != "active" {
			return nil, "unauthorized", "API Key 绑定用户未激活"
		}
		return &Principal{User: &u, APIKey: &ak}, "", ""
	}
	// 3) 开发模式 X-Dev-User（upsert 为 active admin）
	if m.Cfg.AuthDev {
		if name := strings.TrimSpace(r.Header.Get("X-Dev-User")); name != "" {
			u, err := m.upsertDevAdmin(r.Context(), name)
			if err == nil {
				return &Principal{User: u}, "", ""
			}
		}
	}
	return nil, "unauthorized", "未登录或缺少有效凭证"
}

func (m *Manager) upsertDevAdmin(ctx context.Context, name string) (*models.User, error) {
	uname := "dev_" + name
	var u models.User
	err := m.DB.Where("username = ?", uname).First(&u).Error
	if err != nil {
		u = models.User{
			ID: "u_" + idgen.Hex(8), Username: uname, DisplayName: name,
			Email: name + "@dev.local", Role: "admin", Status: "active", Source: "local",
		}
		if err := m.DB.Create(&u).Error; err != nil {
			return nil, err
		}
	} else if u.Status != "active" || u.Role != "admin" {
		m.DB.Model(&u).Updates(map[string]interface{}{"status": "active", "role": "admin"})
		u.Status = "active"
		u.Role = "admin"
	}
	return &u, nil
}

// UpsertDevForLogin 供 dev-login 端点 upsert 一个 active admin。
func (m *Manager) UpsertDevForLogin(ctx context.Context, name string) (*models.User, error) {
	return m.upsertDevAdmin(ctx, name)
}

// Initialized 判断系统是否已有用户（即是否已初始化管理员）。
func (m *Manager) Initialized() bool {
	var cnt int64
	m.DB.Model(&models.User{}).Count(&cnt)
	return cnt > 0
}

// ---------- 小工具 ----------

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func statusFromCode(code string) int {
	switch code {
	case "rate_limited":
		return http.StatusTooManyRequests
	case "unauthorized":
		return http.StatusUnauthorized
	}
	return http.StatusBadRequest
}

func writeErr(w http.ResponseWriter, status int, code, msg string, details interface{}) {
	if details == nil {
		details = []interface{}{}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{"code": code, "message": msg, "details": details},
	})
}
