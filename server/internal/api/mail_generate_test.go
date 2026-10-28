package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"docforge/server/internal/auth"
	"docforge/server/internal/config"
	"docforge/server/internal/mailer"
	"docforge/server/internal/models"
	"docforge/server/internal/sign"
)

// 这一组测试守的是「API 侧能指定什么」这条边界：
// 调用方只能给收件人/主题/正文/附件模式，SMTP 凭据永远在服务端。
// 另外三条硬规则也在这里钉死：没配 SMTP 就 422、域名必须过白名单、
// emails 与 records 长度必须一致。

func setupMailServer(t *testing.T, domains ...string) (*Server, *config.Config) {
	t.Helper()
	s, cfg := setupServer(t)
	s.Signer = sign.New("test-secret-mail", time.Hour)
	cfg.EmailAllowedDomains = domains
	cfg.MaxRecords = 100
	cfg.MaxJSONMB = 10
	cfg.MaxImageMB = 5
	s.Mailer = mailer.New(s.DB, cfg)
	return s, cfg
}

func seedMailTemplate(t *testing.T, s *Server) string {
	t.Helper()
	tpl := seedTemplate(t, s, "tpl_mail", "ver_mail", "v1", "u1", true)
	if err := s.DB.Create(&models.User{ID: "u1", Username: "u1", Role: "admin",
		Email: "u1@example.com"}).Error; err != nil {
		t.Fatal(err)
	}
	return tpl.Code
}

func postGenerate(t *testing.T, s *Server, body map[string]interface{}, withUser bool) *httptest.ResponseRecorder {
	t.Helper()
	// 显式 async：测试环境没有 worker 在跑，走同步会白等 30s。
	if _, ok := body["sync"]; !ok {
		f := false
		body["sync"] = &f
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/generate", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if withUser {
		req = req.WithContext(auth.WithPrincipal(context.Background(),
			&auth.Principal{User: &models.User{ID: "u1", Role: "admin"}}))
	}
	w := httptest.NewRecorder()
	s.handleGenerate(w, req)
	return w
}

// TestGenerateEmail_NotConfigured 没配 SMTP 就别让任务跑起来再失败。
func TestGenerateEmail_NotConfigured(t *testing.T) {
	s, _ := setupMailServer(t, "example.com")
	code := seedMailTemplate(t, s)

	w := postGenerate(t, s, map[string]interface{}{
		"template_code": code,
		"data":          map[string]interface{}{"name": "张三"},
		"email":         map[string]interface{}{"to": []string{"a@example.com"}, "subject": "对账单"},
	}, true)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("未配 SMTP 时应 422，实得 %d（%s）", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "mail_not_configured") {
		t.Fatalf("错误码应为 mail_not_configured：%s", w.Body.String())
	}
	// 关键：不能留下一个悬空任务
	var n int64
	s.DB.Model(&models.Job{}).Count(&n)
	if n != 0 {
		t.Fatalf("预检失败却创建了 %d 个任务", n)
	}
}

// TestGenerateEmail_DomainNotAllowed 白名单是 API 侧的笼子。
func TestGenerateEmail_DomainNotAllowed(t *testing.T) {
	s, _ := setupMailServer(t, "example.com")
	if err := s.Mailer.Save(map[string]string{
		mailer.KeyHost: "smtp.example.com", mailer.KeyPort: "587",
		mailer.KeyTLS: mailer.TLSUpgrade, mailer.KeyFrom: "noreply@example.com",
	}); err != nil {
		t.Fatal(err)
	}
	code := seedMailTemplate(t, s)

	w := postGenerate(t, s, map[string]interface{}{
		"template_code": code,
		"data":          map[string]interface{}{"name": "张三"},
		"email":         map[string]interface{}{"to": []string{"someone@evil.com"}},
	}, true)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("名单外域名应 422，实得 %d（%s）", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "不在允许名单内") {
		t.Fatalf("错误信息应说明域名不合规：%s", w.Body.String())
	}
}

