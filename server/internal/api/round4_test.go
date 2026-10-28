package api

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"docforge/server/internal/auth"
	"docforge/server/internal/models"
	"docforge/server/internal/render"

	"github.com/go-chi/chi/v5"
)

// 本文件覆盖本轮三件事：模板可删、签章删除的级联语义、字体导入的前置校验。
// 关注点统一是「删干净了吗 / 越权了吗」——这类缺陷不会报错，只会留脏数据。

func asUser(u *models.User) *auth.Principal { return &auth.Principal{User: u} }

func withUser(r *http.Request, u *models.User) *http.Request {
	return r.WithContext(auth.WithPrincipal(r.Context(), asUser(u)))
}

func newRouter(fn func(chi.Router, *Server), s *Server) *chi.Mux {
	r := chi.NewRouter()
	fn(r, s)
	return r
}

func seedUser(t *testing.T, s *Server, id, name, role string) *models.User {
	u := models.User{ID: id, Username: name, DisplayName: name, Role: role, Status: "active", Source: "local"}
	if err := s.DB.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	return &u
}

// ---------------- 模板删除 ----------------

func TestTemplateDelete_SoftDeletesAndFreesCode(t *testing.T) {
	s, _ := setupServer(t)
	owner := seedUser(t, s, "u_owner", "owner", "member")
	tpl := seedTemplate(t, s, "tpl_del1", "ver_del1", "1.0.0", owner.ID, true)

	mux := newRouter(func(r chi.Router, s *Server) {
		r.Delete("/api/v1/templates/{id}", s.handleDeleteTemplate)
	}, s)

	req := withUser(httptest.NewRequest(http.MethodDelete, "/api/v1/templates/"+tpl.ID, nil), owner)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	// 1) 正常查询查不到了（GORM 软删过滤生效）
	var cnt int64
	s.DB.Model(&models.Template{}).Where("id = ?", tpl.ID).Count(&cnt)
	if cnt != 0 {
		t.Fatalf("软删后仍能被普通查询查到，说明过滤没生效")
	}
	// 2) 但 Unscoped 还能看到记录，且原 code 被保留下来
	var gone models.Template
	if err := s.DB.Unscoped().First(&gone, "id = ?", tpl.ID).Error; err != nil {
		t.Fatalf("找不到软删记录：%v", err)
	}
	if !gone.DeletedAt.Valid {
		t.Fatal("deleted_at 没写")
	}
	if gone.OriginalCode != tpl.Code {
		t.Fatalf("OriginalCode=%q, want %q", gone.OriginalCode, tpl.Code)
	}
	// 3) code 被释放：这是软删最容易被忽略的坑（不释放则重建同名必撞唯一索引）
	if gone.Code == tpl.Code {
		t.Fatalf("code 没有释放，重建同名模板会撞唯一索引")
	}
	rebuilt := models.Template{
		ID: "tpl_del1b", Code: tpl.Code, Name: "重名的", CreatedBy: owner.ID,
		Visibility: "private", Enabled: true, CurrentVersionID: "ver_x",
	}
	if err := s.DB.Create(&rebuilt).Error; err != nil {
		t.Fatalf("删除后重建同 code 失败：%v", err)
	}
}

func TestTemplateDelete_ForbiddenForOtherMember(t *testing.T) {
	s, _ := setupServer(t)
	owner := seedUser(t, s, "u_o2", "owner2", "member")
	other := seedUser(t, s, "u_x2", "other2", "member")
	tpl := seedTemplate(t, s, "tpl_del2", "ver_del2", "1.0.0", owner.ID, true)

	mux := newRouter(func(r chi.Router, s *Server) {
		r.Delete("/api/v1/templates/{id}", s.handleDeleteTemplate)
	}, s)
	req := withUser(httptest.NewRequest(http.MethodDelete, "/api/v1/templates/"+tpl.ID, nil), other)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	// 模板默认是 private，他人无权删
	if w.Code != http.StatusForbidden {
		t.Fatalf("他人删除私有模板 status=%d, want 403", w.Code)
	}
	var cnt int64
	s.DB.Model(&models.Template{}).Where("id = ?", tpl.ID).Count(&cnt)
	if cnt != 1 {
		t.Fatal("越权请求竟然把模板删掉了")
	}
}

