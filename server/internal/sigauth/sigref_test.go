package sigauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"docforge/server/internal/models"
)

// genPNG 是渲染服务「出一张图」的替身返回值。
// 只用 PNG 魔数 + 可辨识的尾巴：Go 这边不解析像素，只负责把它塞进 data:URI。
var genPNG = []byte("\x89PNG\r\n\x1a\nFAKE-GENERATED")

type fakeRenderer struct {
	sigFields  map[string]string
	sealFields map[string]string
	sigCalls   int
	sealCalls  int
	png        []byte
	err        error
}

func (f *fakeRenderer) RenderSignature(_ context.Context, fields map[string]string) ([]byte, error) {
	f.sigCalls++
	f.sigFields = fields
	return f.png, f.err
}

func (f *fakeRenderer) RenderSeal(_ context.Context, fields map[string]string) ([]byte, error) {
	f.sealCalls++
	f.sealFields = fields
	return f.png, f.err
}

func dataURI(b []byte) string { return "data:image/png;base64," + base64.StdEncoding.EncodeToString(b) }

// mkRes 造一条已审批通过的签名/印章资源，带字体与署名（生成来源的样子）。
func mkRes(t *testing.T, r *Resolver, kind, id, owner, font, signer string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), id+".png")
	if err := os.WriteFile(p, []byte("\x89PNG\r\n\x1a\nORIGINAL"), 0o644); err != nil {
		t.Fatal(err)
	}
	if kind == "seal" {
		x := models.Seal{ID: id, Name: "公章", OwnerID: owner, Source: "generated",
			UnitName: "某某科技有限公司", SealName: "合同专用章", Shape: "circle",
			FilePath: p, WidthMM: 40, HeightMM: 40, Status: "approved"}
		if err := r.DB.Create(&x).Error; err != nil {
			t.Fatal(err)
		}
		return
	}
	x := models.Signature{ID: id, Name: "张三签", OwnerID: owner, Source: "generated",
		SignerName: signer, FontID: font, FilePath: p, WidthMM: 60, HeightMM: 24, Status: "approved"}
	if err := r.DB.Create(&x).Error; err != nil {
		t.Fatal(err)
	}
}

// injectInto 跑一次 Inject 并返回解析后的 map 与「被注入的那个值」。
func injectInto(t *testing.T, r *Resolver, data string, opts Options) (map[string]interface{}, map[string]interface{}, error) {
	t.Helper()
	out, err := r.Inject([]byte(data), opts)
	if err != nil {
		return nil, nil, err
	}
	var m map[string]interface{}
	if uerr := json.Unmarshal(out, &m); uerr != nil {
		t.Fatalf("注入结果不是合法 JSON：%v", uerr)
	}
	img, _ := m["sig_审核人"].(map[string]interface{})
	if img == nil {
		img, _ = m["seal_公章"].(map[string]interface{})
	}
	return m, img, nil
}

// TestSigRef_GeneratesWithTimestampExpr 是本轮需求的正面用例：
// 引用一张**没有时间**的签名资源 + 字体编号 + 时间表达式，
// 由服务端在出图那一刻生成带时间的签字版本。
func TestSigRef_GeneratesWithTimestampExpr(t *testing.T) {
	r, _, _ := setup(t)
	f := &fakeRenderer{png: genPNG}
	r.Renderer = f
	mkRes(t, r, "signature", "sig_abcdef", "u_owner", "liu-jian-mao-cao", "张三")

	data := `{"sig_审核人":{"$sig":"sig_abcdef","with_timestamp":true,` +
		`"timestamp":{"$date":"now","format":"%Y-%m-%d %H:%M"}}}`
	_, img, err := injectInto(t, r, data, Options{GranteeType: "user", GranteeID: "u_owner"})
	if err != nil {
		t.Fatal(err)
	}

	if f.sigCalls != 1 {
		t.Fatalf("应调一次签名出图，实际 %d 次", f.sigCalls)
	}
	// 字体/署名没写在参数里 → 继承资源（用户只需给「引用 + 时间」）
	if f.sigFields["font_id"] != "liu-jian-mao-cao" {
		t.Fatalf("font_id 应继承资源，实际 %q", f.sigFields["font_id"])
	}
	if f.sigFields["signer_name"] != "张三" {
		t.Fatalf("signer_name 应继承资源，实际 %q", f.sigFields["signer_name"])
	}
	if f.sigFields["with_timestamp"] != "true" {
		t.Fatalf("with_timestamp 未透传：%v", f.sigFields)
	}
	// ★ 关键断言：Go 侧**不求值**日期表达式，原样转交给渲染服务。
	// 一旦 Go 自己算一遍，就会出现两套「北京时间怎么算」的答案。
	if !strings.Contains(f.sigFields["timestamp_expr"], `"$date":"now"`) {
		t.Fatalf("timestamp_expr 应原样透传表达式，实际 %q", f.sigFields["timestamp_expr"])
	}
	// 注入出来的必须是渲染服务返回的那张图，不是资源原图
	if img == nil || img["src"] != dataURI(genPNG) {
		t.Fatalf("注入的 src 不是现场出图的结果：%v", img)
	}
	if img["type"] != "image" {
		t.Fatalf("注入对象 type 应为 image，实际 %v", img["type"])
	}
	// 尺寸沿用资源记录的宽度，高度留 0 让下游按原图比例算
	if w, _ := img["width"].(float64); w != 60 {
		t.Fatalf("width 应为资源记录的 60，实际 %v", img["width"])
	}
}

