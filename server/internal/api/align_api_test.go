package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"docforge/server/internal/models"
	"docforge/server/internal/render"

	"github.com/go-chi/chi/v5"
)

// 这一组守的是「自由写作 → 自动对位」这条链路的**非算法**部分：
// 用户写的内容有没有原样送到渲染服务、权限与体积边界有没有守住、
// 渲染服务说错话时主服务有没有把它翻译成话。
//
// 对齐算法本身在 render-service/tests/test_align.py 里测，这里不重复——
// 但有一条必须在这里守：**这条接口不许落笔**。它只产出建议，用户确认后
// 才走 /render。一旦哪天有人图省事让它"顺手把内容写进表单"，格式风险就
// 从零变成了"整份文档的段落都可能是引擎改过的"。

func alignRouter(s *Server) http.Handler {
	r := chi.NewRouter()
	r.Post("/templates/{id}/align", s.handleTemplateAlign)
	return r
}

// alignReq 用给定身份打一次 POST /templates/{id}/align。u 为 nil 表示未登录。
func alignReq(t *testing.T, s *Server, path string, body interface{}, u *models.User) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if u != nil {
		req = withUser(req, u)
	}
	rec := httptest.NewRecorder()
	alignRouter(s).ServeHTTP(rec, req)
	return rec
}

var alignAdmin = &models.User{ID: "u_align", Username: "align", Role: "admin"}

// TestAlignForwardsContentVerbatim 内容必须原样透传，尤其是图。
//
// 这里最容易出的错是"顺手清洗一下"：Go 侧觉得 <img> 危险就删掉，或者为
// 了日志好看把 HTML 转义一遍。清洗一次，字体识别就少一截依据，图片也
// 一起没了——而接口 200、建议照样返回，只是对不准。
func TestAlignForwardsContentVerbatim(t *testing.T) {
	s, _ := setupServer(t)
	fake := &fakeRenderer{}
	s.Renderer = fake
	seedTemplate(t, s, "tpl_al", "ver_al", "1.0.0", "u_owner", true)

	content := "<h2>漏洞详情</h2><p>在挂号查询接口发现 SQL 注入</p>" +
		`<p><img src="data:image/png;base64,AAAA"/></p>`
	rec := alignReq(t, s, "/templates/tpl_al/align",
		map[string]string{"content": content}, alignAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if fake.alignContent != content {
		t.Errorf("内容被改动了：\n送去 %s\n回来 %s", content, fake.alignContent)
	}
	if fake.alignMime != "html" {
		t.Errorf("没写 mime 时应默认 html，实际 %q", fake.alignMime)
	}
	if string(fake.alignDocx) != "ORIGINAL-DOCX" {
		t.Errorf("送去的 docx 不是当前版本文件：%q", fake.alignDocx)
	}

	// 渲染服务的原始 JSON 要原样回来（含前端画"这段落到哪儿"要用的
	// paragraphs/slots）。Go 侧再翻译一遍只会多一处要同步维护的假真相。
	var out map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是 JSON：%v（%s）", err, rec.Body.String())
	}
	if out["ok"] != true {
		t.Errorf("ok 没透传：%v", out["ok"])
	}
	if _, has := out["paragraphs"]; !has {
		t.Errorf("paragraphs 没透传，前端画不了段落列表：%v", out)
	}
}

// TestAlignRejectsUnknownMime 写错 mime 必须报错，不许静默退化成纯文本。
//
// 退化后的表现是「建议莫名其妙对不准」——用户完全查不到原因，因为丢掉的
// 恰恰是字体/字号这些识别依据，而它不出现在任何地方的报错里。
func TestAlignRejectsUnknownMime(t *testing.T) {
	s, _ := setupServer(t)
	fake := &fakeRenderer{}
	s.Renderer = fake
	seedTemplate(t, s, "tpl_m", "ver_m", "1.0.0", "u_owner", true)

	rec := alignReq(t, s, "/templates/tpl_m/align",
		map[string]string{"content": "正文", "mime": "htm"}, alignAdmin)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("mime 写错应 400，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if fake.alignContent != "" {
		t.Error("参数没通过校验就不该已经调用渲染服务")
	}
}