func TestTemplateDelete_BlocksWhenJobRunning(t *testing.T) {
	s, _ := setupServer(t)
	owner := seedUser(t, s, "u_o3", "owner3", "member")
	tpl := seedTemplate(t, s, "tpl_del3", "ver_del3", "1.0.0", owner.ID, true)
	job := models.Job{ID: "jb_run", TemplateID: tpl.ID, VersionID: "ver_del3", Status: "running", Total: 1}
	if err := s.DB.Create(&job).Error; err != nil {
		t.Fatal(err)
	}

	mux := newRouter(func(r chi.Router, s *Server) {
		r.Delete("/api/v1/templates/{id}", s.handleDeleteTemplate)
	}, s)
	req := withUser(httptest.NewRequest(http.MethodDelete, "/api/v1/templates/"+tpl.ID, nil), owner)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("有运行中任务时 status=%d, want 409（body=%s）", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "job_running") {
		t.Fatalf("错误码不是 job_running：%s", w.Body.String())
	}
}

func TestTemplateDelete_TwiceIs404(t *testing.T) {
	s, _ := setupServer(t)
	owner := seedUser(t, s, "u_o4", "owner4", "member")
	tpl := seedTemplate(t, s, "tpl_del4", "ver_del4", "1.0.0", owner.ID, true)
	mux := newRouter(func(r chi.Router, s *Server) {
		r.Delete("/api/v1/templates/{id}", s.handleDeleteTemplate)
	}, s)

	for i, want := range []int{http.StatusOK, http.StatusNotFound} {
		req := withUser(httptest.NewRequest(http.MethodDelete, "/api/v1/templates/"+tpl.ID, nil), owner)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("第 %d 次删除 status=%d, want %d", i+1, w.Code, want)
		}
	}
}

// ---------------- 签章删除：级联吊销授权 ----------------

