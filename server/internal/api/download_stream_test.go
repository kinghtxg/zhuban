package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"docforge/server/internal/auth"
	"docforge/server/internal/models"
)

// 这一组守的是「大包下载不会把整包读进内存」这件事的外部可观测证据。
//
// 内存峰值没法在单测里直接断言，但流式实现有两个必然的副作用可以断言：
//  1. 响应带 Content-Length（整包读内存再写也能带，但流式下才有意义）；
//  2. 支持 Range 请求 —— 这是区分「流式 + Seek」和「一把 ReadFile 再 Write」
//     最硬的判据：后者根本不认 Range，只会回 200 全量。

func seedZipJob(t *testing.T, s *Server, jobID, owner string, content []byte) *models.Job {
	t.Helper()
	zipPath := filepath.Join(s.Cfg.DataDir, "jobs", jobID+".zip")
	if err := os.MkdirAll(filepath.Dir(zipPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(zipPath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	job := &models.Job{
		ID: jobID, TemplateID: "tpl_x", VersionID: "ver_x",
		Status: "succeeded", Total: 1, Succeeded: 1,
		CreatedByUserID: owner, Source: "batch", Format: "docx",
		ZipPath: zipPath,
	}
	if err := s.DB.Create(job).Error; err != nil {
		t.Fatal(err)
	}
	return job
}

func TestJobDownload_SetsContentLength(t *testing.T) {
	s, _ := setupServer(t)
	owner := "u_owner"
	// 2MB 内容：够大到「整包读内存」和「流式」在行为上仍一致，
	// 但足以让 Content-Length 明显不是 0。
	content := bytes.Repeat([]byte("PK\x03\x04"), 512*1024)
	job := seedZipJob(t, s, "jb_big", owner, content)

	req := withParam(httptest.NewRequest("GET", "/api/v1/jobs/jb_big/download", nil), "id", job.ID)
	req = withPrincipal(req, &auth.Principal{User: &models.User{ID: owner, Role: "member"}})
	w := httptest.NewRecorder()
	s.handleJobDownload(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("下载应返回 200，实际 %d：%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Length"); got != itoa(len(content)) {
		t.Errorf("Content-Length 应为 %d，实际 %q（没有它客户端看不到下载进度）", len(content), got)
	}
	if w.Body.Len() != len(content) {
		t.Errorf("响应体长度应为 %d，实际 %d", len(content), w.Body.Len())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/zip" {
		t.Errorf("Content-Type 应为 application/zip，实际 %q", ct)
	}
}

func TestJobDownload_SupportsRange(t *testing.T) {
	s, _ := setupServer(t)
	owner := "u_owner"
	content := []byte("0123456789ABCDEFGHIJ")
	job := seedZipJob(t, s, "jb_range", owner, content)

	req := withParam(httptest.NewRequest("GET", "/api/v1/jobs/jb_range/download", nil), "id", job.ID)
	req.Header.Set("Range", "bytes=5-9")
	req = withPrincipal(req, &auth.Principal{User: &models.User{ID: owner, Role: "member"}})
	w := httptest.NewRecorder()
	s.handleJobDownload(w, req)

	// 206 是分段的正确回法。如果这里拿到 200 全量，说明还是 ReadFile + Write：
	// 那种实现没法断点续传，下载器重试一次就要重传整个包。
	if w.Code != http.StatusPartialContent {
		t.Fatalf("Range 请求应返回 206，实际 %d", w.Code)
	}
	if w.Body.String() != "56789" {
		t.Errorf("Range 5-9 应返回 56789，实际 %q", w.Body.String())
	}
	if cr := w.Header().Get("Content-Range"); cr != "bytes 5-9/20" {
		t.Errorf("Content-Range 应为 bytes 5-9/20，实际 %q", cr)
	}
}

func TestJobDownload_MissingFileIsGone(t *testing.T) {
	s, _ := setupServer(t)
	owner := "u_owner"
	job := seedZipJob(t, s, "jb_missing", owner, []byte("x"))
	// 文件被保留期清理掉，但库里还留着路径——这时必须给 410，而不是 500。
	if err := os.Remove(job.ZipPath); err != nil {
		t.Fatal(err)
	}

	req := withParam(httptest.NewRequest("GET", "/api/v1/jobs/jb_missing/download", nil), "id", job.ID)
	req = withPrincipal(req, &auth.Principal{User: &models.User{ID: owner, Role: "member"}})
	w := httptest.NewRecorder()
	s.handleJobDownload(w, req)

	if w.Code != http.StatusGone {
		t.Fatalf("产物不存在应返回 410，实际 %d：%s", w.Code, w.Body.String())
	}
}

// ---- 单份文档下载 ----
//
// 单独一组，是因为这条路径一开始被漏掉了：只把批量 zip 改成了流式，
// 同一份文件里单文档下载还在 ReadFile + Write。「同类代码只改一半」是审查里最典型的漏项——
// 单份产物看起来只有几百 KB，但模板里贴几十张高清图就能到几十 MB，而且它同样走公网直链。

func seedDoc(t *testing.T, s *Server, docID, jobID, owner string, content []byte) *models.Document {
	t.Helper()
	// job 的归属决定了 canSeeDocument 的结果，留空会让整个请求先变成 404，
	// 那样后面测的就成了「可见性」而不是「下载」。
	job := &models.Job{
		ID: jobID, TemplateID: "tpl_x", VersionID: "ver_x",
		Status: "succeeded", Total: 1, Succeeded: 1,
		CreatedByUserID: owner, Source: "ui", Format: "docx",
	}
	if err := s.DB.Create(job).Error; err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(s.Cfg.DataDir, "docs", jobID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, docID+".docx")
	if err := os.WriteFile(p, content, 0o644); err != nil {
		t.Fatal(err)
	}
	doc := &models.Document{
		ID: docID, JobID: jobID, Filename: "测试报告.docx",
		DocxPath: p, Status: "succeeded",
	}
	if err := s.DB.Create(doc).Error; err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestDocumentDownload_SupportsRange(t *testing.T) {
	s, _ := setupServer(t)
	owner := "u_owner"
	content := []byte("0123456789ABCDEFGHIJ")
	doc := seedDoc(t, s, "doc_range", "jb_doc_range", owner, content)

	req := withParam(httptest.NewRequest("GET", "/api/v1/documents/doc_range/download?format=docx", nil), "id", doc.ID)
	req.Header.Set("Range", "bytes=5-9")
	req = withPrincipal(req, &auth.Principal{User: &models.User{ID: owner, Role: "member"}})
	w := httptest.NewRecorder()
	s.handleDocumentDownload(w, req)

	if w.Code != http.StatusPartialContent {
		t.Fatalf("Range 请求应返回 206，实际 %d：%s（200 说明还在 ReadFile + Write）", w.Code, w.Body.String())
	}
	if w.Body.String() != "56789" {
		t.Errorf("Range 5-9 应返回 56789，实际 %q", w.Body.String())
	}
	if cr := w.Header().Get("Content-Range"); cr != "bytes 5-9/20" {
		t.Errorf("Content-Range 应为 bytes 5-9/20，实际 %q", cr)
	}

	var after models.Document
	s.DB.First(&after, "id = ?", doc.ID)
	if after.DownloadCount != 1 {
		t.Errorf("下载成功后计数应为 1，实际 %d", after.DownloadCount)
	}
}

func TestDocumentDownload_NoCountWhenFileGone(t *testing.T) {
	s, _ := setupServer(t)
	owner := "u_owner"
	doc := seedDoc(t, s, "doc_gone", "jb_doc_gone", owner, []byte("abc"))
	if err := os.Remove(doc.DocxPath); err != nil {
		t.Fatal(err)
	}

	req := withParam(httptest.NewRequest("GET", "/api/v1/documents/doc_gone/download?format=docx", nil), "id", doc.ID)
	req = withPrincipal(req, &auth.Principal{User: &models.User{ID: owner, Role: "member"}})
	w := httptest.NewRecorder()
	s.handleDocumentDownload(w, req)

	if w.Code != http.StatusGone {
		t.Fatalf("文件已清理应返回 410，实际 %d：%s", w.Code, w.Body.String())
	}
	// 计数挂在「文件确实打开了」之后。放前面的话，文件被清理之后每一次重试
	// 都会给一个根本没下到东西的请求 +1，把「这份文件被下载过几次」污染成「被请求过几次」。
	var after models.Document
	s.DB.First(&after, "id = ?", doc.ID)
	if after.DownloadCount != 0 {
		t.Errorf("文件打不开时不该计数，实际 %d", after.DownloadCount)
	}
}
