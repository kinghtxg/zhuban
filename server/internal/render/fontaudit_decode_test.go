package render

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// 这一条守的是「体检结论里新加的字段，会不会在 Go 这一层被悄悄吃掉」。
//
// 为什么值得单独一条：render-service 返回的是裸 JSON，Go 用结构体接。
// **结构体没声明的字段解码时直接消失，不报错、不告警**，于是：
// Python 侧 37 个用例全绿、Go 侧接口 200、前端拿到 undefined 就安静地
// 不渲染那两块——三层都"通过"，用户看到的却还是老样子。
//
// 所以这条用例按 render-service 的**真实输出形状**喂一遍，
// 断言中文/西文分工、逐段归因、脏族名这三样都还在。
func TestFontAuditDecodesRoleAndParagraphFields(t *testing.T) {
	const payload = `{
	  "ok": true,
	  "checked": 11,
	  "missing": [],
	  "severe": [],
	  "fonts": [
	    {"family": "Arial", "status": "exact", "resolved": "Arial", "parts": ["正文"],
	     "clean_name": "Arial", "note": ""}
	  ],
	  "roles": {
	    "eastAsia": {"label": "中文", "fonts": [
	      {"family": "仿宋_GB2312", "status": "exact", "resolved": "仿宋_GB2312",
	       "segments": 51, "parts": ["正文"]},
	      {"family": "宋体", "status": "exact", "resolved": "宋体",
	       "segments": 71, "parts": ["正文", "页眉"]}
	    ]},
	    "latin": {"label": "西文", "fonts": [
	      {"family": "Times New Roman", "status": "exact", "resolved": "Times New Roman",
	       "segments": 28, "parts": ["正文"]}
	    ]}
	  },
	  "paragraphs": [
	    {"part": "正文", "index": 6, "style": "7", "text": "1.3. 范围2",
	     "cjk": {"family": "仿宋_GB2312", "status": "exact", "resolved": "仿宋_GB2312"},
	     "latin": {"family": "Times New Roman", "status": "exact", "resolved": "Times New Roman"}},
	    {"part": "正文", "index": 9, "text": "渗透测试报告",
	     "cjk": {"family": "黑体", "status": "exact", "resolved": "黑体"},
	     "latin": null}
	  ],
	  "suspects": [
	    {"family": "Calibri (西文正文)", "reason": "族名里带括号注解"}
	  ],
	  "runs_error": ""
	}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	c := New(srv.URL, 5*time.Second, 5*time.Second, 5*time.Second)
	got, err := c.FontAudit(context.Background(), []byte("docx"))
	if err != nil {
		t.Fatalf("调用 /fontaudit 失败：%v", err)
	}

	// 角色分工：中文两支、西文一支，段数要跟着过来（前端排序与"71 段"文案都靠它）
	ea, ok := got.Roles["eastAsia"]
	if !ok {
		t.Fatalf("roles 里没有 eastAsia —— 中文用什么字体这条信息丢了：%+v", got.Roles)
	}
	if ea.Label != "中文" || len(ea.Fonts) != 2 {
		t.Fatalf("eastAsia 角色解出来不对：%+v", ea)
	}
	if ea.Fonts[1].Segments != 71 {
		t.Errorf("segments 丢了（应 71，实际 %d）—— 界面上「71 段」会变成「0 段」",
			ea.Fonts[1].Segments)
	}
	if lat := got.Roles["latin"]; lat.Label != "西文" || lat.Fonts[0].Family != "Times New Roman" {
		t.Errorf("latin 角色解出来不对：%+v", got.Roles["latin"])
	}

	// 逐段归因：这一段汉字是仿宋、西文是 Times New Roman
	if len(got.Paragraphs) != 2 {
		t.Fatalf("paragraphs 丢了（应 2 条，实际 %d）", len(got.Paragraphs))
	}
	p := got.Paragraphs[0]
	if p.CJK == nil || p.CJK.Family != "仿宋_GB2312" {
		t.Errorf("第 1 段的 CJK 归属不对：%+v", p.CJK)
	}
	if p.Latin == nil || p.Latin.Family != "Times New Roman" {
		t.Errorf("第 1 段的 Latin 归属不对：%+v", p.Latin)
	}
	if p.Style != "7" || p.Index != 6 {
		t.Errorf("段落定位信息不全（part/index/style 要能指回原文）：%+v", p)
	}
	// 纯中文段落没有西文槽位：必须是 nil，不能被解成空结构体
	if got.Paragraphs[1].Latin != nil {
		t.Errorf("纯中文段落的 latin 应为 null，实际 %+v", got.Paragraphs[1].Latin)
	}

	// 脏族名单独一路，且**不能**混进 missing/severe
	if len(got.Suspects) != 1 || got.Suspects[0].Family != "Calibri (西文正文)" {
		t.Errorf("suspects 丢了：%+v", got.Suspects)
	}
	if len(got.Severe) != 0 {
		t.Errorf("脏族名不该让 severe 变非空（会造成假报）：%v", got.Severe)
	}
	if got.Fonts[0].CleanName != "Arial" {
		t.Errorf("clean_name 没解出来：%+v", got.Fonts[0])
	}
}