// TestSigRef_OverridesFontAndName 参数对象可以覆盖资源自带的字体与署名。
func TestSigRef_OverridesFontAndName(t *testing.T) {
	r, _, _ := setup(t)
	f := &fakeRenderer{png: genPNG}
	r.Renderer = f
	mkRes(t, r, "signature", "sig_abcdef", "u_owner", "ma-shan-zheng", "张三")

	data := `{"sig_审核人":{"$sig":"sig_abcdef","font":"zhi-mang-xing","signer_name":"李四",` +
		`"timestamp":"2026-10-25T09:30:00+08:00","width_mm":45}}`
	_, img, err := injectInto(t, r, data, Options{GranteeType: "user", GranteeID: "u_owner"})
	if err != nil {
		t.Fatal(err)
	}
	if f.sigFields["font_id"] != "zhi-mang-xing" || f.sigFields["signer_name"] != "李四" {
		t.Fatalf("覆盖未生效：%v", f.sigFields)
	}
	if f.sigFields["width_mm"] != "45.00" {
		t.Fatalf("width_mm 应为 45.00，实际 %q", f.sigFields["width_mm"])
	}
	// 给了字面时间就等于要印时间，不必再写 with_timestamp
	if f.sigFields["with_timestamp"] != "true" {
		t.Fatalf("字面时间应隐含 with_timestamp=true：%v", f.sigFields)
	}
	// 字面时间走 timestamp 字段，不走表达式字段
	if f.sigFields["timestamp"] != "2026-10-25T09:30:00+08:00" || f.sigFields["timestamp_expr"] != "" {
		t.Fatalf("字面时间应原样进 timestamp：%v", f.sigFields)
	}
	if w, _ := img["width"].(float64); w != 45 {
		t.Fatalf("注入宽度应为 45，实际 %v", img["width"])
	}
}

// TestSigRef_OnlySizeOverrideSkipsRender 只改尺寸不该多一次出图。
func TestSigRef_OnlySizeOverrideSkipsRender(t *testing.T) {
	r, _, _ := setup(t)
	f := &fakeRenderer{png: genPNG}
	r.Renderer = f
	mkRes(t, r, "signature", "sig_abcdef", "u_owner", "ma-shan-zheng", "张三")

	_, img, err := injectInto(t, r, `{"sig_审核人":{"$sig":"sig_abcdef","width_mm":30,"height_mm":12}}`,
		Options{GranteeType: "user", GranteeID: "u_owner"})
	if err != nil {
		t.Fatal(err)
	}
	if f.sigCalls != 0 {
		t.Fatalf("只改尺寸不应调渲染服务，实际 %d 次", f.sigCalls)
	}
	if w, _ := img["width"].(float64); w != 30 {
		t.Fatalf("width 应被覆盖成 30，实际 %v", img["width"])
	}
	if h, _ := img["height"].(float64); h != 12 {
		t.Fatalf("height 应被覆盖成 12，实际 %v", img["height"])
	}
}

// TestSigRef_ParamsCannotBypassGrant 参数对象不能绕过授权。
// 这是本轮最要紧的安全断言：否则传个 font 就能自己画一个签贴上去。
func TestSigRef_ParamsCannotBypassGrant(t *testing.T) {
	r, _, _ := setup(t)
	f := &fakeRenderer{png: genPNG}
	r.Renderer = f
	mkRes(t, r, "signature", "sig_abcdef", "u_owner", "ma-shan-zheng", "张三")

	data := `{"sig_审核人":{"$sig":"sig_abcdef","font":"ma-shan-zheng","signer_name":"张三",` +
		`"with_timestamp":true}}`
	_, _, err := injectInto(t, r, data, Options{GranteeType: "user", GranteeID: "u_stranger"})
	var denied *ErrDenied
	if !errors.As(err, &denied) {
		t.Fatalf("无授权应 403 grant_denied，实际 %v", err)
	}
	// 拒绝就一个字也别画：不能出现「白算一张图再报错」
	if f.sigCalls != 0 {
		t.Fatalf("被拒绝时不应调用渲染服务，实际 %d 次", f.sigCalls)
	}
}

