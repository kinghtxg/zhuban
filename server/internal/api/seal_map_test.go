package api

import (
	"testing"

	"docforge/server/internal/models"
)

// 这一组守的是「印章详情能不能显示出真实的几何参数」。
//
// 背景：前端类型里一直声明着 Seal.radius_mm 与 Seal.star，但 sealMap 从来没返回过。
// 结果是详情页「半径」永远是「—」、「五角星」永远显示「有」——包括用户明确关掉星的那些。
// 数据其实在库里，只是没人往外读。

func TestSealMapDerivesRadiusFromWidth(t *testing.T) {
	// 创建时后端把表单里的 radius_mm 换算成 width_mm = radius*2，
	// 半径本身不单独存。所以列表/详情要靠宽度反推。
	seal := &models.Seal{ID: "seal_a", WidthMM: 40, Shape: "circle"}
	m := sealMap(seal)

	if got := m["radius_mm"]; got != 20.0 {
		t.Errorf("宽度 40 应推出半径 20，实际 %v", got)
	}
}

func TestSealMapOmitsRadiusWhenWidthUnset(t *testing.T) {
	// 宽度为 0 的异常数据不要推出一个 0 半径，那会显示成「0 mm」，
	// 比显示「—」更像真的。
	m := sealMap(&models.Seal{ID: "seal_b", WidthMM: 0})
	if _, ok := m["radius_mm"]; ok {
		t.Errorf("宽度缺失时不应给半径，实际 %v", m["radius_mm"])
	}
}

func TestSealMapStarFromParams(t *testing.T) {
	cases := []struct {
		name   string
		params string
		want   bool
	}{
		{"params 有 star=false", `{"star":"false"}`, false},
		{"params 有 star=0", `{"star":"0"}`, false},
		{"params 有 star=true", `{"star":"true"}`, true},
		{"params 有布尔 false", `{"star":false}`, false},
		{"params 没有 star（存量数据）", `{"unit_name":"A","shape":"circle"}`, true},
		{"params 为空", ``, true},
		{"params 不是合法 JSON", `not-json`, true},
	}
	for _, c := range cases {
		seal := &models.Seal{ID: "seal_c", Params: models.JSONB(c.params)}
		got, _ := sealMap(seal)["star"].(bool)
		if got != c.want {
			t.Errorf("%s：期望 star=%v，实际 %v", c.name, c.want, got)
		}
	}
}

func TestSealMapKeepsCoreFields(t *testing.T) {
	// 顺手守一下别把原有字段改坏：这一组是本轮动过 sealMap 之后加的。
	seal := &models.Seal{
		ID: "seal_d", Name: "示例（公章）", OwnerID: "u1", Source: "generated",
		UnitName: "示例信息", SealName: "公章", Shape: "circle", WidthMM: 40, Status: "approved",
	}
	m := sealMap(seal)
	for _, k := range []string{"id", "name", "owner_id", "source", "unit_name", "seal_name",
		"shape", "width_mm", "height_mm", "status", "created_at", "url"} {
		if _, ok := m[k]; !ok {
			t.Errorf("sealMap 缺少字段 %s", k)
		}
	}
	if m["url"] != "/api/v1/seals/seal_d/file" {
		t.Errorf("url 拼接不对：%v", m["url"])
	}
}