func TestSealDelete_RevokesGrants(t *testing.T) {
	s, _ := setupServer(t)
	owner := seedUser(t, s, "u_so", "sealowner", "member")
	other := seedUser(t, s, "u_sx", "sealother", "member")
	seal := models.Seal{ID: "seal_del1", Name: "公司公章", OwnerID: owner.ID, Status: "approved",
		FilePath: "/tmp/none.png", Source: "upload"}
	if err := s.DB.Create(&seal).Error; err != nil {
		t.Fatal(err)
	}
	g := models.Grant{ID: "grt_1", ResourceType: "seal", ResourceID: seal.ID,
		GrantedBy: owner.ID, GranteeType: "user", GranteeID: other.ID, MaxUses: 5}
	if err := s.DB.Create(&g).Error; err != nil {
		t.Fatal(err)
	}

	mux := newRouter(func(r chi.Router, s *Server) {
		r.Delete("/api/v1/seals/{id}", s.handleDeleteSeal)
	}, s)
	req := withUser(httptest.NewRequest(http.MethodDelete, "/api/v1/seals/"+seal.ID, nil), owner)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var grantCnt int64
	s.DB.Model(&models.Grant{}).Where("resource_id = ?", seal.ID).Count(&grantCnt)
	if grantCnt != 0 {
		t.Fatalf("印章删除后仍残留 %d 条授权（授权抽屉里会显示幽灵条目）", grantCnt)
	}

	var resp struct {
		RevokedGrants int64 `json:"revoked_grants"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.RevokedGrants != 1 {
		t.Fatalf("revoked_grants=%d, want 1", resp.RevokedGrants)
	}
}

func TestSealDelete_AdminCanDeleteOthers(t *testing.T) {
	s, _ := setupServer(t)
	owner := seedUser(t, s, "u_so2", "owner_a", "member")
	admin := seedUser(t, s, "u_sa2", "boss", "admin")
	seal := models.Seal{ID: "seal_del2", Name: "他人章", OwnerID: owner.ID, Status: "approved",
		FilePath: "/tmp/none.png", Source: "upload"}
	if err := s.DB.Create(&seal).Error; err != nil {
		t.Fatal(err)
	}
	mux := newRouter(func(r chi.Router, s *Server) {
		r.Delete("/api/v1/seals/{id}", s.handleDeleteSeal)
	}, s)
	req := withUser(httptest.NewRequest(http.MethodDelete, "/api/v1/seals/"+seal.ID, nil), admin)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("管理员删他人印章 status=%d, want 200", w.Code)
	}
}

func TestSealDelete_OtherMemberForbidden(t *testing.T) {
	s, _ := setupServer(t)
	owner := seedUser(t, s, "u_so3", "owner_b", "member")
	other := seedUser(t, s, "u_sx3", "intruder", "member")
	seal := models.Seal{ID: "seal_del3", Name: "私章", OwnerID: owner.ID, Status: "approved",
		FilePath: "/tmp/none.png", Source: "upload"}
	if err := s.DB.Create(&seal).Error; err != nil {
		t.Fatal(err)
	}
	mux := newRouter(func(r chi.Router, s *Server) {
		r.Delete("/api/v1/seals/{id}", s.handleDeleteSeal)
	}, s)
	req := withUser(httptest.NewRequest(http.MethodDelete, "/api/v1/seals/"+seal.ID, nil), other)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("非所有者删除 status=%d, want 403", w.Code)
	}
}

// ---------------- 可用签章（选章的数据源） ----------------

func TestUsableAssets_IncludesGrantedNotJustOwned(t *testing.T) {
	s, _ := setupServer(t)
	owner := seedUser(t, s, "u_au", "assetowner", "member")
	user := seedUser(t, s, "u_am", "member1", "member")

	mine := models.Seal{ID: "seal_mine", Name: "我的章", OwnerID: user.ID, Status: "approved",
		FilePath: "/tmp/a.png", Source: "upload"}
	shared := models.Seal{ID: "seal_shared", Name: "借来的章", OwnerID: owner.ID, Status: "approved",
		FilePath: "/tmp/b.png", Source: "upload"}
	for _, x := range []models.Seal{mine, shared} {
		if err := s.DB.Create(&x).Error; err != nil {
			t.Fatal(err)
		}
	}
	// 一条有效授权 + 一条已过期授权
	s.DB.Create(&models.Grant{ID: "g_ok", ResourceType: "seal", ResourceID: shared.ID,
		GranteeType: "user", GranteeID: user.ID, MaxUses: 3, UsedCount: 1})
	expired := time.Now().Add(-24 * time.Hour)
	s.DB.Create(&models.Grant{ID: "g_exp", ResourceType: "seal", ResourceID: "seal_expired_only",
		GranteeType: "user", GranteeID: user.ID, ValidUntil: &expired})

	mux := newRouter(func(r chi.Router, s *Server) {
		r.Get("/api/v1/seals/usable", func(w http.ResponseWriter, r *http.Request) {
			s.handleUsableAssets(w, r, "seal")
		})
	}, s)
	req := withUser(httptest.NewRequest(http.MethodGet, "/api/v1/seals/usable", nil), user)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var out struct {
		Items []struct {
			ID        string `json:"id"`
			Owned     bool   `json:"owned"`
			Remaining int    `json:"remaining"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	byID := map[string]bool{}
	for _, it := range out.Items {
		byID[it.ID] = it.Owned
		if it.ID == "seal_shared" {
			if it.Owned {
				t.Fatal("授权来的章被标成了自己的")
			}
			if it.Remaining != 2 {
				t.Fatalf("剩余次数=%d, want 2（3 次授权已用 1）", it.Remaining)
			}
		}
	}
	if _, ok := byID["seal_mine"]; !ok {
		t.Fatal("自己拥有的章没列出来")
	}
	if _, ok := byID["seal_shared"]; !ok {
		t.Fatal("授权来的章没列出来——选章列表就是靠它，缺了等于用户以为没权限")
	}
}

func TestUsableAssets_HidesUnapproved(t *testing.T) {
	s, _ := setupServer(t)
	user := seedUser(t, s, "u_ap", "pender", "member")
	s.DB.Create(&models.Seal{ID: "seal_pending", Name: "待审", OwnerID: user.ID, Status: "pending",
		FilePath: "/tmp/c.png", Source: "upload"})

	mux := newRouter(func(r chi.Router, s *Server) {
		r.Get("/api/v1/seals/usable", func(w http.ResponseWriter, r *http.Request) {
			s.handleUsableAssets(w, r, "seal")
		})
	}, s)
	req := withUser(httptest.NewRequest(http.MethodGet, "/api/v1/seals/usable", nil), user)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if strings.Contains(w.Body.String(), "seal_pending") {
		t.Fatal("未审批通过的印章不应出现在可用列表里")
	}
}

// ---------------- 字体导入 ----------------

func mkFontBytes(magic string, size int) []byte {
	b := make([]byte, size)
	copy(b, []byte(magic))
	for i := 4; i < size; i++ {
		b[i] = 0x20
	}
	return b
}

func uploadFontReq(t *testing.T, path, field, filename string, content []byte, u *models.User) *http.Request {
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	fw, err := mw.CreateFormFile(field, filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatal(err)
	}
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, path, body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return withUser(req, u)
}

func TestFontUpload_AcceptsValidTTF(t *testing.T) {
	s, _ := setupServer(t)
	admin := seedUser(t, s, "u_ad", "admin1", "admin")
	s.Renderer = &fakeRenderer{}
	mux := newRouter(func(r chi.Router, s *Server) {
		r.Post("/api/v1/fonts", s.handleUploadFont)
	}, s)

	req := uploadFontReq(t, "/api/v1/fonts", "file", "simhei.ttf", mkFontBytes("\x00\x01\x00\x00", 2048), admin)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	saved := filepath.Join(s.Cfg.DataDir, "fonts", "simhei.ttf")
	if _, err := os.Stat(saved); err != nil {
		t.Fatalf("字体没落盘：%v", err)
	}
	if fr := s.Renderer.(*fakeRenderer); fr.reloadFontCalls == 0 {
		t.Fatal("上传后没有触发字体缓存刷新，LibreOffice 不会认识这个字体")
	}
}

func TestFontUpload_RejectsFakeExtension(t *testing.T) {
	s, _ := setupServer(t)
	admin := seedUser(t, s, "u_ad2", "admin2", "admin")
	mux := newRouter(func(r chi.Router, s *Server) {
		r.Post("/api/v1/fonts", s.handleUploadFont)
	}, s)

	// 扩展名对、内容不是字体（比如把 zip 改名）
	req := uploadFontReq(t, "/api/v1/fonts", "file", "fake.ttf", []byte("PK\x03\x04not a font"), admin)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("伪造字体 status=%d, want 422（body=%s）", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(s.Cfg.DataDir, "fonts", "fake.ttf")); err == nil {
		t.Fatal("魔数校验失败却把文件写进去了")
	}
}

func TestFontUpload_RejectsBadExtAndPathTraversal(t *testing.T) {
	s, _ := setupServer(t)
	admin := seedUser(t, s, "u_ad3", "admin3", "admin")
	mux := newRouter(func(r chi.Router, s *Server) {
		r.Post("/api/v1/fonts", s.handleUploadFont)
	}, s)

	// 扩展名不在白名单 → 必须拒
	for _, c := range []struct{ name, filename string }{
		{"bad-ext", "font.exe"},
		{"empty-ext", "font"},
	} {
		req := uploadFontReq(t, "/api/v1/fonts", "file", c.filename, mkFontBytes("OTTO", 512), admin)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code == http.StatusCreated {
			t.Fatalf("%s 竟然被接受了（%s）", c.name, c.filename)
		}
	}

	// 路径穿越：Go 的 part.FileName() 已经过 filepath.Base，所以真正的防线是
	// 「文件必须落在 fonts 目录内」。这里断言结果安全，而不是断言某个状态码 ——
	// 状态码会随标准库/框架行为变，落在哪是硬约束。
	req := uploadFontReq(t, "/api/v1/fonts", "file", "../../etc/evil.ttf", mkFontBytes("OTTO", 512), admin)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	fontDir := filepath.Join(s.Cfg.DataDir, "fonts")
	entries, _ := os.ReadDir(fontDir)
	found := false
	for _, e := range entries {
		if e.Name() == "evil.ttf" {
			found = true
		}
	}
	if !found {
		t.Fatalf("期望落盘为 fonts/evil.ttf（Base 后），实际 fonts/ 内容=%v status=%d",
			entries, w.Code)
	}
	// 绝不能出现在 data 根目录或其他上层位置
	if _, err := os.Stat(filepath.Join(s.Cfg.DataDir, "evil.ttf")); err == nil {
		t.Fatal("路径穿越成功，写到了 data 根目录")
	}

	// 名字不合法（含空格以外的奇怪字符）也必须拒
	req = uploadFontReq(t, "/api/v1/fonts", "file", "bad$name.ttf", mkFontBytes("OTTO", 512), admin)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code == http.StatusCreated {
		t.Fatal("非法字符的文件名被接受了")
	}
}

func TestFontUpload_RequiresAdmin(t *testing.T) {
	s, _ := setupServer(t)
	member := seedUser(t, s, "u_mem", "member9", "member")
	mux := newRouter(func(r chi.Router, s *Server) {
		r.Post("/api/v1/fonts", s.handleUploadFont)
	}, s)
	req := uploadFontReq(t, "/api/v1/fonts", "file", "ok.ttf", mkFontBytes("\x00\x01\x00\x00", 512), member)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("普通成员上传字体 status=%d, want 403", w.Code)
	}
}

