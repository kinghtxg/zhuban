package api

// 归属隔离的回归测试（job / document / signed-url）。
//
// 背景：上线审查时发现 /jobs/{id}、/jobs/{id}/download、/documents/{id}/download
// 只校验「登录了」，没校验「是不是你的」。任务 ID 形如 jb_20261026_0016 是按日递增的，
// 猜一个号就能拿到别人刚渲染出来的业务数据（姓名、证件号、审计结论）。
//
// 这组用例锁三件事：
//  1. 非发起人拿别人的 ID 一律 404（不是 403——403 等于承认这个 ID 存在）；
//  2. 管理员不受限（运维要能排查）；
//  3. 列表口径与详情口径一致（列表里看不到的，不能靠猜 ID 拿到）。

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"docforge/server/internal/auth"
	"docforge/server/internal/models"
	"docforge/server/internal/sign"

	"github.com/go-chi/chi/v5"
)

// withPrincipal 把主体注入请求上下文（绕过中间件，直接测 handler）。
func withPrincipal(r *http.Request, p *auth.Principal) *http.Request {
	return r.WithContext(auth.WithPrincipal(r.Context(), p))
}

// withParam 注入 chi 的 URL 参数。
func withParam(r *http.Request, key, val string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, val)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

// seedJob 造一个属于 owner 的任务，产物文件真实落盘，
// 这样「有权限就能下到字节」也是被验证过的，而不是只比状态码。
func seedJob(t *testing.T, s *Server, jobID, owner string) (*models.Job, *models.Document) {
	t.Helper()
	zipPath := filepath.Join(s.Cfg.DataDir, "jobs", jobID+".zip")
	if err := os.MkdirAll(filepath.Dir(zipPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(zipPath, []byte("PK-fake-zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	docxPath := filepath.Join(s.Cfg.DataDir, "jobs", jobID+"_001.docx")
	if err := os.WriteFile(docxPath, []byte("PK-fake-docx"), 0o644); err != nil {
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
	doc := &models.Document{
		ID: "dc_" + jobID, JobID: jobID, RecordIndex: 1,
		Filename: "x.docx", Status: "succeeded", DocxPath: docxPath,
	}
	if err := s.DB.Create(doc).Error; err != nil {
		t.Fatal(err)
	}
	return job, doc
}

func TestJobDetail_RejectsNonOwner(t *testing.T) {
	s, _ := setupServer(t)
	s.Signer = sign.New("authz-test-secret", time.Hour)
	job, _ := seedJob(t, s, "jb_owner_1", "u_owner")

	outsider := &auth.Principal{User: &models.User{ID: "u_outsider", Role: "member"}}
	req := withParam(httptest.NewRequest(http.MethodGet, "/jobs/"+job.ID, nil), "id", job.ID)
	req = withPrincipal(req, outsider)
	w := httptest.NewRecorder()
	s.handleJobDetail(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("非发起人读他人任务应 404，实际 %d：%s", w.Code, w.Body.String())
	}
	// 响应体不能泄露任务信息（哪怕是错误信息里带个字段名都算漏）
	if body := w.Body.String(); len(body) > 0 && body != `{"error":{"code":"not_found","details":[],"message":"任务不存在"}}`+"\n" {
		t.Logf("响应体：%s", body)
	}
}

func TestJobDetail_OwnerAndAdminAllowed(t *testing.T) {
	s, _ := setupServer(t)
	s.Signer = sign.New("authz-test-secret", time.Hour)
	job, _ := seedJob(t, s, "jb_owner_2", "u_owner")

	owner := &auth.Principal{User: &models.User{ID: "u_owner", Role: "member"}}
	req := withParam(httptest.NewRequest(http.MethodGet, "/jobs/"+job.ID, nil), "id", job.ID)
	w := httptest.NewRecorder()
	s.handleJobDetail(w, withPrincipal(req, owner))
	if w.Code != http.StatusOK {
		t.Fatalf("发起人读自己的任务应 200，实际 %d：%s", w.Code, w.Body.String())
	}

	admin := &auth.Principal{User: &models.User{ID: "u_admin", Role: "admin"}}
	req2 := withParam(httptest.NewRequest(http.MethodGet, "/jobs/"+job.ID, nil), "id", job.ID)
	w2 := httptest.NewRecorder()
	s.handleJobDetail(w2, withPrincipal(req2, admin))
	if w2.Code != http.StatusOK {
		t.Fatalf("管理员读任意任务应 200，实际 %d", w2.Code)
	}
}

func TestJobDownload_RejectsNonOwner(t *testing.T) {
	s, _ := setupServer(t)
	s.Signer = sign.New("authz-test-secret", time.Hour)
	job, _ := seedJob(t, s, "jb_owner_3", "u_owner")

	outsider := &auth.Principal{User: &models.User{ID: "u_outsider", Role: "member"}}
	req := withParam(httptest.NewRequest(http.MethodGet, "/jobs/"+job.ID+"/download", nil), "id", job.ID)
	w := httptest.NewRecorder()
	s.handleJobDownload(w, withPrincipal(req, outsider))

	if w.Code != http.StatusNotFound {
		t.Fatalf("非发起人下载他人产物应 404，实际 %d", w.Code)
	}
	if w.Body.Len() > 200 {
		t.Fatalf("拒绝时不应带出产物内容，实际 %d 字节", w.Body.Len())
	}

	// 发起人自己下载必须真的拿到 zip 字节
	owner := &auth.Principal{User: &models.User{ID: "u_owner", Role: "member"}}
	req2 := withParam(httptest.NewRequest(http.MethodGet, "/jobs/"+job.ID+"/download", nil), "id", job.ID)
	w2 := httptest.NewRecorder()
	s.handleJobDownload(w2, withPrincipal(req2, owner))
	if w2.Code != http.StatusOK || w2.Body.String() != "PK-fake-zip" {
		t.Fatalf("发起人下载应拿到 zip，实际 %d / %q", w2.Code, w2.Body.String())
	}
}

func TestDocumentDownload_RejectsNonOwner(t *testing.T) {
	s, _ := setupServer(t)
	s.Signer = sign.New("authz-test-secret", time.Hour)
	_, doc := seedJob(t, s, "jb_owner_4", "u_owner")

	outsider := &auth.Principal{User: &models.User{ID: "u_outsider", Role: "member"}}
	req := withParam(httptest.NewRequest(http.MethodGet, "/documents/"+doc.ID+"/download", nil), "id", doc.ID)
	w := httptest.NewRecorder()
	s.handleDocumentDownload(w, withPrincipal(req, outsider))

	if w.Code != http.StatusNotFound {
		t.Fatalf("非发起人下载他人文档应 404，实际 %d", w.Code)
	}
}

func TestSignedURL_RejectsNonOwner(t *testing.T) {
	s, _ := setupServer(t)
	s.Signer = sign.New("authz-test-secret", time.Hour)
	_, doc := seedJob(t, s, "jb_owner_5", "u_owner")

	outsider := &auth.Principal{User: &models.User{ID: "u_outsider", Role: "member"}}
	body, _ := json.Marshal(map[string]string{"document_id": doc.ID})
	req := httptest.NewRequest(http.MethodPost, "/signed-url", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handleSignedURL(w, withPrincipal(req, outsider))

	// 这条最关键：直链 token 本身不带身份，签发环节不拦就等于绕过了上面所有校验
	if w.Code != http.StatusNotFound {
		t.Fatalf("非发起人签发他人直链应 404，实际 %d：%s", w.Code, w.Body.String())
	}
	if w.Body.Len() > 0 && json.Valid(w.Body.Bytes()) {
		var m map[string]interface{}
		_ = json.Unmarshal(w.Body.Bytes(), &m)
		if _, ok := m["signed_url"]; ok {
			t.Fatal("拒绝时绝不能返回 signed_url")
		}
	}
}

func TestListJobs_ScopedByOwner(t *testing.T) {
	s, _ := setupServer(t)
	s.Signer = sign.New("authz-test-secret", time.Hour)
	seedJob(t, s, "jb_scope_a", "u_a")
	seedJob(t, s, "jb_scope_b", "u_b")

	pa := &auth.Principal{User: &models.User{ID: "u_a", Role: "member"}}
	req := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	w := httptest.NewRecorder()
	s.handleListJobs(w, withPrincipal(req, pa))
	if w.Code != http.StatusOK {
		t.Fatalf("列表应 200，实际 %d", w.Code)
	}
	var resp struct {
		Items []map[string]interface{} `json:"items"`
		Total int64                    `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Total != 1 || len(resp.Items) != 1 {
		t.Fatalf("member 只应看到自己 1 条任务，实际 total=%d len=%d", resp.Total, len(resp.Items))
	}
	if resp.Items[0]["id"] != "jb_scope_a" {
		t.Fatalf("看到了别人的任务：%v", resp.Items[0]["id"])
	}

	// 管理员的列表口径不动：两条都要能看到
	admin := &auth.Principal{User: &models.User{ID: "u_admin", Role: "admin"}}
	req2 := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	w2 := httptest.NewRecorder()
	s.handleListJobs(w2, withPrincipal(req2, admin))
	var resp2 struct {
		Total int64 `json:"total"`
	}
	_ = json.Unmarshal(w2.Body.Bytes(), &resp2)
	if resp2.Total != 2 {
		t.Fatalf("管理员应看到全部 2 条任务，实际 %d", resp2.Total)
	}
}

func TestListDocuments_ScopedByOwner(t *testing.T) {
	s, _ := setupServer(t)
	s.Signer = sign.New("authz-test-secret", time.Hour)
	seedJob(t, s, "jb_doc_a", "u_a")
	seedJob(t, s, "jb_doc_b", "u_b")

	pa := &auth.Principal{User: &models.User{ID: "u_a", Role: "member"}}
	req := httptest.NewRequest(http.MethodGet, "/documents", nil)
	w := httptest.NewRecorder()
	s.handleListDocuments(w, withPrincipal(req, pa))
	if w.Code != http.StatusOK {
		t.Fatalf("文档列表应 200，实际 %d：%s", w.Code, w.Body.String())
	}
	var resp struct {
		Items []map[string]interface{} `json:"items"`
		Total int64                    `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Total != 1 {
		t.Fatalf("member 只应看到自己 1 份文档，实际 %d", resp.Total)
	}
}
