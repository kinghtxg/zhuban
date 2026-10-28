package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"docforge/server/internal/assetimg"
	"docforge/server/internal/auth"
	"docforge/server/internal/config"
	"docforge/server/internal/models"
	"docforge/server/internal/render"
	"docforge/server/internal/storage"
	"docforge/server/internal/store"

	"github.com/go-chi/chi/v5"
)

// fakeRenderer 实现 Renderer，记录 Apply/Parse 调用，不依赖在线服务。
type fakeRenderer struct {
	applyCount int
	appliedDocx []byte
	parseBlocks string
	parseErr error
	// 记录字体缓存刷新被调用的次数（上传/删除字体后必须触发一次）
	reloadFontCalls int
}

func (f *fakeRenderer) Scan(ctx context.Context, docx []byte) (*render.ScanResult, error) {
	return &render.ScanResult{
		Variables: []interface{}{
			map[string]interface{}{"name": "被测件名称", "kind": "scalar", "type": "string", "required": true},
			map[string]interface{}{"name": "items", "kind": "loop", "type": "array"},
		},
		SampleJSON: map[string]interface{}{"被测件名称": "示例"},
	}, nil
}
func (f *fakeRenderer) Render(ctx context.Context, docx []byte, data []byte, format string) ([]byte, string, error) {
	return []byte("r-" + format), "application/octet-stream", nil
}
func (f *fakeRenderer) Validate(ctx context.Context, docx []byte, data []byte) (*render.ValidateResult, error) {
	return &render.ValidateResult{Valid: true}, nil
}
func (f *fakeRenderer) Preview(ctx context.Context, docx []byte, data []byte, maxPages int) (*render.PreviewResult, error) {
	return &render.PreviewResult{PreviewID: "pv_test", PageCount: 1, Pages: []render.PreviewPage{{Page: 1, URL: "/x"}}}, nil
}
func (f *fakeRenderer) PreviewPage(ctx context.Context, pv string, n int) ([]byte, error) {
	return []byte("png"), nil
}
func (f *fakeRenderer) Health(ctx context.Context) (*render.Health, error) {
	return &render.Health{Status: "ok"}, nil
}
func (f *fakeRenderer) ParseModel(ctx context.Context, docx []byte) (*render.ParseResult, error) {
	if f.parseErr != nil {
		return nil, f.parseErr
	}
	blocks := f.parseBlocks
	if blocks == "" {
		blocks = `[{"id":"b1","type":"paragraph","text":"hi"}]`
	}
	return &render.ParseResult{Blocks: json.RawMessage(blocks), Warnings: []string{}}, nil
}
func (f *fakeRenderer) ApplyModel(ctx context.Context, docx []byte, modelJSON []byte) ([]byte, error) {
	f.applyCount++
	f.appliedDocx = docx
	return []byte("APPLIED:" + string(modelJSON)), nil
}
func (f *fakeRenderer) RenderSignature(ctx context.Context, fields map[string]string) ([]byte, error) {
	return []byte("PNG-SIG"), nil
}
func (f *fakeRenderer) RenderSeal(ctx context.Context, fields map[string]string) ([]byte, error) {
	return []byte("PNG-SEAL"), nil
}
func (f *fakeRenderer) Fonts(ctx context.Context) ([]render.Font, error) {
	return []render.Font{{ID: "hand", Name: "手写体"}}, nil
}

func (f *fakeRenderer) ReloadFonts(ctx context.Context) (*render.ReloadResult, error) {
	f.reloadFontCalls++
	return &render.ReloadResult{OK: true, ElapsedMS: 1}, nil
}

func (f *fakeRenderer) InstalledFonts(ctx context.Context) ([]render.InstalledFont, error) {
	return []render.InstalledFont{
		{File: "MaShanZheng-Regular.ttf", Family: "Ma Shan Zheng", Path: "/usr/share/fonts/custom/MaShanZheng-Regular.ttf"},
		{File: "SourceHanSansCN.otf", Family: "Source Han Sans CN", Path: "/usr/share/fonts/imported/SourceHanSansCN.otf", Imported: true},
	}, nil
}