func TestAlignRejectsEmptyContent(t *testing.T) {
	s, _ := setupServer(t)
	s.Renderer = &fakeRenderer{}
	seedTemplate(t, s, "tpl_e", "ver_e", "1.0.0", "u_owner", true)

	rec := alignReq(t, s, "/templates/tpl_e/align",
		map[string]string{"content": "   \n  "}, alignAdmin)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("空白内容应 400，实际 %d：%s", rec.Code, rec.Body.String())
	}
}

// TestAlignRejectsOversizeContent 带图的报告很容易十几 MB，但要有上限。
func TestAlignRejectsOversizeContent(t *testing.T) {
	s, _ := setupServer(t)
	s.Renderer = &fakeRenderer{}
	seedTemplate(t, s, "tpl_big", "ver_big", "1.0.0", "u_owner", true)

	huge := strings.Repeat("A", maxAlignContentBytes+1)
	rec := alignReq(t, s, "/templates/tpl_big/align",
		map[string]string{"content": huge}, alignAdmin)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超限应 413，实际 %d：%s", rec.Code, rec.Body.String())
	}
}

func TestAlignRequiresLogin(t *testing.T) {
	s, _ := setupServer(t)
	s.Renderer = &fakeRenderer{}
	seedTemplate(t, s, "tpl_n", "ver_n", "1.0.0", "u_owner", true)

	rec := alignReq(t, s, "/templates/tpl_n/align",
		map[string]string{"content": "正文"}, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("未登录应 401，实际 %d：%s", rec.Code, rec.Body.String())
	}
}

// TestAlignUsesReadPermissionNotEdit 填表的人能用模板就够了，不必是模板作者。
//
// 用编辑权限挡会直接毁掉这个功能：绝大多数人是在填别人建的模板。
func TestAlignUsesReadPermissionNotEdit(t *testing.T) {
	s, _ := setupServer(t)
	fake := &fakeRenderer{}
	s.Renderer = fake
	// public 模板，创建者是别人
	seedTemplate(t, s, "tpl_pub", "ver_pub", "1.0.0", "u_someone_else", true)

	reader := &models.User{ID: "u_reader", Username: "reader", Role: "member"}
	rec := alignReq(t, s, "/templates/tpl_pub/align",
		map[string]string{"content": "正文"}, reader)
	if rec.Code != http.StatusOK {
		t.Fatalf("非作者的 public 模板应能对齐，实际 %d：%s", rec.Code, rec.Body.String())
	}
}

// TestAlignMapsRendererError 渲染服务返回业务错误时，主服务要原样翻译状态码，
// 不能让用户看到"服务内部错误"。
func TestAlignMapsRendererError(t *testing.T) {
	s, _ := setupServer(t)
	fake := &fakeRenderer{alignErr: &render.Error{
		StatusCode: http.StatusUnprocessableEntity, Code: "data_invalid", Message: "模板结构解析失败",
	}}
	s.Renderer = fake
	seedTemplate(t, s, "tpl_er", "ver_er", "1.0.0", "u_owner", true)

	rec := alignReq(t, s, "/templates/tpl_er/align",
		map[string]string{"content": "正文"}, alignAdmin)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("应透传 422，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "模板结构解析失败") {
		t.Errorf("错误信息没带上：%s", rec.Body.String())
	}
}

func TestAlignOnMissingTemplate(t *testing.T) {
	s, _ := setupServer(t)
	s.Renderer = &fakeRenderer{}
	rec := alignReq(t, s, "/templates/nope/align",
		map[string]string{"content": "正文"}, alignAdmin)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("模板不存在应 404，实际 %d：%s", rec.Code, rec.Body.String())
	}
}