// TestGenerateEmail_WhitelistEmptyIsFailClosed 白名单没配 → 一律拒绝，不默认放行。
func TestGenerateEmail_WhitelistEmptyIsFailClosed(t *testing.T) {
	s, _ := setupMailServer(t) // 不给任何域名
	if err := s.Mailer.Save(map[string]string{
		mailer.KeyHost: "smtp.example.com", mailer.KeyPort: "587",
		mailer.KeyFrom: "noreply@example.com",
	}); err != nil {
		t.Fatal(err)
	}
	code := seedMailTemplate(t, s)

	w := postGenerate(t, s, map[string]interface{}{
		"template_code": code,
		"data":          map[string]interface{}{"name": "张三"},
		"email":         map[string]interface{}{"to": []string{"anyone@example.com"}},
	}, true)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("白名单为空时必须 fail-closed，实得 %d（%s）", w.Code, w.Body.String())
	}
}

// TestGenerateEmail_LengthMismatch emails 与 records 长度对不上就报错，不猜。
func TestGenerateEmail_LengthMismatch(t *testing.T) {
	s, _ := setupMailServer(t, "example.com")
	if err := s.Mailer.Save(map[string]string{
		mailer.KeyHost: "smtp.example.com", mailer.KeyPort: "587",
		mailer.KeyFrom: "noreply@example.com",
	}); err != nil {
		t.Fatal(err)
	}
	code := seedMailTemplate(t, s)

	w := postGenerate(t, s, map[string]interface{}{
		"template_code": code,
		"records":       []map[string]interface{}{{"name": "A"}, {"name": "B"}},
		"emails": []map[string]interface{}{
			{"to": []string{"a@example.com"}},
		},
	}, true)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("长度不一致应 422，实得 %d（%s）", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "必须等于") {
		t.Fatalf("错误信息应说明长度约束：%s", w.Body.String())
	}
}

// TestGenerateEmail_AcceptedWritesPending 合法请求：任务落库、状态 pending、响应可见。
func TestGenerateEmail_AcceptedWritesPending(t *testing.T) {
	s, _ := setupMailServer(t, "example.com")
	if err := s.Mailer.Save(map[string]string{
		mailer.KeyHost: "smtp.example.com", mailer.KeyPort: "587",
		mailer.KeyFrom: "noreply@example.com",
	}); err != nil {
		t.Fatal(err)
	}
	code := seedMailTemplate(t, s)

	w := postGenerate(t, s, map[string]interface{}{
		"template_code": code,
		"data":          map[string]interface{}{"name": "张三"},
		"email": map[string]interface{}{
			"to": []string{"a@example.com"}, "subject": "{{ name }} 的对账单",
			"body": "请查收", "attach": "pdf",
		},
	}, true)
	if w.Code != http.StatusAccepted && w.Code != http.StatusOK {
		t.Fatalf("合法请求不该失败：%d（%s）", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "email_status") {
		t.Fatalf("响应里应能直接看到投递状态：%s", body)
	}
	if !strings.Contains(body, "pending") {
		t.Fatalf("刚创建的任务邮件状态应为 pending：%s", body)
	}

	var job models.Job
	if err := s.DB.Order("created_at desc").First(&job).Error; err != nil {
		t.Fatalf("任务未落库：%v", err)
	}
	if job.EmailStatus != "pending" {
		t.Fatalf("job.email_status=%q，期望 pending", job.EmailStatus)
	}
	var spec mailer.EmailSpec
	if err := json.Unmarshal(job.EmailSpec, &spec); err != nil {
		t.Fatalf("email_spec 落库格式不对：%v", err)
	}
	if len(spec.To) != 1 || spec.To[0] != "a@example.com" || spec.Attach != "pdf" {
		t.Fatalf("落库规格不对：%+v", spec)
	}
}

// TestGenerateEmail_SpecRejectsBadAddress 收件人地址格式在入库前就要拦住。
func TestGenerateEmail_SpecRejectsBadAddress(t *testing.T) {
	s, _ := setupMailServer(t, "example.com")
	if err := s.Mailer.Save(map[string]string{
		mailer.KeyHost: "smtp.example.com", mailer.KeyPort: "587",
		mailer.KeyFrom: "noreply@example.com",
	}); err != nil {
		t.Fatal(err)
	}
	code := seedMailTemplate(t, s)

	w := postGenerate(t, s, map[string]interface{}{
		"template_code": code,
		"data":          map[string]interface{}{"name": "张三"},
		"email":         map[string]interface{}{"to": []string{"zhangsan"}},
	}, true)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("非法地址应 422，实得 %d（%s）", w.Code, w.Body.String())
	}
}

