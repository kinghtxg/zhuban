// Package sigauth —— 签章「引用参数对象」解析。
//
// 为什么需要它
// ------------
// 老形态只有两种值：资源 ID 字符串，或空串（拿变量名当 ID）。资源那一侧存的是
// **一张已经出好的图**，于是「签署时间」在资源创建那一刻就被烤进像素里了：
//
//   1. 同一张签名图用在三份文件上，三份印的签署时间是同一个（且是建资源那天）；
//   2. 想在合同上印「出具时间」而不是「制图时间」，只能每次重新建一个签名资源；
//   3. 换个字体就得重建资源，资源库很快变成一堆「张三-隶书-0925」。
//
// 所以新增一种值形态：带 `$sig` / `$seal` 标记的参数对象。它同时表达两件事——
//
//   {"sig_审核人": {
//       "$sig": "sig_ab12cd34",          // 用哪份资源的**授权与素材**
//       "font": "liu-jian-mao-cao",      // 出图参数（可覆盖资源自带的）
//       "signer_name": "张三",
//       "with_timestamp": true,
//       "timestamp": {"$date": "now", "format": "%Y-%m-%d %H:%M"},
//       "timestamp_prefix": "签署时间：",
//       "width_mm": 60
//   }}
//
// 授权校验仍然只认资源——`$sig` 就是被扣次数的那一条 grant，参数对象自己不能
// 凭空获得权限。这一点必须守住：否则传个 `font` 就能绕过授权自己画签。
//
// 时间为什么不在 Go 这边解析
// --------------------------
// `timestamp` 里的 `{"$date":...}` 原样转交给渲染服务，由它在**出图那一刻**求值。
// 理由和 dateexpr 模块的注释一样：Go 侧再实现一遍 strftime/时区，就会有第二份
// 「北京时间到底怎么算」的答案，两份迟早不一致。
package sigauth

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

const (
	markerSig  = "$sig"
	markerSeal = "$seal"
)

// 允许的字段。多一个就报错——静默忽略等于「用户以为生效了但没生效」。
var (
	sigRefKeys = map[string]bool{
		markerSig: true, markerSeal: true,
		"font": true, "signer_name": true,
		"with_timestamp": true, "timestamp": true, "timestamp_prefix": true,
		"width_mm": true, "height_mm": true,
	}
	sealRefKeys = map[string]bool{
		markerSeal: true,
		"unit_name": true, "seal_name": true, "shape": true, "radius_mm": true,
		"width_mm": true, "height_mm": true,
	}
)

// SigRef 是一条签章引用参数。ResourceID 必有；其余为零值即「不覆盖」。
type SigRef struct {
	ResourceID string

	// 签名出图参数
	Font       string
	SignerName string

	// 时间。两种形态分开存，因为目的地不同：
	//   StampExpr  {"$date":...} 的 JSON 原文 → 渲染服务 timestamp_expr，出图那刻求值
	//   StampText  ISO 8601 字面时间        → 渲染服务 timestamp，原样格式化
	StampExpr   string
	StampText   string
	WithStamp   bool
	StampPrefix string
	// 印章出图参数
	UnitName string
	SealName string
	Shape    string
	RadiusMM float64

	// 尺寸覆盖（两个类型都用）
	WidthMM  float64
	HeightMM float64
}

// needsRender 判断是否需要走渲染服务现场出图。
//
// 只有尺寸覆盖不算——那只是换摆放尺寸，读原图就够，没必要多一次出图
// （编辑器每次 apply 都会调到这里，能省则省）。
func (r *SigRef) needsRender() bool {
	if r == nil {
		return false
	}
	return r.Font != "" || r.SignerName != "" || r.WithStamp ||
		r.StampExpr != "" || r.StampText != "" ||
		r.UnitName != "" || r.SealName != "" || r.Shape != "" || r.RadiusMM > 0
}

// ErrParams 引用参数本身不合法（422），与「资源不可用」区分开：
// 前者是调用方写错了，后者是资源状态/授权问题。
type ErrParams struct {
	Name string // 变量名，用来告诉用户是哪一条写错了
	Msg  string
}

