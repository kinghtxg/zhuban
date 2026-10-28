// Package mailer 负责把生成好的文档用邮件投递出去（V3.2）。
//
// 边界只有一条，但必须守住：**SMTP 凭据只在服务端配置**。
// API 侧能指定的只有「发给谁、主题是什么、正文是什么、带哪些附件」。
// 让调用方自带 SMTP，等于把本系统变成一台开放中继——发件域名的声誉
// 会被别人花光，而且 Abuse 投诉会落在域名所有者头上。
//
// 与之配套的是收件人 fail-closed：未配置允许域名时，通过 API 投递一律拒绝
// （管理端「测试邮件」不受此限，那是运维自己在验链路）。
package mailer

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"docforge/server/internal/auditx"
	"docforge/server/internal/config"
	"docforge/server/internal/models"

	"gorm.io/gorm"
)

// 数据库 setting 的键名。这些值由管理端写入，覆盖同名环境变量。
const (
	KeyHost           = "smtp_host"
	KeyPort           = "smtp_port"
	KeyUser           = "smtp_user"
	KeyPass           = "smtp_pass"
	KeyFrom           = "smtp_from"
	KeyTLS            = "smtp_tls"
	KeyAllowedDomains = "mail_allowed_domains"
)

// TLS 模式。
const (
	TLSDirect  = "tls"      // 465：一上来就 TLS
	TLSUpgrade = "starttls" // 587：先明文再升格
	TLSNone    = "none"     // 内网中继，不加密
)

// Config 是一份完整可用的发信配置（DB 覆盖 env 之后的结果）。
type Config struct {
	Host     string
	Port     int
	User     string
	Pass     string
	From     string
	TLSMode  string
	Timeout  time.Duration

	AllowedDomains []string
	MaxRecipients  int
	MaxAttachBytes int64
	MaxBodyChars   int
}

// Sender 发信器。DB 可以为 nil（单测里只测 MIME/校验时不需要查库）。
type Sender struct {
	DB  *gorm.DB
	Cfg *config.Config
	// Audit 投递结果的落点（哈希链审计）。为 nil 时只发信不留痕，
	// 正常部署下 main.go 总会装配好。
	Audit *auditx.Chain
}

func New(db *gorm.DB, cfg *config.Config) *Sender {
	s := &Sender{DB: db, Cfg: cfg}
	// DB 都没有的话审计链也无从落笔，保持 nil 让 record 跳过，别写出 panic。
	if db != nil {
		s.Audit = &auditx.Chain{DB: db}
	}
	return s
}

func (s *Sender) setting(key string) string {
	if s.DB == nil {
		return ""
	}
	var row models.Setting
	if err := s.DB.Where("key = ?", key).First(&row).Error; err != nil {
		return ""
	}
	return strings.TrimSpace(row.Value)
}

// Load 合并配置：数据库（管理端）优先，环境变量兜底。
//
// 为什么要两条来源：SMTP 主机/账号属于「部署事实」，写进 .env 才能在换机器、
// 重建容器后自动恢复；而运维偶尔要改密码、换发件人，走 UI 改比改文件重启快。
func (s *Sender) Load() Config {
	c := Config{
		Host:           s.Cfg.SMTPHost,
		Port:           s.Cfg.SMTPPort,
		User:           s.Cfg.SMTPUser,
		Pass:           s.Cfg.SMTPPass,
		From:           s.Cfg.SMTPFrom,
		TLSMode:        s.Cfg.SMTPTLS,
		AllowedDomains: s.Cfg.EmailAllowedDomains,
		MaxRecipients:  s.Cfg.EmailMaxRecipients,
		MaxAttachBytes: int64(s.Cfg.EmailMaxAttachMB) << 20,
		MaxBodyChars:   s.Cfg.EmailMaxBodyChars,
		Timeout:        s.Cfg.EmailTimeout,
	}
	if v := s.setting(KeyHost); v != "" {
		c.Host = v
	}
	if v := s.setting(KeyPort); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.Port = n
		}
	}
	if v := s.setting(KeyUser); v != "" {
		c.User = v
	}
	// 密码允许被显式清空（改回走 env），所以空串也认——只在 DB 里有这一行时生效。
	if s.DB != nil {
		var row models.Setting
		if err := s.DB.Where("key = ?", KeyPass).First(&row).Error; err == nil {
			c.Pass = row.Value
		}
	}
	if v := s.setting(KeyFrom); v != "" {
		c.From = v
	}
	if v := s.setting(KeyTLS); v != "" {
		c.TLSMode = strings.ToLower(strings.TrimSpace(v))
	}
	if v := s.setting(KeyAllowedDomains); v != "" {
		c.AllowedDomains = splitList(v)
	}
	if c.Timeout <= 0 {
		c.Timeout = 30 * time.Second
	}
	if c.MaxRecipients <= 0 {
		c.MaxRecipients = 10
	}
	if c.MaxBodyChars <= 0 {
		c.MaxBodyChars = 8000
	}
	c.TLSMode = normalizeTLS(c.TLSMode, c.Port)
	return c
}