func setupServer(t *testing.T) (*Server, *config.Config) {
	dir := t.TempDir()
	cfg := &config.Config{
		DataDir: dir, RetentionDays: 7,
		DatabaseURL: "sqlite://" + filepath.Join(dir, "test.db"),
		MaxImageMB: 5,
	}
	db, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := storage.New(dir)
	s := &Server{DB: db, Cfg: cfg, Store: st, Renderer: &fakeRenderer{}}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return s, cfg
}

func seedTemplate(t *testing.T, s *Server, tplID, verID, version string, owner string, public bool) *models.Template {
	tpl := models.Template{
		ID: tplID, Code: "t-" + tplID, Name: "测试", CreatedBy: owner,
		Visibility: map[bool]string{true: "public", false: "private"}[public],
		Enabled: true, CurrentVersionID: verID,
	}
	docx := []byte("ORIGINAL-DOCX")
	path, err := s.Store.TemplateVersionPath(tplID, verID)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Store.WriteFile(path, docx, 0o644)
	ver := models.TemplateVersion{
		ID: verID, TemplateID: tplID, Version: version, FilePath: path,
		FileSize: int64(len(docx)), UploadedBy: owner, Source: "upload", Scanned: true,
	}
	if err := s.DB.Create(&tpl).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Create(&ver).Error; err != nil {
		t.Fatal(err)
	}
	return &tpl
}