func TestFontList_RequiresAdmin(t *testing.T) {
	// 「不能写但能看」只是半个门：字体清单回答的是这台机器上装了哪些字体，
	// 属于部署信息，前端也只有后台字体管理页在用它。
	s, _ := setupServer(t)
	member := seedUser(t, s, "u_mem2", "member10", "member")
	admin := seedUser(t, s, "u_ad5", "admin5", "admin")
	mux := newRouter(func(r chi.Router, s *Server) {
		r.Get("/api/v1/fonts", s.handleListFonts)
	}, s)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/fonts", nil)
	req = withUser(req, member)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("普通成员读字体清单 status=%d, want 403", w.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/fonts", nil)
	req = withUser(req, admin)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("管理员读字体清单 status=%d, want 200 body=%s", w.Code, w.Body.String())
	}
}

func TestFontUpload_DuplicateGetsSuffix(t *testing.T) {
	s, _ := setupServer(t)
	admin := seedUser(t, s, "u_ad4", "admin4", "admin")
	mux := newRouter(func(r chi.Router, s *Server) {
		r.Post("/api/v1/fonts", s.handleUploadFont)
	}, s)
	for i := 0; i < 2; i++ {
		req := uploadFontReq(t, "/api/v1/fonts", "file", "dup.ttf", mkFontBytes("\x00\x01\x00\x00", 512), admin)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("第 %d 次上传 status=%d body=%s", i+1, w.Code, w.Body.String())
		}
	}
	files, _ := os.ReadDir(filepath.Join(s.Cfg.DataDir, "fonts"))
	if len(files) != 2 {
		t.Fatalf("同名文件应并存为 dup.ttf / dup-2.ttf，实际 %d 个", len(files))
	}
}

