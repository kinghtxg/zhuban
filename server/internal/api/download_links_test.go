package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"docforge/server/internal/auth"
	"docforge/server/internal/models"
	"docforge/server/internal/sign"

	"github.com/go-chi/chi/v5"
)

// 这一组用例守的是一个曾经的线上故障：
// format=pdf 的任务 DocxPath 为空，但响应里的 download_url 仍指向 docx，
// 客户端下到 410 + 82 字节错误 JSON，于是「PDF 输出坏了」。
// 产物一直是好的，坏的是链接。

func TestDocURLs(t *testing.T) {
	cases := []struct {
		name        string
		docx, pdf   bool
		wantPrimary string
		wantPDF     string
	}{
		{"docx-only", true, false, "/api/v1/documents/dc_x/download", ""},
		{"pdf-only", false, true, "/api/v1/documents/dc_x/download?format=pdf", "/api/v1/documents/dc_x/download?format=pdf"},
		{"both", true, true, "/api/v1/documents/dc_x/download", "/api/v1/documents/dc_x/download?format=pdf"},
		{"neither", false, false, "/api/v1/documents/dc_x/download", ""},
	}
	for _, c := range cases {
		d := models.Document{ID: "dc_x"}
		if c.docx {
			d.DocxPath = "/data/out.docx"
		}
		if c.pdf {
			d.PdfPath = "/data/out.pdf"
		}
		primary, pdfURL := docURLs(d)
		if primary != c.wantPrimary {
			t.Errorf("%s: download_url=%q want %q", c.name, primary, c.wantPrimary)
		}
		if pdfURL != c.wantPDF {
			t.Errorf("%s: pdf_download_url=%q want %q", c.name, pdfURL, c.wantPDF)
		}
	}
}

// asTestUser 给测试路由装一个固定主体。
//
// 加了产物归属隔离之后，/jobs/{id} 与 /documents/{id}/download 都要求
// 「登录 + 是发起人」。这组用例测的是链接拼装，不是权限，所以统一用一个
// 固定用户当发起人，把权限这层让过去——权限本身由 authz_test.go 覆盖。
func asTestUser(r chi.Router, uid string) {
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			p := &auth.Principal{User: &models.User{ID: uid, Role: "member"}}
			next.ServeHTTP(w, req.WithContext(auth.WithPrincipal(req.Context(), p)))
		})
	})
}

// TestSyncResponsePDFOnlyPrimaryLink 断言 PDF-only 任务的 download_url 真的能下到东西。
func TestSyncResponsePDFOnlyPrimaryLink(t *testing.T) {
	s, _ := setupServer(t)
	s.Signer = sign.New("test-secret-for-docurls", time.Hour)

	dir := t.TempDir()
	pdfPath := filepath.Join(dir, "out.pdf")
	if err := os.WriteFile(pdfPath, []byte("%PDF-1.7\nfake"), 0o644); err != nil {
		t.Fatal(err)
	}

	job := models.Job{ID: "jb_pdfonly", TemplateID: "tpl_1", VersionID: "ver_1", Status: "succeeded",
		Total: 1, Succeeded: 1, Format: "pdf", CreatedByUserID: "u_link"}
	doc := models.Document{
		ID: "dc_pdfonly", JobID: job.ID, RecordIndex: 0, Filename: "报告.pdf",
		PdfPath: pdfPath, Status: "succeeded", InputData: models.JSONB(`{}`),
	}
	if err := s.DB.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Create(&doc).Error; err != nil {
		t.Fatal(err)
	}

	resp := s.syncResponse(&job)
	docs, _ := resp["documents"].([]map[string]interface{})
	if len(docs) != 1 {
		t.Fatalf("documents 条数 = %d, want 1", len(docs))
	}
	downloadURL, _ := docs[0]["download_url"].(string)
	if downloadURL != "/api/v1/documents/dc_pdfonly/download?format=pdf" {
		t.Fatalf("PDF-only 任务的主下载链接没有带 format=pdf：%q（下载会拿到 410）", downloadURL)
	}

	// 真的走一遍端点：主链接必须返回 PDF 字节，而不是错误信封
	r := chi.NewRouter()
	asTestUser(r, "u_link")
	r.Get("/api/v1/documents/{id}/download", s.handleDocumentDownload)
	req := httptest.NewRequest(http.MethodGet, downloadURL, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("主链接 status=%d body=%s", w.Code, w.Body.String())
	}
	if got := w.Body.Bytes(); len(got) < 5 || string(got[:4]) != "%PDF" {
		t.Fatalf("主链接返回的不是 PDF：%q", got[:min(40, len(got))])
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/pdf" {
		t.Fatalf("Content-Type=%q, want application/pdf", ct)
	}
}

