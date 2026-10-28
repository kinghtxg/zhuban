// Package config 负责从环境变量加载 docforge Go 主服务的全部运行配置。
// 所有配置项都有合理默认值，启动时由 main 打印关键配置（密钥类打码）。
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config 是服务运行时的不可变配置快照。
type Config struct {
	HTTPAddr         string
	ExternalBaseURL  string
	DatabaseURL      string
	RenderServiceURL string
	DataDir          string

	SignedURLSecret string
	SignedURLTTL    time.Duration
	RetentionDays   int

	// 会话与本地账号
	AuthSecret string
	AuthDev    bool
	// SessionTTLDays 会话（cookie + sessions 表）有效期天数。
	// 「共享电脑上会话该留多久」是各部署方会给出不同答案的策略，不该写死在代码里。
	SessionTTLDays int
	AdminUsername  string
	AdminPassword  string
	// AdminUsernames 需要始终具备管理员权限的用户名白名单（逗号分隔）。
	// 存在的意义：OIDC 自动开户只会给 member，而「谁是管理员」属于部署事实，
	// 不该靠一次性 SQL 改库（重装、换库、加副本都会丢）。每次启动与每次登录
	// 都按这份名单幂等提升，配置在就永远成立。
	AdminUsernames []string
	AllowRegister  bool

	// 可选 OIDC（与本地登录并存，不互斥）
	OIDCEnabled      bool
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSec    string
	OIDCRedirect     string
	OIDCAutoProvision string // active | pending | none

	MaxRecords    int
	MaxJSONMB     int
	MaxTemplateMB int
	MaxImageMB    int

	CallbackAllowPrivate bool
	CallbackHostAllow     []string

	// TrustedProxies 可信反向代理名单（CIDR 或裸 IP）。只有对端落在这个名单里，
	// 才采信它带来的 X-Forwarded-For。详见 internal/clientip 的包注释。
	TrustedProxies []string

	// 邮件（V3.2）。SMTP 凭据**只在服务端配置**，API 侧能指定的只有收件人与正文：
	// 让调用方自带 SMTP 等于把本系统变成一台开放中继，发件域名声誉会被别人花光。
	// 配置来源优先级：数据库 setting（管理端可改）> 环境变量（部署事实）。
	SMTPHost string
	SMTPPort int
	SMTPUser string
	SMTPPass string
	SMTPFrom string
	// SMTPTLS 连接加密方式：tls（465 直连）/ starttls（587 升格）/ none（内网裸连）。
	// 空串时按端口推断：465→tls，587→starttls，其余→starttls。
	SMTPTLS string
	// EmailAllowedDomains 允许发送的收件人域名白名单。为空 = **拒绝**通过 API 发信
	// （fail-closed）：管理端的「测试邮件」不受此限，但正式投递必须先声明发给谁。
	EmailAllowedDomains []string
	EmailMaxRecipients  int
	EmailMaxAttachMB    int
	EmailMaxBodyChars   int
	EmailTimeout        time.Duration

	SeedBuiltin bool

	// 版本与更新检查。
	//
	// UpdateCheck 默认开启，UpdateRepo 默认指向官方仓库：开源版的自建用户
	// 默认就该收到更新提示，否则这个功能只有原作者用得上。
	//
	// 关掉它的理由通常是**出口受限**（政务网、隔离内网根本没有 api.github.com），
	// 此时每 6 小时一次失败请求只会在日志里刷 [update] 查询失败，什么也不会发生。
	// 所以默认值取 true、但一次失败即退避 6 小时，不在启动时同步打网络。
	UpdateCheck bool
	UpdateRepo  string

	// render client 超时（契约 §5：render 70s / scan 30s / preview 90s）
	RenderTimeout  time.Duration
	ScanTimeout    time.Duration
	PreviewTimeout time.Duration

	WorkerPoll time.Duration
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		b, err := strconv.ParseBool(v)
		if err == nil {
			return b
		}
	}
	return def
}