func TestCanEditTemplate(t *testing.T) {
	admin := &models.User{ID: "u_admin", Role: "admin"}
	memberA := &models.User{ID: "u_a", Role: "member"}
	memberB := &models.User{ID: "u_b", Role: "member"}
	pub := models.Template{CreatedBy: "u_a", Visibility: "public"}
	priv := models.Template{CreatedBy: "u_a", Visibility: "private"}

	cases := []struct {
		name string
		u    *models.User
		t    models.Template
		want bool
	}{
		{"admin-public", admin, pub, true},
		{"creator-public", memberA, pub, true},
		{"other-public", memberB, pub, true},
		{"creator-private", memberA, priv, true},
		{"other-private", memberB, priv, false},
	}
	for _, c := range cases {
		got := canEditTemplate(&auth.Principal{User: c.u}, &c.t)
		if got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestImageBindingOverrides(t *testing.T) {
	s, _ := setupServer(t)
	tpl := seedTemplate(t, s, "tpl_img", "ver_img", "1.0.0", "u1", true)
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 1, 2, 3} // 假 png 字节
	asset := models.TemplateAsset{
		ID: "ast_1", TemplateID: tpl.ID, Name: "logo", Type: "image",
		MIME: "image/png", WidthMM: 30, HeightMM: 12,
	}
	path := s.Store.AssetPath(tpl.ID, asset.ID, ".png")
	if err := s.Store.WriteFile(path, png, 0o644); err != nil {
		t.Fatal(err)
	}
	asset.FilePath = path
	s.DB.Create(&asset)

	// 真实 parse 输出：占位符 type=placeholder kind=image name=image_logo，前端打 asset_id。
	model := `{"blocks":[{"id":"p1","type":"placeholder","kind":"image","name":"image_logo","asset_id":"ast_1"}]}`
	ov := s.editorOverrides(tpl.ID, []byte(model))
	obj, ok := ov["image_logo"].(map[string]interface{})
	if !ok {
		t.Fatalf("override missing for image_logo: %v", ov)
	}
	if obj["type"] != "image" {
		t.Errorf("type=%v", obj["type"])
	}
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	if obj["src"] != want {
		t.Errorf("src mismatch: %v", obj["src"])
	}
	if obj["width"].(float64) != 30 {
		t.Errorf("width=%v want 30", obj["width"])
	}

	// 用户数据同名 key 不被覆盖。
	base := []byte(`{"image_logo":"USER-VALUE","other":1}`)
	merged := assetimg.MergeInto(base, ov)
	var m map[string]interface{}
	_ = json.Unmarshal(merged, &m)
	if m["image_logo"] != "USER-VALUE" {
		t.Errorf("user data overwritten: %v", m["image_logo"])
	}
	if m["other"].(float64) != 1 {
		t.Errorf("other lost: %v", m["other"])
	}

	// 无用户数据时注入默认图片。
	merged2 := assetimg.MergeInto(nil, ov)
	var m2 map[string]interface{}
	_ = json.Unmarshal(merged2, &m2)
	if _, ok := m2["image_logo"].(map[string]interface{}); !ok {
		t.Errorf("default override not injected: %v", m2)
	}
}

func TestPersistBindingsAndMergeDefaults(t *testing.T) {
	s, _ := setupServer(t)
	tpl := seedTemplate(t, s, "tpl_bind", "ver_bind", "1.0.0", "u1", true)
	png := []byte{1, 2, 3, 4, 5}
	asset := models.TemplateAsset{
		ID: "ast_bind", TemplateID: tpl.ID, Name: "logo", Type: "image", MIME: "image/png",
	}
	path := s.Store.AssetPath(tpl.ID, asset.ID, ".png")
	if err := s.Store.WriteFile(path, png, 0o644); err != nil {
		t.Fatal(err)
	}
	asset.FilePath = path
	s.DB.Create(&asset)

	// 保存时把 model 占位绑定到该资源。
	model := `{"blocks":[{"id":"p1","type":"placeholder","kind":"image","name":"image_logo","asset_id":"ast_bind"}]}`
	s.imgResolver().PersistBindings(tpl.ID, assetimg.ScanBindings([]byte(model)))

	var got models.TemplateAsset
	s.DB.First(&got, "id = ?", asset.ID)
	if got.BindVar != "image_logo" {
		t.Errorf("bind_var=%q want image_logo", got.BindVar)
	}

	// MergeDefaults 注入该图片默认值。
	out := s.imgResolver().MergeDefaults(tpl.ID, nil)
	var m map[string]interface{}
	_ = json.Unmarshal(out, &m)
	obj, ok := m["image_logo"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected image_logo override: %v", m)
	}
	if obj["type"] != "image" || obj["src"] == "" {
		t.Errorf("bad override: %v", obj)
	}

	// 解绑后 MergeDefaults 不再注入。
	s.imgResolver().PersistBindings(tpl.ID, nil)
	var got2 models.TemplateAsset
	s.DB.First(&got2, "id = ?", asset.ID)
	if got2.BindVar != "" {
		t.Errorf("bind_var should be cleared, got %q", got2.BindVar)
	}
	out2 := s.imgResolver().MergeDefaults(tpl.ID, nil)
	var m2 map[string]interface{}
	_ = json.Unmarshal(out2, &m2)
	if _, ok := m2["image_logo"]; ok {
		t.Errorf("override should be gone after unbind: %v", m2)
	}
}

func TestRollbackCopiesAndIncrements(t *testing.T) {
	s, _ := setupServer(t)
	tpl := seedTemplate(t, s, "tpl_rb", "ver_v1", "1.0.0", "u1", true)
	// 历史版本文件字节（一个更早的版本 0.9.0）。
	histBytes := []byte("HISTORICAL-BYTES")
	histVer := models.TemplateVersion{
		ID: "ver_hist", TemplateID: tpl.ID, Version: "0.9.0", UploadedBy: "u1", Source: "upload",
	}
	hpath, _ := s.Store.TemplateVersionPath(tpl.ID, histVer.ID)
	_ = s.Store.WriteFile(hpath, histBytes, 0o644)
	histVer.FilePath = hpath
	s.DB.Create(&histVer)

	operator := &models.User{ID: "u_op", Role: "member"}
	newVer, err := s.doRollback(tpl, &histVer, histBytes, operator)
	if err != nil {
		t.Fatal(err)
	}
	if newVer.Source != "rollback" {
		t.Errorf("source=%s want rollback", newVer.Source)
	}
	if newVer.Version != "1.0.1" {
		t.Errorf("version=%s want 1.0.1", newVer.Version)
	}
	if newVer.UploadedBy != "u_op" {
		t.Errorf("created_by=%s want u_op", newVer.UploadedBy)
	}
	// 新文件字节与历史一致。
	data, _ := os.ReadFile(newVer.FilePath)
	if string(data) != string(histBytes) {
		t.Error("rolled file bytes differ")
	}
	// 历史版本仍在（ver_v1 + ver_hist + 回滚新建的 = 3）。
	var cnt int64
	s.DB.Model(&models.TemplateVersion{}).Where("template_id = ?", tpl.ID).Count(&cnt)
	if cnt != 3 {
		t.Errorf("expected 3 versions after rollback, got %d", cnt)
	}
	// 当前指针已切到新版本。
	var updated models.Template
	s.DB.First(&updated, "id = ?", tpl.ID)
	if updated.CurrentVersionID != newVer.ID {
		t.Error("current_version_id not updated")
	}
}

func TestMergeVarMeta(t *testing.T) {
	s, _ := setupServer(t)
	ver := models.TemplateVersion{
		Variables:   models.JSONB(`[{"name":"a","kind":"scalar","type":"string","required":true},{"name":"b","kind":"loop"}]`),
		VariableMeta: models.JSONB(`{"a":{"default":"AAA","description":"字母A"},"gone":{"default":"X"}}`),
	}
	out := s.mergeVarMeta(&ver)
	if len(out) != 3 {
		t.Fatalf("expected 3 merged vars, got %d: %v", len(out), out)
	}
	byName := map[string]map[string]interface{}{}
	for _, m := range out {
		byName[m["name"].(string)] = m
	}
	if byName["a"]["default"] != "AAA" {
		t.Error("user default not merged into a")
	}
	if byName["a"]["status"] != "active" {
		t.Error("a should be active")
	}
	if byName["gone"]["status"] != "removed" {
		t.Error("gone should be marked removed")
	}
}

func TestEditorExportDocx(t *testing.T) {
	s, _ := setupServer(t)
	tpl := seedTemplate(t, s, "tpl_exp", "ver_exp", "1.0.0", "u1", true)

	// 构造 multipart：model + format=docx。
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("model", `{"blocks":[{"id":"b1","type":"paragraph","text":"x"}]}`)
	_ = mw.WriteField("format", "docx")
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/templates/tpl_exp/editor/export", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	// 注入登录主体（创建者 u1，可编辑）。
	pr := &auth.Principal{User: &models.User{ID: "u1", Role: "member"}}
	req = req.WithContext(auth.WithPrincipal(context.Background(), pr))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "tpl_exp")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rr := httptest.NewRecorder()
	s.handleEditorExport(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	ct := rr.Header().Get("Content-Type")
	if ct != "application/vnd.openxmlformats-officedocument.wordprocessingml.document" {
		t.Errorf("content-type=%s", ct)
	}
	cd := rr.Header().Get("Content-Disposition")
	if !strings.Contains(cd, `-draft.docx`) || !strings.Contains(cd, "t-tpl_exp") {
		t.Errorf("content-disposition=%s", cd)
	}
	if !strings.Contains(cd, `filename*=UTF-8''`) {
		t.Errorf("missing RFC5987 filename*=UTF-8'': %s", cd)
	}
	if strings.Contains(cd, `UTF-8-''`) {
		t.Errorf("bad hyphenated charset present: %s", cd)
	}
	if !strings.Contains(cd, `filename="`) {
		t.Errorf("missing ASCII filename= fallback: %s", cd)
	}
	// 回写字节来自 fakeRenderer.ApplyModel 输出。
	if rr.Body.String() != "APPLIED:{\"blocks\":[{\"id\":\"b1\",\"type\":\"paragraph\",\"text\":\"x\"}]}" {
		t.Errorf("unexpected body: %q", rr.Body.String())
	}
	// 确认未创建新版本（不落库）。
	var cnt int64
	s.DB.Model(&models.TemplateVersion{}).Where("template_id = ?", tpl.ID).Count(&cnt)
	if cnt != 1 {
		t.Errorf("expected no new version, got %d", cnt)
	}
}

func TestEditorExportForbidden(t *testing.T) {
	s, _ := setupServer(t)
	seedTemplate(t, s, "tpl_priv", "ver_priv", "1.0.0", "u1", false) // private

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("model", `{"blocks":[]}`)
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/templates/tpl_priv/editor/export", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	pr := &auth.Principal{User: &models.User{ID: "u_other", Role: "member"}}
	req = req.WithContext(auth.WithPrincipal(context.Background(), pr))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "tpl_priv")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rr := httptest.NewRecorder()
	s.handleEditorExport(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rr.Code)
	}
}