func (e *ErrParams) Error() string {
	if e.Name == "" {
		return e.Msg
	}
	return fmt.Sprintf("签章参数 %s：%s", e.Name, e.Msg)
}

func errParams(name, format string, args ...interface{}) error {
	return &ErrParams{Name: name, Msg: fmt.Sprintf(format, args...)}
}

// parseSigRef 尝试把 v 解析成引用参数对象。
//
// 返回 (nil, nil) 表示「v 不是引用参数对象」——可能是用户直接给的内联图片
// 对象（{"type":"image","src":...}），按既有行为原样保留、不校验也不扣次数。
// 判定依据是 `$sig`/`$seal` 标记：业务数据里几乎不会出现 `$` 开头的键。
func parseSigRef(v interface{}, name, resourceType string) (*SigRef, error) {
	m, ok := v.(map[string]interface{})
	if !ok {
		return nil, nil
	}
	_, hasSig := m[markerSig]
	_, hasSeal := m[markerSeal]
	if !hasSig && !hasSeal {
		return nil, nil
	}
	if hasSig && hasSeal {
		return nil, errParams(name, "不能同时带 %s 与 %s", markerSig, markerSeal)
	}

	marker := markerSig
	allowed := sigRefKeys
	if hasSeal {
		marker = markerSeal
		allowed = sealRefKeys
	}
	// 键名和标记必须对得上。`sig_xxx` 配 `$seal` 是复制粘贴事故，
	// 放过它会让「签名位」印出一个红章，而且查不出是哪一步错的。
	if want := map[string]string{"signature": markerSig, "seal": markerSeal}[resourceType]; want != marker {
		return nil, errParams(name, "键名前缀是 %s，但标记写的是 %s（%s 应配 %s）",
			resourceType, marker, resourceType, want)
	}

	for k := range m {
		if !allowed[k] {
			return nil, errParams(name, "含未知字段 %q（允许：%s）", k, joinKeys(allowed))
		}
	}

	ref := &SigRef{ResourceID: name}

	// 标记值：资源 ID 字符串（空串 → 用变量名当 ID）
	switch idv := m[marker].(type) {
	case nil:
	case string:
		if s := strings.TrimSpace(idv); s != "" {
			if !resourceIDRe.MatchString(s) {
				return nil, errParams(name, "%s 的值 %q 不是签章资源 ID（形如 sig_xxx / seal_xxx）", marker, idv)
			}
			ref.ResourceID = s
		}
	default:
		return nil, errParams(name, "%s 的值应为资源 ID 字符串", marker)
	}

	var err error
	if ref.Font, err = optStr(m, "font", name); err != nil {
		return nil, err
	}
	if ref.SignerName, err = optStr(m, "signer_name", name); err != nil {
		return nil, err
	}
	if ref.UnitName, err = optStr(m, "unit_name", name); err != nil {
		return nil, err
	}
	if ref.SealName, err = optStr(m, "seal_name", name); err != nil {
		return nil, err
	}
	if ref.Shape, err = optStr(m, "shape", name); err != nil {
		return nil, err
	}
	if ref.StampPrefix, err = optStr(m, "timestamp_prefix", name); err != nil {
		return nil, err
	}
	if ref.WithStamp, err = optBool(m, "with_timestamp", name); err != nil {
		return nil, err
	}
	if ref.WidthMM, err = optNum(m, "width_mm", name); err != nil {
		return nil, err
	}
	if ref.HeightMM, err = optNum(m, "height_mm", name); err != nil {
		return nil, err
	}
	if ref.RadiusMM, err = optNum(m, "radius_mm", name); err != nil {
		return nil, err
	}

	// timestamp：字符串（ISO 8601 字面时间）或日期表达式对象，两种都原样留存，
	// 由渲染服务统一归一。这里只做「必须是字符串或对象」这一层粗校验。
	if tv, ok := m["timestamp"]; ok {
		switch t := tv.(type) {
		case string:
			ref.StampText = strings.TrimSpace(t)
		case map[string]interface{}:
			b, merr := json.Marshal(t)
			if merr != nil {
				return nil, errParams(name, "timestamp 无法序列化：%v", merr)
			}
			ref.StampExpr = string(b)
		case nil:
		default:
			return nil, errParams(name, "timestamp 应为 ISO 8601 字符串或 {\"$date\":...} 表达式对象")
		}
		if ref.StampExpr != "" || ref.StampText != "" {
			// 给了时间就是要印出来，不必再要求重复声明 with_timestamp。
			ref.WithStamp = true
		}
	}

	if ref.RadiusMM < 0 || ref.WidthMM < 0 || ref.HeightMM < 0 {
		return nil, errParams(name, "尺寸/半径不能为负")
	}
	return ref, nil
}