// envList 读逗号分隔的名单，去空白、去空项、去重（保留原顺序）。
func envList(key, def string) []string {
	raw := env(key, def)
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, 4)
	for _, part := range strings.Split(raw, ",") {
		v := strings.TrimSpace(part)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// Load 读取环境变量构造配置。DATABASE_URL 为空时按 POSTGRES_* 拼装。
func Load() *Config {
	// 默认值必须是「谁 clone 下来都能用」的本地地址。写死某个内网 IP，
	// 自建用户第一次启动就会看到回调地址、接入示例全指向别人的机器。
	base := strings.TrimRight(env("EXTERNAL_BASE_URL", "http://localhost:8100"), "/")

	dsn := env("DATABASE_URL", "")
	if dsn == "" {
		host := env("POSTGRES_HOST", "")
		if host != "" {
			port := env("POSTGRES_PORT", "5432")
			user := env("POSTGRES_USER", "docforge")
			pass := env("POSTGRES_PASSWORD", "")
			db := env("POSTGRES_DB", "docforge")
			dsn = fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable TimeZone=UTC",
				host, port, user, pass, db)
		}
	}

	ttlHours := envInt("SIGNED_URL_TTL_HOURS", 24)
	// OIDC_ISSUER 默认留空：OIDC 是可选能力，默认必须「不指向任何人的 SSO」。
	// 早期这里默认写死了开发者的 SSO 地址，开源后等于把自建部署的默认值
	// 指向一个外人无权访问的服务器，只会得到一堆 Discovery 失败日志。
	issuer := strings.TrimRight(env("OIDC_ISSUER", ""), "/")
	redirect := env("OIDC_REDIRECT_URL", base+"/api/v1/auth/oidc/callback")

	c := &Config{
		HTTPAddr:         env("HTTP_ADDR", ":8100"),
		ExternalBaseURL:  base,
		DatabaseURL:      dsn,
		RenderServiceURL: strings.TrimRight(env("RENDER_SERVICE_URL", "http://render:8101"), "/"),
		DataDir:          env("DATA_DIR", "./volumes"),

		SignedURLSecret: env("SIGNED_URL_SECRET", ""),
		SignedURLTTL:    time.Duration(ttlHours) * time.Hour,
		RetentionDays:   envInt("RETENTION_DAYS", 7),

		AuthSecret: env("AUTH_SECRET", ""),
		AuthDev:    envBool("AUTH_DEV", false), // 安全默认关闭；仅本地联调显式置 true
		// 7 天与历史写死的常量一致，升级后不会把在线用户踢下线。
		// auth 侧还有一层 <=0 兜底：配 0 或负数不会立刻让所有人掉线。
		SessionTTLDays: envInt("SESSION_TTL_DAYS", 7),

		OIDCEnabled:       envBool("OIDC_ENABLED", false),
		OIDCIssuer:        issuer,
		OIDCClientID:      env("OIDC_CLIENT_ID", ""),
		OIDCClientSec:     env("OIDC_CLIENT_SECRET", ""),
		OIDCRedirect:      redirect,
		OIDCAutoProvision: env("OIDC_AUTO_PROVISION", "active"),
		AdminUsername:     env("ADMIN_USERNAME", ""),
		AdminPassword:     env("ADMIN_PASSWORD", ""),
		AdminUsernames:    envList("ADMIN_USERNAMES", ""),
		AllowRegister:     envBool("ALLOW_REGISTER", false),

		MaxRecords:    envInt("MAX_RECORDS", 1000),
		MaxJSONMB:     envInt("MAX_JSON_MB", 20),
		MaxTemplateMB: envInt("MAX_TEMPLATE_MB", 10),
		MaxImageMB:    envInt("MAX_IMAGE_MB", 5),

		CallbackAllowPrivate: envBool("CALLBACK_ALLOW_PRIVATE", false),
		TrustedProxies:       trustedProxies(),
		SeedBuiltin:          envBool("SEED_BUILTIN", true),
		UpdateCheck:          envBool("UPDATE_CHECK", true),
		UpdateRepo:           env("UPDATE_REPO", "kinghtxg/zhuban"),

		SMTPHost:             env("SMTP_HOST", ""),
		SMTPPort:             envInt("SMTP_PORT", 587),
		SMTPUser:             env("SMTP_USER", ""),
		SMTPPass:             env("SMTP_PASS", ""),
		SMTPFrom:             env("SMTP_FROM", ""),
		SMTPTLS:              strings.ToLower(env("SMTP_TLS", "")),
		EmailAllowedDomains:  envList("EMAIL_ALLOWED_DOMAINS", ""),
		EmailMaxRecipients:   envInt("EMAIL_MAX_RECIPIENTS", 10),
		EmailMaxAttachMB:     envInt("EMAIL_MAX_ATTACH_MB", 20),
		EmailMaxBodyChars:    envInt("EMAIL_MAX_BODY_CHARS", 8000),
		EmailTimeout:         30 * time.Second,

		RenderTimeout:  70 * time.Second,
		ScanTimeout:    30 * time.Second,
		PreviewTimeout: 90 * time.Second,
		WorkerPoll:     2 * time.Second,
	}
	if v := env("CALLBACK_HOST_ALLOWLIST", ""); v != "" {
		for _, h := range strings.Split(v, ",") {
			h = strings.TrimSpace(h)
			if h != "" {
				c.CallbackHostAllow = append(c.CallbackHostAllow, strings.ToLower(h))
			}
		}
	}
	return c
}

// trustedProxies 解析 TRUSTED_PROXIES。默认信任「本机 + 私网段」：
// 单机部署里 Go 服务几乎总被 nginx 或 docker 端口映射包了一层，对端看起来是
// 127.0.0.1（宿主 nginx 直连）或 172.17.0.1（端口映射后的 docker 网关），
// 而不是真实客户端。默认留空会让这两个场景把所有人都算成同一个 IP ——
// 限流从此对所有用户共用一个额度，审计里也全是 172.17.0.1。
//
// 反过来，如果这个服务是**直接**暴露在公网上的（没有代理），请设
// TRUSTED_PROXIES=none：此时对端就是攻击者本人，任何转发头都不可信。
func trustedProxies() []string {
	raw := strings.TrimSpace(os.Getenv("TRUSTED_PROXIES"))
	switch strings.ToLower(raw) {
	case "none", "off", "false":
		return nil
	case "":
		return []string{
			"127.0.0.0/8", "::1/128",
			"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7",
		}
	}
	return envList("TRUSTED_PROXIES", "")
}

// IsSQLite 判断是否使用 sqlite 兜底（DATABASE_URL 以 sqlite:// 开头）。
func (c *Config) IsSQLite() bool {
	return strings.HasPrefix(c.DatabaseURL, "sqlite://")
}

// SQLitePath 从 DATABASE_URL=sqlite:///./x.db 中提取文件路径。
// 规范形式 sqlite:///<path>（三斜杠），兼容 sqlite://<path>。
func (c *Config) SQLitePath() string {
	p := strings.TrimPrefix(c.DatabaseURL, "sqlite:///")
	if p == c.DatabaseURL {
		p = strings.TrimPrefix(c.DatabaseURL, "sqlite://")
	}
	return p
}