// TestUploadAssetCreatesDir 验证首次上传（assets 目录不存在）也能成功落盘。
func TestUploadAssetCreatesDir(t *testing.T) {
	s, _ := setupServer(t)
	// 用一个全新模板 id，其 assets 目录必然不存在。
	s.DB.Create(&models.Template{
		ID: "tpl_fresh", Code: "t-fresh", Name: "新模板", CreatedBy: "u1",
		Visibility: "public", Enabled: true, CurrentVersionID: "ver_fresh",
	})
	verFresh := models.TemplateVersion{
		ID: "ver_fresh", TemplateID: "tpl_fresh", Version: "1.0.0",
		FilePath: filepath.Join(s.Store.Root, "templates", "tpl_fresh", "ver_fresh.docx"),
		UploadedBy: "u1", Source: "upload", Scanned: true,
	}
	_ = s.Store.WriteFile(verFresh.FilePath, []byte("docx"), 0o644)
	s.DB.Create(&verFresh)

	png := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D,
		0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x06,
		0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4, 0x89, 0x00, 0x00, 0x00, 0x0D, 0x49, 0x44, 0x41,
		0x54, 0x78, 0x9C, 0x63, 0x00, 0x01, 0x00, 0x00, 0x05, 0x00, 0x01, 0x0D, 0x0A, 0x2D,
		0xB4, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE, 0x42, 0x60, 0x82}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "logo.png")
	_, _ = fw.Write(png)
	_ = mw.WriteField("name", "logo")
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/templates/tpl_fresh/assets", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	pr := &auth.Principal{User: &models.User{ID: "u1", Role: "member"}}
	req = req.WithContext(auth.WithPrincipal(context.Background(), pr))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "tpl_fresh")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rr := httptest.NewRecorder()
	s.handleUploadAsset(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]interface{}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	aid, _ := resp["id"].(string)
	if aid == "" {
		t.Fatalf("no asset id in response: %s", rr.Body.String())
	}
	// 落盘文件确实存在。
	var asset models.TemplateAsset
	s.DB.First(&asset, "id = ?", aid)
	if _, err := os.Stat(asset.FilePath); err != nil {
		t.Errorf("asset file not on disk: %v", err)
	}
}

func TestNextVersionNumber(t *testing.T) {
	s, _ := setupServer(t)
	cases := []struct {
		name     string
		versions []string
		want     string
	}{
		{"only-1.0.0", []string{"1.0.0"}, "1.0.1"},
		{"with-gap", []string{"1.0.0", "1.0.2", "1.0.3", "1.0.4", "1.0.5"}, "1.0.6"},
		{"empty", []string{}, "1.0.1"},
		{"two-segment", []string{"1.0"}, "1.0.1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tplID := "tpl_ver_" + c.name
			s.DB.Create(&models.Template{
				ID: tplID, Code: "c-" + c.name, Name: "n", CreatedBy: "u1",
				Visibility: "public", Enabled: true,
			})
			for i, v := range c.versions {
				s.DB.Create(&models.TemplateVersion{
					ID: fmt.Sprintf("v_%s_%d", c.name, i), TemplateID: tplID, Version: v,
					UploadedBy: "u1", Source: "upload",
				})
			}
			got := s.nextVersionNumber(tplID)
			if got != c.want {
				t.Errorf("next=%s want %s (existing %v)", got, c.want, c.versions)
			}
		})
	}
}