// TestDocumentDownloadFormatNotAvailable 区分「没生成这种格式」与「文件已清理」。
func TestDocumentDownloadFormatNotAvailable(t *testing.T) {
	s, _ := setupServer(t)
	dir := t.TempDir()
	docxPath := filepath.Join(dir, "out.docx")
	if err := os.WriteFile(docxPath, []byte("PK\x03\x04fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	doc := models.Document{ID: "dc_docxonly", JobID: "jb_docxonly", Filename: "报告.docx", DocxPath: docxPath, Status: "succeeded"}
	job := models.Job{ID: "jb_docxonly", TemplateID: "tpl_1", VersionID: "ver_1", Status: "succeeded",
		Total: 1, Succeeded: 1, Format: "docx", CreatedByUserID: "u_link"}
	if err := s.DB.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Create(&doc).Error; err != nil {
		t.Fatal(err)
	}

	r := chi.NewRouter()
	asTestUser(r, "u_link")
	r.Get("/api/v1/documents/{id}/download", s.handleDocumentDownload)

	// 要 pdf 但没有 → 404 format_not_available，不能是 410 expired
	req := httptest.NewRequest(http.MethodGet, "/api/v1/documents/dc_docxonly/download?format=pdf", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("未生成 PDF 时 status=%d, want 404（body=%s）", w.Code, w.Body.String())
	}
	if !contains(w.Body.String(), "format_not_available") {
		t.Fatalf("错误码不是 format_not_available：%s", w.Body.String())
	}

	// 要 docx 且存在 → 200
	req = httptest.NewRequest(http.MethodGet, "/api/v1/documents/dc_docxonly/download", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("docx 下载 status=%d", w.Code)
	}
}

// TestJobDetailCarriesPDFLink 任务详情里的产物也要给出 pdf 链接。
func TestJobDetailCarriesPDFLink(t *testing.T) {
	s, _ := setupServer(t)
	s.Signer = sign.New("test-secret-jobdetail", time.Hour)
	job := models.Job{ID: "jb_both", TemplateID: "tpl_1", VersionID: "ver_1", Status: "succeeded",
		Total: 1, Succeeded: 1, Format: "both", CreatedByUserID: "u_link"}
	doc := models.Document{
		ID: "dc_both", JobID: job.ID, RecordIndex: 0, Filename: "报告.docx",
		DocxPath: "/data/a.docx", PdfPath: "/data/a.pdf", Status: "succeeded",
	}
	if err := s.DB.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Create(&doc).Error; err != nil {
		t.Fatal(err)
	}

	r := chi.NewRouter()
	asTestUser(r, "u_link")
	r.Get("/api/v1/jobs/{id}", s.handleJobDetail)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/jb_both", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	body := w.Body.String()
	for _, want := range []string{
		`"download_url":"/api/v1/documents/dc_both/download"`,
		`"pdf_download_url":"/api/v1/documents/dc_both/download?format=pdf"`,
	} {
		if !contains(body, want) {
			t.Fatalf("任务详情缺少 %s\n%s", want, body)
		}
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
