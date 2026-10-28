package api

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"docforge/server/internal/auth"
	"docforge/server/internal/models"

	"github.com/go-chi/chi/v5"
)

// 这一组守的是「上传新版本时，谁传的、改了什么」这两件事有没有真的记下来。
//
// 背景：handleAddVersion 里 UploadedBy 一直被写死成 "system"，
// 而 change_summary 从来没从表单里读过。页面上于是出现两种症状：
//   - 版本时间线每一版都署名 system，多人共用一个模板时没法追责；
//   - 「这一版改了什么」永远是空的，隔两周没人说得清。
// 两个字段都有，只是没人往里写。

// postVersion 用真实的多路由由把 multipart 请求打进去。
// 不用直接调 handler：URL 参数来自 chi 的路由上下文，绕过路由就拿不到 {id}。
func postVersion(t *testing.T, s *Server, tplID string, p *auth.Principal, fields map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "v.docx")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write([]byte("DOCX-BYTES")); err != nil {
		t.Fatal(err)
	}
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(auth.WithPrincipal(req.Context(), p)))
		})
	})
	r.Post("/templates/{id}/versions", s.handleAddVersion)

	req := httptest.NewRequest(http.MethodPost, "/templates/"+tplID+"/versions", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// latestVersion 取这个模板最新插入的版本行。
func latestVersion(t *testing.T, s *Server, tplID string) models.TemplateVersion {
	t.Helper()
	var v models.TemplateVersion
	if err := s.DB.Where("template_id = ?", tplID).Order("created_at desc").First(&v).Error; err != nil {
		t.Fatalf("读取新版本失败：%v", err)
	}
	return v
}

func TestAddVersion_RecordsUploaderAndSummary(t *testing.T) {
	s, _ := setupServer(t)
	seedTemplate(t, s, "tpl_up", "ver_up", "1.0.0", "u_owner", true)

	p := &auth.Principal{User: &models.User{ID: "u_alice", Username: "alice", Role: "member"}}
	rec := postVersion(t, s, "tpl_up", p, map[string]string{
		"activate": "true",
		// 前后各留一个空格：入库前要 trim，否则页面上会显示成「· 补充签署页」这种带空隙的样子
		"change_summary": "  补充签署页  ",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("上传版本应返回 201，实际 %d：%s", rec.Code, rec.Body.String())
	}

	v := latestVersion(t, s, "tpl_up")
	if v.UploadedBy != "u_alice" {
		t.Errorf("上传人应记为 u_alice，实际 %q（写死 system 会让时间线失去追责能力）", v.UploadedBy)
	}
	if v.ChangeSummary != "补充签署页" {
		t.Errorf("变更说明应被 trim 后入库，实际 %q", v.ChangeSummary)
	}
}

func TestAddVersion_ChangeSummaryOptional(t *testing.T) {
	s, _ := setupServer(t)
	seedTemplate(t, s, "tpl_nosum", "ver_nosum", "1.0.0", "u_owner", true)

	p := &auth.Principal{User: &models.User{ID: "u_bob", Role: "member"}}
	rec := postVersion(t, s, "tpl_nosum", p, map[string]string{"activate": "true"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("不填变更说明也应上传成功，实际 %d：%s", rec.Code, rec.Body.String())
	}

	v := latestVersion(t, s, "tpl_nosum")
	// 不能默认填「上传新版本」这类空话：时间线上写满空话等于没有信息。
	if v.ChangeSummary != "" {
		t.Errorf("未填写时变更说明应为空，实际 %q", v.ChangeSummary)
	}
	if v.UploadedBy != "u_bob" {
		t.Errorf("上传人应记为 u_bob，实际 %q", v.UploadedBy)
	}
}

func TestAddVersion_APIKeyPrincipalHasNoUploader(t *testing.T) {
	s, _ := setupServer(t)
	seedTemplate(t, s, "tpl_key", "ver_key", "1.0.0", "u_owner", true)

	// API Key 调用时 Principal 里只有 APIKey，没有 User。
	// 这时应该留空（页面显示「未知」），而不是编一个 "system" 出来。
	p := &auth.Principal{APIKey: &models.APIKey{ID: "ak_1", UserID: "u_owner"}}
	rec := postVersion(t, s, "tpl_key", p, map[string]string{"activate": "true"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("API Key 上传版本应返回 201，实际 %d：%s", rec.Code, rec.Body.String())
	}

	v := latestVersion(t, s, "tpl_key")
	if v.UploadedBy != "" {
		t.Errorf("API Key 场景上传人应留空，实际 %q", v.UploadedBy)
	}
}

func TestAddVersion_NoPrincipalDoesNotPanic(t *testing.T) {
	s, _ := setupServer(t)
	seedTemplate(t, s, "tpl_nil", "ver_nil", "1.0.0", "u_owner", true)

	// 直接调 handler 而不带 principal：这条路径在线上走不到（路由在鉴权组里），
	// 但 handler 一旦 nil 解引用就是 500，守住它比信任"走不到"更便宜。
	r := chi.NewRouter()
	r.Post("/templates/{id}/versions", s.handleAddVersion)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "v.docx")
	_, _ = fw.Write([]byte("DOCX-BYTES"))
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/templates/tpl_nil/versions", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req.WithContext(context.Background()))

	if rec.Code != http.StatusCreated {
		t.Fatalf("无 principal 时不应崩溃，期望 201，实际 %d：%s", rec.Code, rec.Body.String())
	}
}
