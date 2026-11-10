package api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"docforge/server/internal/auth"
	"docforge/server/internal/models"

	"github.com/go-chi/chi/v5"
)

// 这一组守的是「录入模板那一刻的字体体检结论，有没有真的送到用户眼前」。
//
// 背景：模板里写「仿宋_GB2312」，本机没有这个族名时**一个字都不会报错**——
// 接口 200、正文全对、PDF 能打开，只有字形被悄悄换成宋体或干脆逐字回退。
// 用户在自己 Windows 上打开模板是好的，所以他没有任何线索怀疑。
// 渲染服务已经能体检（fontaudit.py），这里守的是主服务这条链路：
//   - 上传时把结论随版本一起存下来（快照）
//   - 详情接口要把它下发出去（否则前端拿不到就等于没做）
//   - 允许按需重算（运维导入字体后老模板的结论需要能刷新）

// postNewTemplate 走真实路由 POST /templates（code/name 校验都在 handler 里）。
// 返回路由器本身，后续 GET 复用同一套路由，避免为了测试给 Server 加字段。
func postNewTemplate(t *testing.T, s *Server, code string) (http.Handler, *httptest.ResponseRecorder) {
	t.Helper()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "t.docx")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write([]byte("DOCX-BYTES")); err != nil {
		t.Fatal(err)
	}
	_ = mw.WriteField("code", code)
	_ = mw.WriteField("name", "字体体检用例")
	_ = mw.WriteField("visibility", "private")
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	p := &auth.Principal{User: &models.User{ID: "u_font", Username: "font", Role: "admin"}}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(auth.WithPrincipal(req.Context(), p)))
		})
	})
	r.Post("/templates", s.handleCreateTemplate)
	r.Get("/templates/{id}", s.handleTemplateDetail)
	r.Get("/templates/{id}/font-audit", s.handleTemplateFontAudit)

	req := httptest.NewRequest(http.MethodPost, "/templates", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return r, rec
}

// createTemplate 建一份模板并返回 (路由器, 模板 id)。
func createTemplate(t *testing.T, s *Server, code string) (http.Handler, string) {
	t.Helper()
	h, rec := postNewTemplate(t, s, code)
	if rec.Code != http.StatusCreated {
		t.Fatalf("建模板期望 201，实际 %d：%s", rec.Code, rec.Body.String())
	}
	var created map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("响应里没有模板 id：%s", rec.Body.String())
	}
	return h, id
}

func getJSONFrom(t *testing.T, h http.Handler, path string) map[string]interface{} {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s 期望 200，实际 %d：%s", path, rec.Code, rec.Body.String())
	}
	var out map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析 %s 响应失败：%v（body=%s）", path, err, rec.Body.String())
	}
	return out
}

// TestCreateTemplateStoresFontAudit 上传建模板时，体检结论必须落进版本行。
func TestCreateTemplateStoresFontAudit(t *testing.T) {
	s, _ := setupServer(t)

	_, tplID := createTemplate(t, s, "font-audit-01")

	var ver models.TemplateVersion
	if err := s.DB.Where("template_id = ?", tplID).First(&ver).Error; err != nil {
		t.Fatalf("读取版本失败：%v", err)
	}
	if len(ver.FontAudit) == 0 {
		t.Fatal("版本行里没有 font_audit —— 上传时的字体体检结论被丢掉了")
	}
	var audit map[string]interface{}
	if err := json.Unmarshal(ver.FontAudit, &audit); err != nil {
		t.Fatalf("font_audit 不是合法 JSON：%v", err)
	}
	if ok, _ := audit["ok"].(bool); !ok {
		t.Errorf("mock 返回的是 ok=true 的报告，入库却是 %v", audit["ok"])
	}
	missing, _ := audit["missing"].([]interface{})
	if len(missing) != 1 || missing[0] != "仿宋_GB2312" {
		t.Errorf("missing 应原样透传为 [仿宋_GB2312]，实际 %v", audit["missing"])
	}
}