func TestFontDelete_OnlyImportedDir(t *testing.T) {
	s, _ := setupServer(t)
	admin := seedUser(t, s, "u_ad5", "admin5", "admin")
	dir := filepath.Join(s.Cfg.DataDir, "fonts")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "ok.ttf"), []byte("x"), 0o644)

	mux := newRouter(func(r chi.Router, s *Server) {
		r.Delete("/api/v1/fonts/{name}", s.handleDeleteFont)
	}, s)
	req := withUser(httptest.NewRequest(http.MethodDelete, "/api/v1/fonts/ok.ttf", nil), admin)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "ok.ttf")); err == nil {
		t.Fatal("文件没删掉")
	}

	// 穿越请求必须被挡
	req = withUser(httptest.NewRequest(http.MethodDelete, "/api/v1/fonts/..%2F..%2Fetc%2Fpasswd", nil), admin)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code == http.StatusOK {
		t.Fatal("路径穿越删除竟然成功")
	}
}

// ---------------- 管理员名单 ----------------

func TestEnsureAdminUsernames_PromotesAndIsIdempotent(t *testing.T) {
	s, cfg := setupServer(t)
	cfg.AdminUsernames = []string{"opsadmin"}
	u := seedUser(t, s, "u_kn", "opsadmin", "member")

	auth.EnsureAdminUsernames(s.DB, cfg)
	var got models.User
	s.DB.First(&got, "id = ?", u.ID)
	if got.Role != "admin" {
		t.Fatalf("role=%q, want admin", got.Role)
	}

	// 幂等：再跑一次不应报错、也不应改 updated_at
	before := got.UpdatedAt
	auth.EnsureAdminUsernames(s.DB, cfg)
	s.DB.First(&got, "id = ?", u.ID)
	if !got.UpdatedAt.Equal(before) {
		t.Fatal("已经是管理员时不应再写库（会产生审计噪音）")
	}
}

func TestEnsureAdminUsernames_DoesNotDemote(t *testing.T) {
	s, cfg := setupServer(t)
	cfg.AdminUsernames = []string{"someoneelse"}
	u := seedUser(t, s, "u_keep", "keepadmin", "admin")

	auth.EnsureAdminUsernames(s.DB, cfg)
	var got models.User
	s.DB.First(&got, "id = ?", u.ID)
	if got.Role != "admin" {
		t.Fatal("名单外的管理员被降级了——移出名单不应解除已有权限")
	}
}

func TestPromoteIfListed_AfterFirstLogin(t *testing.T) {
	s, cfg := setupServer(t)
	cfg.AdminUsernames = []string{"opsadmin"}
	// 模拟：启动时账号还不存在（OIDC 首次登录才建号）
	auth.EnsureAdminUsernames(s.DB, cfg)
	u := seedUser(t, s, "u_late", "opsadmin", "member")

	auth.PromoteIfListed(s.DB, cfg, u)
	if u.Role != "admin" {
		t.Fatalf("登录后未提升，内存对象 role=%q", u.Role)
	}
	var got models.User
	s.DB.First(&got, "id = ?", u.ID)
	if got.Role != "admin" {
		t.Fatalf("登录后未提升，库里 role=%q", got.Role)
	}
}

// 确保渲染接口扩展没破坏既有实现（编译期断言）
var _ render.Renderer = (*fakeRenderer)(nil)

var _ = context.Background