// TestGenerateEmail_AttachModeInvalid 附件模式必须是枚举值。
func TestGenerateEmail_AttachModeInvalid(t *testing.T) {
	s, _ := setupMailServer(t, "example.com")
	if err := s.Mailer.Save(map[string]string{
		mailer.KeyHost: "smtp.example.com", mailer.KeyPort: "587",
		mailer.KeyFrom: "noreply@example.com",
	}); err != nil {
		t.Fatal(err)
	}
	code := seedMailTemplate(t, s)

	w := postGenerate(t, s, map[string]interface{}{
		"template_code": code,
		"data":          map[string]interface{}{"name": "张三"},
		"email": map[string]interface{}{
			"to": []string{"a@example.com"}, "attach": "rar",
		},
	}, true)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("非法附件模式应 422，实得 %d（%s）", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "email.attach") {
		t.Fatalf("错误信息应指向 email.attach：%s", w.Body.String())
	}
}

// TestGenerateEmail_PerRecordSpecsStored 每条记录各发一封，规格落在 document 上。
func TestGenerateEmail_PerRecordSpecsStored(t *testing.T) {
	s, _ := setupMailServer(t, "example.com")
	if err := s.Mailer.Save(map[string]string{
		mailer.KeyHost: "smtp.example.com", mailer.KeyPort: "587",
		mailer.KeyFrom: "noreply@example.com",
	}); err != nil {
		t.Fatal(err)
	}
	code := seedMailTemplate(t, s)

	w := postGenerate(t, s, map[string]interface{}{
		"template_code": code,
		"records": []map[string]interface{}{
			{"name": "A", "email": "a@example.com"},
			{"name": "B", "email": "b@example.com"},
		},
		"emails": []map[string]interface{}{
			{"to": []string{"a@example.com"}, "subject": "A 的单据"},
			{"to": []string{"b@example.com"}, "subject": "B 的单据"},
		},
	}, true)
	if w.Code != http.StatusAccepted && w.Code != http.StatusOK {
		t.Fatalf("合法请求不该失败：%d（%s）", w.Code, w.Body.String())
	}
	var job models.Job
	if err := s.DB.Order("created_at desc").First(&job).Error; err != nil {
		t.Fatal(err)
	}
	var docs []models.Document
	s.DB.Where("job_id = ?", job.ID).Order("record_index").Find(&docs)
	if len(docs) != 2 {
		t.Fatalf("应有 2 份文档，实得 %d", len(docs))
	}
	for i, d := range docs {
		if d.EmailStatus != "pending" {
			t.Fatalf("doc[%d].email_status=%q，期望 pending", i, d.EmailStatus)
		}
		var spec mailer.EmailSpec
		if err := json.Unmarshal(d.EmailSpec, &spec); err != nil {
			t.Fatalf("doc[%d] 规格落库格式不对：%v", i, err)
		}
		if len(spec.To) != 1 {
			t.Fatalf("doc[%d] 收件人不对：%v", i, spec.To)
		}
	}
}

// TestGenerateEmail_NoEmailFieldLeavesStatusEmpty 不填 email 时响应不该凭空多出邮件字段。
func TestGenerateEmail_NoEmailFieldLeavesStatusEmpty(t *testing.T) {
	s, _ := setupMailServer(t, "example.com")
	code := seedMailTemplate(t, s)

	w := postGenerate(t, s, map[string]interface{}{
		"template_code": code,
		"data":          map[string]interface{}{"name": "张三"},
	}, true)
	if w.Code != http.StatusAccepted && w.Code != http.StatusOK {
		t.Fatalf("不带邮件的请求不该失败：%d（%s）", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "email_status") {
		t.Fatalf("未请求发信时不该出现 email_status：%s", w.Body.String())
	}
}