// TestSigRef_GenerationStillConsumesGrant 现场出图这条路同样扣次数。
func TestSigRef_GenerationStillConsumesGrant(t *testing.T) {
	r, _, _ := setup(t)
	f := &fakeRenderer{png: genPNG}
	r.Renderer = f
	mkRes(t, r, "signature", "sig_abcdef", "u_owner", "ma-shan-zheng", "张三")
	grantFor(t, r, "sig_abcdef", "u_member", 1, nil, nil)

	data := `{"sig_审核人":{"$sig":"sig_abcdef","with_timestamp":true}}`
	opts := Options{GranteeType: "user", GranteeID: "u_member"}
	if _, _, err := injectInto(t, r, data, opts); err != nil {
		t.Fatalf("第一次应放行：%v", err)
	}
	_, _, err := injectInto(t, r, data, opts)
	var denied *ErrDenied
	if !errors.As(err, &denied) {
		t.Fatalf("次数用尽应 403，实际 %v", err)
	}
	if denied.Reason != "exhausted" {
		t.Fatalf("拒绝原因应为 exhausted，实际 %s", denied.Reason)
	}
	if f.sigCalls != 1 {
		t.Fatalf("出图应只发生一次（第二次被拒），实际 %d 次", f.sigCalls)
	}
}

// TestSigRef_UnknownKeyRejected 未知字段必须报错——静默忽略等于「以为生效了」。
func TestSigRef_UnknownKeyRejected(t *testing.T) {
	r, _, _ := setup(t)
	r.Renderer = &fakeRenderer{png: genPNG}
	mkRes(t, r, "signature", "sig_abcdef", "u_owner", "ma-shan-zheng", "张三")

	cases := []string{
		`{"sig_审核人":{"$sig":"sig_abcdef","timestmap":true}}`,      // 拼错字段名
		`{"sig_审核人":{"$sig":"sig_abcdef","with_timestamp":"yes"}}`, // 类型错
		`{"sig_审核人":{"$sig":"sig_abcdef","$seal":"seal_abcdef"}}`,  // 两个标记
		`{"sig_审核人":{"$sig":"not-an-id"}}`,                         // 值不是资源 ID
		`{"sig_审核人":{"$sig":"sig_abcdef","width_mm":"60"}}`,        // 数字写成字符串
	}
	for i, c := range cases {
		if _, err := r.Inject([]byte(c), Options{GranteeType: "user", GranteeID: "u_owner"}); err == nil {
			t.Fatalf("用例 %d 应报 422，实际通过：%s", i, c)
		} else {
			var pe *ErrParams
			if !errors.As(err, &pe) {
				t.Fatalf("用例 %d 应返回 ErrParams，实际 %T %v", i, err, err)
			}
		}
	}
}

// TestSigRef_MarkerMustMatchKey 键名前缀与标记必须一致。
func TestSigRef_MarkerMustMatchKey(t *testing.T) {
	r, _, _ := setup(t)
	_, err := r.Inject([]byte(`{"sig_审核人":{"$seal":"seal_abcdef"}}`),
		Options{GranteeType: "user", GranteeID: "u_owner"})
	var pe *ErrParams
	if !errors.As(err, &pe) || !strings.Contains(pe.Msg, "标记") {
		t.Fatalf("签名键配 $seal 应报错，实际 %v", err)
	}
}

// TestSigRef_SealRejectsTimestamp 印章不印时间（印章上印时间不合公文规范）。
func TestSigRef_SealRejectsTimestamp(t *testing.T) {
	r, _, _ := setup(t)
	f := &fakeRenderer{png: genPNG}
	r.Renderer = f
	mkRes(t, r, "seal", "seal_abcdef", "u_owner", "", "")

	_, err := r.Inject([]byte(`{"seal_公章":{"$seal":"seal_abcdef","with_timestamp":true}}`),
		Options{GranteeType: "user", GranteeID: "u_owner"})
	var pe *ErrParams
	if !errors.As(err, &pe) {
		t.Fatalf("印章带时间应报错，实际 %v", err)
	}
	if f.sealCalls != 0 {
		t.Fatalf("报错前不该出图，实际 %d 次", f.sealCalls)
	}
}