func splitList(raw string) []string {
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, strings.ToLower(v))
		}
	}
	return out
}

// normalizeTLS 端口推断：465 直连 TLS，587 升格，其余默认升格。
func normalizeTLS(mode string, port int) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case TLSDirect, TLSUpgrade, TLSNone:
		return strings.ToLower(strings.TrimSpace(mode))
	}
	if port == 465 {
		return TLSDirect
	}
	return TLSUpgrade
}

// Enabled 是否配置了发信服务。
func (c Config) Enabled() bool { return strings.TrimSpace(c.Host) != "" }

// EffectiveFrom 实际发件人：没单独配 SMTP_FROM 时用登录账号兜底。
func (c Config) EffectiveFrom() string {
	if strings.TrimSpace(c.From) != "" {
		return strings.TrimSpace(c.From)
	}
	return strings.TrimSpace(c.User)
}

// ErrNotConfigured 未配置 SMTP。
var ErrNotConfigured = errors.New("邮件服务未配置（SMTP 主机为空）")

// ErrDomainNotAllowed 收件人域名不在白名单里。
type ErrDomainNotAllowed struct {
	Addr   string
	Domain string
}

func (e *ErrDomainNotAllowed) Error() string {
	return "收件人域名 " + e.Domain + " 不在允许名单内（" + e.Addr + "）"
}

// ErrTooManyRecipients 单次收件人超限。
var ErrTooManyRecipients = errors.New("单次收件人数量超出上限")

// CheckRecipients 校验收件人：格式、数量、域名白名单。
//
// 白名单为空 → 一律拒绝（fail-closed）。宁可让第一次配置的人先被拦一下、
// 去管理端把域名填上，也不能默认放行「发给任意人」。
func (c Config) CheckRecipients(to, cc, bcc []string) error {
	return c.checkRecipients(to, cc, bcc, false)
}

// CheckRecipientsLoose 跳过域名白名单，其余（格式、数量）照查。
//
// 只有管理端「测试邮件」用得到：白名单是给 API 调用方准备的笼子，
// 运维自己验链路时不该被自己还没填完的白名单挡住。
func (c Config) CheckRecipientsLoose(to, cc, bcc []string) error {
	return c.checkRecipients(to, cc, bcc, true)
}

func (c Config) checkRecipients(to, cc, bcc []string, skipDomain bool) error {
	addrs := make([]string, 0, len(to)+len(cc)+len(bcc))
	addrs = append(addrs, to...)
	addrs = append(addrs, cc...)
	addrs = append(addrs, bcc...)
	if len(addrs) == 0 {
		return errors.New("收件人为空")
	}
	if c.MaxRecipients > 0 && len(addrs) > c.MaxRecipients {
		return ErrTooManyRecipients
	}
	for _, a := range addrs {
		addr := strings.TrimSpace(a)
		if addr == "" {
			return errors.New("收件人含空值")
		}
		if !strings.Contains(addr, "@") || strings.HasPrefix(addr, "@") || strings.HasSuffix(addr, "@") {
			return errors.New("收件人地址不合法：" + addr)
		}
		// 显示名形式（张三 <a@b.com>）只取尖括号里的地址
		if i := strings.LastIndex(addr, "<"); i >= 0 {
			if j := strings.LastIndex(addr, ">"); j > i {
				addr = strings.TrimSpace(addr[i+1 : j])
			}
		}
		if skipDomain {
			continue
		}
		if len(c.AllowedDomains) == 0 {
			return &ErrDomainNotAllowed{Addr: addr, Domain: ""}
		}
		domain := strings.ToLower(addr[strings.LastIndex(addr, "@")+1:])
		if !domainAllowed(domain, c.AllowedDomains) {
			return &ErrDomainNotAllowed{Addr: addr, Domain: domain}
		}
	}
	return nil
}

// domainAllowed 支持精确匹配与「.example.com」形式的后缀匹配（匹配自身及子域）。
func domainAllowed(domain string, allowed []string) bool {
	for _, a := range allowed {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" {
			continue
		}
		if strings.HasPrefix(a, ".") {
			if domain == strings.TrimPrefix(a, ".") || strings.HasSuffix(domain, a) {
				return true
			}
			continue
		}
		if domain == a {
			return true
		}
	}
	return false
}