// TestTemplateDetailCarriesFontAudit 详情必须下发，否则前端拿不到等于没做。
func TestTemplateDetailCarriesFontAudit(t *testing.T) {
	s, _ := setupServer(t)
	h, tplID := createTemplate(t, s, "font-audit-02")

	d := getJSONFrom(t, h, "/templates/"+tplID)

	cur, _ := d["current_version"].(map[string]interface{})
	if cur == nil {
		t.Fatalf("详情里没有 current_version：%v", d)
	}
	fa, _ := cur["font_audit"].(map[string]interface{})
	if fa == nil {
		t.Fatal("current_version 里没有 font_audit —— 前端无从展示字体提醒")
	}
	miss, _ := fa["missing"].([]interface{})
	if len(miss) != 1 || miss[0] != "仿宋_GB2312" {
		t.Errorf("下发的 missing 不对：%v", fa["missing"])
	}
	// severe 必须单独下发：它是「汉字可能根本显示不出来」那一档，
	// 和 fallback（字形被换）不是一个严重程度，前端要能分开说。
	sev, _ := fa["severe"].([]interface{})
	if len(sev) != 1 {
		t.Errorf("severe 应单独下发，实际 %v", fa["severe"])
	}
	// 逐字体明细也要在：用户需要知道「哪几处在用、会显示成什么」。
	fonts, _ := fa["fonts"].([]interface{})
	if len(fonts) != 2 {
		t.Fatalf("fonts 明细应为 2 条，实际 %v", fa["fonts"])
	}
	first, _ := fonts[0].(map[string]interface{})
	if first["resolved"] != "Noto Serif CJK SC" {
		t.Errorf("每条要给 resolved（会显示成什么），实际 %v", first["resolved"])
	}

	// 角色分工要下发：「中文用什么、西文用什么」是用户真正问的那个问题。
	// 只给一张不分角色的族名清单，他看得见两支字体，却答不上哪支管汉字。
	roles, _ := fa["roles"].(map[string]interface{})
	ea, _ := roles["eastAsia"].(map[string]interface{})
	if ea == nil {
		t.Fatalf("roles.eastAsia 没下发 —— 前端「中文：仿宋_GB2312 51 段」那一行会是空的：%v", roles)
	}
	if ea["label"] != "中文" {
		t.Errorf("角色名要带过来，实际 %v", ea["label"])
	}
	eaFonts, _ := ea["fonts"].([]interface{})
	if len(eaFonts) != 1 {
		t.Fatalf("eastAsia 下的字体数不对：%v", eaFonts)
	}
	if seg, _ := eaFonts[0].(map[string]interface{})["segments"].(float64); int(seg) != 51 {
		t.Errorf("segments 丢了（应 51，实际 %v）—— 界面会显示「0 段」", eaFonts[0])
	}

	// 逐段归因也要下发，且 part/index/style 都要在（否则指不回原文）
	paras, _ := fa["paragraphs"].([]interface{})
	if len(paras) != 1 {
		t.Fatalf("paragraphs 没下发：%v", fa["paragraphs"])
	}
	p0, _ := paras[0].(map[string]interface{})
	if p0["index"] == nil || p0["part"] == nil {
		t.Errorf("逐段归因缺定位字段：%v", p0)
	}
	cjk, _ := p0["cjk"].(map[string]interface{})
	if cjk == nil || cjk["family"] != "仿宋_GB2312" {
		t.Errorf("逐段的中文归属不对：%v", p0["cjk"])
	}

	// 脏族名单独一路：要说，但**不能**进 missing（假报会让真报警失效）
	sus, _ := fa["suspects"].([]interface{})
	if len(sus) != 1 {
		t.Errorf("suspects 没下发：%v", fa["suspects"])
	}
}

// TestFontAuditEndpointSeparatesRecordedFromFresh 按需重算要能区分「当时」和「现在」。
//
// 这是这条接口存在的唯一理由：管理员后来把缺的字体导入了，老模板上仍写着
// 「缺 仿宋_GB2312」。那句话在当时是真的、现在不真了。把两份并列返回，
// 页面才能说清「录入时缺 1 个，现在缺 0 个」——只返回新的会让人以为当初记录是错的。
func TestFontAuditEndpointSeparatesRecordedFromFresh(t *testing.T) {
	s, _ := setupServer(t)
	fake := &fakeRenderer{}
	s.Renderer = fake
	seedTemplate(t, s, "tpl_fa", "ver_fa", "1.0.0", "u_owner", true)
	// 给版本补一份「当时缺字体」的快照
	if err := s.DB.Model(&models.TemplateVersion{}).Where("id = ?", "ver_fa").
		Update("font_audit", models.JSONB(`{"ok":true,"checked":1,"missing":["仿宋_GB2312"],"severe":["仿宋_GB2312"],"fonts":[]}`)).Error; err != nil {
		t.Fatal(err)
	}

	r := chi.NewRouter()
	r.Get("/templates/{id}/font-audit", s.handleTemplateFontAudit)

	// 此刻本机字体仍然缺：最新结论与快照一致
	out := getJSONFrom(t, r, "/templates/tpl_fa/font-audit")
	if out["version"] != "1.0.0" {
		t.Errorf("应回带版本号，实际 %v", out["version"])
	}
	if out["recorded"] == nil {
		t.Error("recorded 应回带录入时的快照")
	}

	// 导入字体后：最新结论变干净，但 recorded 必须保持原样
	fake.fontAuditClear = true
	out = getJSONFrom(t, r, "/templates/tpl_fa/font-audit")
	fresh, _ := out["font_audit"].(map[string]interface{})
	if fresh == nil {
		t.Fatalf("没有 font_audit：%v", out)
	}
	if miss, _ := fresh["missing"].([]interface{}); len(miss) != 0 {
		t.Errorf("重算后不该再报缺字体，实际 %v", fresh["missing"])
	}
	rec, _ := out["recorded"].(map[string]interface{})
	if rec == nil {
		t.Fatal("recorded 丢了")
	}
	if miss, _ := rec["missing"].([]interface{}); len(miss) != 1 {
		t.Errorf("recorded 必须保持录入时的原样，实际 %v", rec["missing"])
	}
}

