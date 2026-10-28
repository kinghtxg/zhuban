// Package assetimg 负责把模板资源库中的图片绑定到 docx 占位变量：
// 渲染时在 data 里注入 {type:"image", src:"data:...", width, height} 对象，
// 由 render-service richdata.resolve_context 转成 docxtpl InlineImage。
//
// 关键链路（联调修正）：
//   - parse 输出的图片占位是 {type:"placeholder", kind:"image", name:"..."}，
//     docapply 对 placeholder 只回写 {{name}} 标签、不消费 src；
//   - 因此图片必须走渲染 data 注入，而不是往 model 块里写 src。
package assetimg

import (
	"encoding/base64"
	"encoding/json"
	"os"

	"docforge/server/internal/models"

	"gorm.io/gorm"
)

// Binding 描述一次编辑中，某个图片占位变量绑定到了哪个资源。
type Binding struct {
	AssetID string
	Name    string
}

// Resolver 读模板资源并生成渲染 data 覆盖。
type Resolver struct {
	DB *gorm.DB
}

// ScanBindings 递归遍历 model.blocks（顶层 + table 非 loop_row 单元格的 blocks），
// 收集 {type:"placeholder", kind:"image"} 且带 asset_id（兼容 ref）的绑定。
// 表格循环行（loop_row）内的图片变量不在 V1 范围，跳过。
func ScanBindings(modelJSON []byte) []Binding {
	var doc map[string]interface{}
	if err := json.Unmarshal(modelJSON, &doc); err != nil {
		return nil
	}
	blocks, _ := doc["blocks"].([]interface{})
	var out []Binding
	scanBlocks(blocks, &out)
	return out
}

func scanBlocks(blocks []interface{}, out *[]Binding) {
	for _, b := range blocks {
		bm, ok := b.(map[string]interface{})
		if !ok {
			continue
		}
		switch bm["type"] {
		case "placeholder":
			if bm["kind"] != "image" {
				continue
			}
			aid, _ := bm["asset_id"].(string)
			if aid == "" {
				aid, _ = bm["ref"].(string)
			}
			name, _ := bm["name"].(string)
			if aid == "" || name == "" {
				continue
			}
			*out = append(*out, Binding{AssetID: aid, Name: name})
		case "table":
			rows, _ := bm["rows"].([]interface{})
			for _, r := range rows {
				rm, _ := r.(map[string]interface{})
				if lr, _ := rm["loop_row"].(bool); lr {
					continue // 表格循环内 item.xxx 图片变量不处理（V1 边界）
				}
				cells, _ := rm["cells"].([]interface{})
				for _, c := range cells {
					cm, _ := c.(map[string]interface{})
					inner, _ := cm["blocks"].([]interface{})
					scanBlocks(inner, out)
				}
			}
		}
	}
}

// BuildOverride 读资源文件，生成 richdata.resolve_context 识别的图片对象。
// width/height 单位 mm（float），与 richdata.py 读取字段一致。
func (r *Resolver) BuildOverride(asset *models.TemplateAsset) (map[string]interface{}, error) {
	data, err := os.ReadFile(asset.FilePath)
	if err != nil {
		return nil, err
	}
	mime := asset.MIME
	if mime == "" {
		mime = "image/png"
	}
	obj := map[string]interface{}{
		"type": "image",
		"src":  "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data),
	}
	if asset.WidthMM > 0 {
		obj["width"] = asset.WidthMM
	}
	if asset.HeightMM > 0 {
		obj["height"] = asset.HeightMM
	}
	return obj, nil
}

// OverridesFromBindings 把本次 model 的绑定关系解析为 name→图片对象覆盖。
// 资源缺失/读失败的绑定被跳过（不影响其它占位）。
func (r *Resolver) OverridesFromBindings(tplID string, bs []Binding) map[string]interface{} {
	out := map[string]interface{}{}
	for _, b := range bs {
		var asset models.TemplateAsset
		if err := r.DB.First(&asset, "id = ? AND template_id = ?", b.AssetID, tplID).Error; err != nil {
			continue
		}
		obj, err := r.BuildOverride(&asset)
		if err != nil {
			continue
		}
		out[b.Name] = obj
	}
	return out
}

// MergeInto 把 overrides 合并进 dataJSON；同名 key 已存在则保留用户值（用户数据优先）。
// dataJSON 非法或不是对象时退化为 {"overrides":...}。
func MergeInto(dataJSON []byte, overrides map[string]interface{}) []byte {
	if len(overrides) == 0 {
		return dataJSON
	}
	var data map[string]interface{}
	if len(dataJSON) == 0 || string(dataJSON) == "null" {
		data = map[string]interface{}{}
	} else if err := json.Unmarshal(dataJSON, &data); err != nil {
		data = map[string]interface{}{}
	}
	if data == nil {
		data = map[string]interface{}{}
	}
	for k, v := range overrides {
		if _, exists := data[k]; !exists {
			data[k] = v
		}
	}
	b, _ := json.Marshal(data)
	return b
}

// MergeDefaults 读取模板所有 bind_var 非空的资源，作为默认图片注入 data（用户数据优先）。
// 供 worker / 同步直渲路径在调 Renderer.Render 前统一调用。
func (r *Resolver) MergeDefaults(tplID string, dataJSON []byte) []byte {
	var assets []models.TemplateAsset
	r.DB.Where("template_id = ? AND bind_var <> ?", tplID, "").Find(&assets)
	overrides := map[string]interface{}{}
	for i := range assets {
		obj, err := r.BuildOverride(&assets[i])
		if err != nil {
			continue
		}
		overrides[assets[i].BindVar] = obj
	}
	return MergeInto(dataJSON, overrides)
}

// PersistBindings 把本次 model 的绑定关系写入各资源的 bind_var：
// 本次绑定的写入占位 name；该模板下本次未被绑定的资源 bind_var 置空。
func (r *Resolver) PersistBindings(tplID string, bs []Binding) {
	// 先清空该模板所有资源的 bind_var。
	r.DB.Model(&models.TemplateAsset{}).Where("template_id = ?", tplID).
		Update("bind_var", "")
	seen := map[string]string{} // asset_id -> name
	for _, b := range bs {
		seen[b.AssetID] = b.Name
	}
	for aid, name := range seen {
		r.DB.Model(&models.TemplateAsset{}).
			Where("id = ? AND template_id = ?", aid, tplID).
			Update("bind_var", name)
	}
}
