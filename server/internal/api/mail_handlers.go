package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"docforge/server/internal/mailer"
)

// ---------------------------------------------------------------------------
// 管理端：邮件服务配置
//
// SMTP 凭据只在这里进系统。API 侧能传的只有收件人与正文，
// 这条边界一旦放开，本系统就成了一台开放中继。
// ---------------------------------------------------------------------------

// handleMailSettings GET /admin/mail/settings：读回当前配置（密码永不出网）。
func (s *Server) handleMailSettings(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	if s.Mailer == nil {
		writeErr(w, http.StatusServiceUnavailable, "internal_error", "邮件模块未初始化", nil)
		return
	}
	st := s.Mailer.ReadAll()
	c := s.Mailer.Load()
	// DB 里没配时，把环境变量的值显示出来（密码仍然只给「已设置」标记）。
	if st[mailer.KeyHost] == "" {
		st[mailer.KeyHost] = s.Cfg.SMTPHost
	}
	if st[mailer.KeyPort] == "" {
		st[mailer.KeyPort] = strconv.Itoa(s.Cfg.SMTPPort)
	}
	if st[mailer.KeyUser] == "" {
		st[mailer.KeyUser] = s.Cfg.SMTPUser
	}
	if st[mailer.KeyFrom] == "" {
		st[mailer.KeyFrom] = s.Cfg.SMTPFrom
	}
	if st[mailer.KeyTLS] == "" {
		st[mailer.KeyTLS] = c.TLSMode
	}
	if st[mailer.KeyAllowedDomains] == "" {
		st[mailer.KeyAllowedDomains] = strings.Join(s.Cfg.EmailAllowedDomains, ",")
	}
	// 端口回数字而不是字符串：前端表单是 InputNumber，给字符串会来回转换出错。
	port := c.Port
	if v := st[mailer.KeyPort]; v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			port = n
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"smtp_host":            st[mailer.KeyHost],
		"smtp_port":            port,
		"smtp_user":            st[mailer.KeyUser],
		"smtp_from":            st[mailer.KeyFrom],
		"smtp_tls":             st[mailer.KeyTLS],
		"mail_allowed_domains": st[mailer.KeyAllowedDomains],
		// 密码只回「有没有」。改密码是覆盖写，不需要先读出来。
		"smtp_password_set": st[mailer.KeyPass] != "" || s.Cfg.SMTPPass != "",
		"configured":        c.Enabled(),
		"effective_from":    c.EffectiveFrom(),
		"limits": map[string]interface{}{
			"max_recipients": c.MaxRecipients,
			"max_attach_mb":  s.Cfg.EmailMaxAttachMB,
			"max_body_chars": c.MaxBodyChars,
		},
	})
}

// handleMailSettingsPut PUT /admin/mail/settings：保存配置。
func (s *Server) handleMailSettingsPut(w http.ResponseWriter, r *http.Request) {
	p := s.requireAdmin(w, r)
	if p == nil {
		return
	}
	if s.Mailer == nil {
		writeErr(w, http.StatusServiceUnavailable, "internal_error", "邮件模块未初始化", nil)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var in map[string]interface{}
	if err := json.Unmarshal(body, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "data_invalid", "请求体不是 JSON 对象", nil)
		return
	}
	out := map[string]string{}
	for _, k := range []string{mailer.KeyHost, mailer.KeyPort, mailer.KeyUser, mailer.KeyPass,
		mailer.KeyFrom, mailer.KeyTLS, mailer.KeyAllowedDomains} {
		v, ok := in[k]
		if !ok {
			continue
		}
		var sv string
		switch t := v.(type) {
		case string:
			sv = t
		case float64:
			sv = strconv.FormatFloat(t, 'f', -1, 64)
		default:
			writeErr(w, http.StatusBadRequest, "data_invalid", k+" 类型不合法", nil)
			return
		}
		out[k] = strings.TrimSpace(sv)
	}
	if v := out[mailer.KeyTLS]; v != "" {
		switch strings.ToLower(v) {
		case mailer.TLSDirect, mailer.TLSUpgrade, mailer.TLSNone:
		default:
			writeErr(w, http.StatusBadRequest, "data_invalid",
				"smtp_tls 取值非法："+v+"（允许 tls|starttls|none）", nil)
			return
		}
	}
	if v := out[mailer.KeyPort]; v != "" {
		if n, err := strconv.Atoi(v); err != nil || n <= 0 || n > 65535 {
			writeErr(w, http.StatusBadRequest, "data_invalid", "smtp_port 必须是 1-65535 的整数", nil)
			return
		}
	}
	if err := s.Mailer.Save(out); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", "保存失败："+err.Error(), nil)
		return
	}
	_, passSaved := out[mailer.KeyPass]
	s.audit(p, "mail.settings.update", "setting", "smtp", map[string]interface{}{
		"keys":       sortedKeys(out),
		"pass_saved": passSaved,
	})
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "saved": sortedKeys(out)})
}

// handleMailTest POST /admin/mail/test：发一封测试邮件验链路。
//
// 刻意**不受收件人域名白名单限制**：白名单是给 API 调用方准备的笼子，
// 运维自己验链路时不该被自己还没填完的白名单挡住。
func (s *Server) handleMailTest(w http.ResponseWriter, r *http.Request) {
	p := s.requireAdmin(w, r)
	if p == nil {
		return
	}
	if s.Mailer == nil {
		writeErr(w, http.StatusServiceUnavailable, "internal_error", "邮件模块未初始化", nil)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var in struct {
		To string `json:"to"`
	}
	_ = json.Unmarshal(body, &in)
	to := strings.TrimSpace(in.To)
	if to == "" || !strings.Contains(to, "@") {
		writeErr(w, http.StatusBadRequest, "data_invalid", "to 必填且须为合法邮箱地址", nil)
		return
	}
	c := s.Mailer.Load()
	if !c.Enabled() {
		writeErr(w, http.StatusUnprocessableEntity, "mail_not_configured",
			"邮件服务未配置：请先填写 SMTP 主机与账号再测试", nil)
		return
	}
	msg := mailer.Message{
		To:      []string{to},
		Subject: "铸版 · 邮件服务连通性测试",
		Body: fmt.Sprintf("这是一封测试邮件。\n\n"+
			"收到它说明 SMTP 配置可用：主机 %s、端口 %d、加密 %s、发件人 %s。\n"+
			"API 投递时生效的收件人域名白名单：%s\n",
			c.Host, c.Port, c.TLSMode, c.EffectiveFrom(),
			strings.Join(c.AllowedDomains, ",")),
	}
	// 审计只此一条，由 Sender 在投递结束时写（action=mail.deliver，
	// source=admin_test）。这里不再额外记一条 mail.test：同一次发信留两条
	// 记录，事后对不上时反而要花时间判断哪条算数。
	actorName := p.User.DisplayName
	if actorName == "" {
		actorName = p.User.Username
	}
	err := s.Mailer.SendTest(mailer.Outbox{Source: "admin_test", Message: msg,
		ActorUserID: p.User.ID, ActorName: actorName})
	if err != nil {
		writeErr(w, http.StatusBadGateway, "mail_send_failed", "发送失败："+err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "to": to})
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}