// TestSigRef_SealGenerates 印章也能按参数现场出图（半径跟占位宽度对齐）。
func TestSigRef_SealGenerates(t *testing.T) {
	r, _, _ := setup(t)
	f := &fakeRenderer{png: genPNG}
	r.Renderer = f
	mkRes(t, r, "seal", "seal_abcdef", "u_owner", "", "")

	_, img, err := injectInto(t, r, `{"seal_公章":{"$seal":"seal_abcdef","width_mm":40,"shape":"ellipse"}}`,
		Options{GranteeType: "user", GranteeID: "u_owner"})
	if err != nil {
		t.Fatal(err)
	}
	if f.sealCalls != 1 || f.sigCalls != 0 {
		t.Fatalf("应只调印章出图，实际 sig=%d seal=%d", f.sigCalls, f.sealCalls)
	}
	if f.sealFields["unit_name"] != "某某科技有限公司" || f.sealFields["seal_name"] != "合同专用章" {
		t.Fatalf("单位/印章名应继承资源：%v", f.sealFields)
	}
	if f.sealFields["shape"] != "ellipse" {
		t.Fatalf("shape 覆盖未生效：%v", f.sealFields)
	}
	if f.sealFields["radius_mm"] != "20.00" {
		t.Fatalf("半径应等于宽度一半 20.00，实际 %q", f.sealFields["radius_mm"])
	}
	if img["src"] != dataURI(genPNG) {
		t.Fatalf("印章注入 src 有误：%v", img)
	}
}

// TestSigRef_MissingDefaultsIs422 资源没记字体就要求参数里给，不能瞎印一个。
func TestSigRef_MissingDefaultsIs422(t *testing.T) {
	r, _, _ := setup(t)
	r.Renderer = &fakeRenderer{png: genPNG}
	// 上传来源：无字体、无署名
	x := models.Signature{ID: "sig_upld01", Name: "扫描件", OwnerID: "u_owner", Source: "upload",
		FilePath: filepath.Join(t.TempDir(), "u.png"), Status: "approved", WidthMM: 60}
	_ = os.WriteFile(x.FilePath, []byte("\x89PNG\r\n\x1a\nX"), 0o644)
	if err := r.DB.Create(&x).Error; err != nil {
		t.Fatal(err)
	}

	_, err := r.Inject([]byte(`{"sig_审核人":{"$sig":"sig_upld01","with_timestamp":true}}`),
		Options{GranteeType: "user", GranteeID: "u_owner"})
	var pe *ErrParams
	if !errors.As(err, &pe) || !strings.Contains(pe.Msg, "font") {
		t.Fatalf("缺字体应 422 并提示 font，实际 %v", err)
	}

	// 补上 font + signer_name 就能出图（上传件也能当素材用，授权仍绑在这条资源上）
	if _, err := r.Inject([]byte(`{"sig_审核人":{"$sig":"sig_upld01","font":"ma-shan-zheng",`+
		`"signer_name":"王五","with_timestamp":true}}`),
		Options{GranteeType: "user", GranteeID: "u_owner"}); err != nil {
		t.Fatalf("补齐参数后应成功：%v", err)
	}
}

// TestSigRef_InlineImageObjectUntouched 用户直接给图片对象的行为不变。
func TestSigRef_InlineImageObjectUntouched(t *testing.T) {
	r, _, _ := setup(t)
	f := &fakeRenderer{png: genPNG}
	r.Renderer = f
	data := `{"sig_审核人":{"type":"image","src":"data:image/png;base64,AAAA","width":30,"height":10}}`
	out, err := r.Inject([]byte(data), Options{GranteeType: "user", GranteeID: "u_owner"})
	if err != nil {
		t.Fatalf("内联图片对象不该报错：%v", err)
	}
	// JSON 重新序列化会换键序，所以按语义比对而不是逐字节
	var got, want map[string]interface{}
	_ = json.Unmarshal(out, &got)
	_ = json.Unmarshal([]byte(data), &want)
	gs, _ := got["sig_审核人"].(map[string]interface{})
	ws, _ := want["sig_审核人"].(map[string]interface{})
	if gs["src"] != ws["src"] {
		t.Fatalf("内联图片对象被改写了：%s", out)
	}
	if f.sigCalls != 0 {
		t.Fatalf("内联图片不该触发渲染，实际 %d 次", f.sigCalls)
	}
}

// TestSigRef_NilRendererIs422 渲染服务不可用时报错，绝不静默退回预置图。
func TestSigRef_NilRendererIs422(t *testing.T) {
	r, _, _ := setup(t)
	mkRes(t, r, "signature", "sig_abcdef", "u_owner", "ma-shan-zheng", "张三")
	_, err := r.Inject([]byte(`{"sig_审核人":{"$sig":"sig_abcdef","with_timestamp":true}}`),
		Options{GranteeType: "user", GranteeID: "u_owner"})
	var pe *ErrParams
	if !errors.As(err, &pe) {
		t.Fatalf("无渲染器应 422，实际 %v", err)
	}
}