// fields 组装渲染服务 /signature/render 或 /seal/render 的 multipart 字段。
//
// 资源自带的字体/署名只是**默认值**：参数对象里写了就覆盖。这条规则让「同一份
// 签名资源」既能当素材、又能当授权凭证，不必为换字体重新建资源。
func (r *SigRef) fields(resourceType string, meta resMeta, widthMM float64) (map[string]string, error) {
	if widthMM <= 0 {
		widthMM = 60
	}
	f := map[string]string{
		"width_mm": strconv.FormatFloat(widthMM, 'f', 2, 64),
	}
	if r.StampExpr != "" {
		f["timestamp_expr"] = r.StampExpr
	} else if r.StampText != "" {
		f["timestamp"] = r.StampText
	}
	if r.StampPrefix != "" {
		f["timestamp_prefix"] = r.StampPrefix
	}

	if resourceType == "seal" {
		if r.WithStamp {
			return nil, &ErrParams{Name: r.ResourceID, Msg: "印章不支持带时间（$seal 不接受 with_timestamp / timestamp）"}
		}
		unit := firstNonEmpty(r.UnitName, meta.unitName)
		sealName := firstNonEmpty(r.SealName, meta.sealName)
		if unit == "" && sealName == "" {
			return nil, &ErrParams{Name: r.ResourceID, Msg: "该印章没有记录单位名/印章名，现场出图需在参数里给 unit_name 或 seal_name"}
		}
		f["unit_name"] = unit
		f["seal_name"] = sealName
		if s := firstNonEmpty(r.Shape, meta.shape); s != "" {
			f["shape"] = s
		}
		radius := r.RadiusMM
		if radius <= 0 {
			// 出图半径跟文档里占位宽度对齐，否则图插进去还得靠 fit 缩，
			// 边缘小字会被缩得看不清。
			radius = widthMM / 2
		}
		f["radius_mm"] = strconv.FormatFloat(radius, 'f', 2, 64)
		return f, nil
	}

	font := firstNonEmpty(r.Font, meta.fontID)
	name := firstNonEmpty(r.SignerName, meta.signerName)
	if font == "" {
		return nil, &ErrParams{Name: r.ResourceID, Msg: "该签名没有记录字体，现场出图需在参数里给 font"}
	}
	if name == "" {
		return nil, &ErrParams{Name: r.ResourceID, Msg: "该签名没有记录署名，现场出图需在参数里给 signer_name"}
	}
	f["font_id"] = font
	f["signer_name"] = name
	if r.WithStamp {
		f["with_timestamp"] = "true"
	}
	return f, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func joinKeys(m map[string]bool) string {	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// 稳定输出，便于测试断言与用户对读
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return strings.Join(out, "、")
}

func optStr(m map[string]interface{}, key, name string) (string, error) {
	v, ok := m[key]
	if !ok || v == nil {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", errParams(name, "%s 应为字符串", key)
	}
	return strings.TrimSpace(s), nil
}

func optBool(m map[string]interface{}, key, name string) (bool, error) {
	v, ok := m[key]
	if !ok || v == nil {
		return false, nil
	}
	b, ok := v.(bool)
	if !ok {
		return false, errParams(name, "%s 应为 true/false", key)
	}
	return b, nil
}

func optNum(m map[string]interface{}, key, name string) (float64, error) {
	v, ok := m[key]
	if !ok || v == nil {
		return 0, nil
	}
	f, ok := v.(float64)
	if !ok {
		return 0, errParams(name, "%s 应为数字", key)
	}
	return f, nil
}