// TestFontAuditEndpointRejectsForeignVersion 版本 id 必须属于该模板。
// 否则就是一个跨模板读别人模板用字的口子（虽然只是字体名，也没必要开）。
func TestFontAuditEndpointRejectsForeignVersion(t *testing.T) {
	s, _ := setupServer(t)
	s.Renderer = &fakeRenderer{}
	seedTemplate(t, s, "tpl_mine", "ver_mine", "1.0.0", "u_owner", true)
	seedTemplate(t, s, "tpl_other", "ver_other", "1.0.0", "u_other", false)

	r := chi.NewRouter()
	r.Get("/templates/{id}/font-audit", s.handleTemplateFontAudit)

	req := httptest.NewRequest(http.MethodGet, "/templates/tpl_mine/font-audit?version_id=ver_other", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("拿别的模板的版本 id 应 404，实际 %d：%s", rec.Code, rec.Body.String())
	}
}

// TestFontAuditEndpointWithoutVersion 模板还没版本时给出明确的 404，而不是 500。
func TestFontAuditEndpointWithoutVersion(t *testing.T) {
	s, _ := setupServer(t)
	s.Renderer = &fakeRenderer{}
	tpl := models.Template{ID: "tpl_empty", Code: "tpl-empty", Name: "空", Visibility: "private", Enabled: true}
	if err := s.DB.Create(&tpl).Error; err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	r.Get("/templates/{id}/font-audit", s.handleTemplateFontAudit)
	req := httptest.NewRequest(http.MethodGet, "/templates/tpl_empty/font-audit", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("无版本应 404，实际 %d：%s", rec.Code, rec.Body.String())
	}
}

// preflight 打 POST /templates/font-audit：选完文件就能问，不必先建模板。
func preflight(t *testing.T, s *Server, withFile bool) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if withFile {
		fw, err := mw.CreateFormFile("file", "t.docx")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write([]byte("DOCX-BYTES")); err != nil {
			t.Fatal(err)
		}
	}
	_ = mw.Close()

	r := chi.NewRouter()
	r.Post("/templates/font-audit", s.handleFontAuditPreflight)
	req := httptest.NewRequest(http.MethodPost, "/templates/font-audit", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// TestFontAuditPreflightTalksBeforeCreating 预检的核心承诺：**不产生任何模板**。
//
// 提醒的价值全在「还来得及」这四个字上。如果预检顺手建了个模板，
// 用户为了看清缺什么字体就先被塞了一个半成品，那还不如不做。
func TestFontAuditPreflightTalksBeforeCreating(t *testing.T) {
	s, _ := setupServer(t)
	rec := preflight(t, s, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("预检期望 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	var out map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	fa, _ := out["font_audit"].(map[string]interface{})
	if fa == nil {
		t.Fatalf("预检响应里没有 font_audit：%s", rec.Body.String())
	}
	if miss, _ := fa["missing"].([]interface{}); len(miss) != 1 {
		t.Errorf("应报出 1 个缺失字体，实际 %v", fa["missing"])
	}

	var cnt int64
	s.DB.Model(&models.Template{}).Count(&cnt)
	if cnt != 0 {
		t.Errorf("预检不该创建模板，实际库里已有 %d 个", cnt)
	}
	var vcnt int64
	s.DB.Model(&models.TemplateVersion{}).Count(&vcnt)
	if vcnt != 0 {
		t.Errorf("预检不该落版本行，实际 %d 个", vcnt)
	}
}

// TestFontAuditPreflightRequiresFile 没带文件时给 400，不要 500。
func TestFontAuditPreflightRequiresFile(t *testing.T) {
	s, _ := setupServer(t)
	rec := preflight(t, s, false)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺文件应 400，实际 %d：%s", rec.Code, rec.Body.String())
	}
}
