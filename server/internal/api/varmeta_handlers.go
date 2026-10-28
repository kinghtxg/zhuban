package api

import (
	"encoding/json"
	"net/http"

	"docforge/server/internal/models"

	"github.com/go-chi/chi/v5"
)

// handleGetVarMeta GET /templates/{id}/variables/meta
// 合并 scan 推导的变量清单与用户维护的元数据：
//   - scan 出的变量：以 scan 的 name/kind/type 为准，叠加用户维护的 default/required/description；
//   - 用户维护但 scan 已不再出现的变量：保留并标记 status="removed"（下线，不删除）。
func (s *Server) handleGetVarMeta(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	tpl, ver, ok := s.loadTemplateAndCurrentVersion(w, r)
	if !ok {
		return
	}
	merged := s.mergeVarMeta(ver)
	_ = tpl
	writeJSON(w, http.StatusOK, map[string]interface{}{"variables": merged})
}

// handlePutVarMeta PUT /templates/{id}/variables/meta
// body {"variables":[{name,kind,type,required,default,description}]}
// 整体覆盖当前版本上用户维护的元数据（按 name 索引）；scan 推导结果不动。
func (s *Server) handlePutVarMeta(w http.ResponseWriter, r *http.Request) {
	p, tpl := s.loadEditableTemplate(w, r)
	if p == nil {
		return
	}
	var body struct {
		Variables []models.VariableMetaItem `json:"variables"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "data_invalid", "请求体不是合法 JSON", nil)
		return
	}
	ver, err := s.currentVersion(tpl)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "模板尚无当前版本", nil)
		return
	}
	store := map[string]models.VariableMetaItem{}
	for _, it := range body.Variables {
		if it.Name == "" {
			continue
		}
		store[it.Name] = it
	}
	b, _ := json.Marshal(store)
	if err := s.DB.Model(ver).Update("variable_meta", models.JSONB(b)).Error; err != nil {
		s.renderErr(w, err)
		return
	}
	ver.VariableMeta = models.JSONB(b)
	s.audit(p, "variables.meta_update", "template", tpl.ID, map[string]interface{}{"count": len(store)})
	writeJSON(w, http.StatusOK, map[string]interface{}{"variables": s.mergeVarMeta(ver)})
}

// loadTemplateAndCurrentVersion 取模板与当前版本，失败已写响应。
func (s *Server) loadTemplateAndCurrentVersion(w http.ResponseWriter, r *http.Request) (*models.Template, *models.TemplateVersion, bool) {
	var tpl models.Template
	if err := s.DB.First(&tpl, "id = ?", chi.URLParam(r, "id")).Error; err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "模板不存在", nil)
		return nil, nil, false
	}
	ver, err := s.currentVersion(&tpl)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "模板尚无当前版本", nil)
		return nil, nil, false
	}
	return &tpl, ver, true
}

// mergeVarMeta 把 scan 变量清单与用户维护元数据合并为输出列表。
func (s *Server) mergeVarMeta(ver *models.TemplateVersion) []map[string]interface{} {
	// scan 推导的变量清单。
	var scanVars []map[string]interface{}
	if len(ver.Variables) > 0 {
		_ = json.Unmarshal(ver.Variables, &scanVars)
	}
	// 用户维护的元数据。
	userMeta := map[string]models.VariableMetaItem{}
	if len(ver.VariableMeta) > 0 {
		_ = json.Unmarshal(ver.VariableMeta, &userMeta)
	}
	seen := map[string]bool{}
	out := make([]map[string]interface{}, 0, len(scanVars))
	for _, sv := range scanVars {
		name, _ := sv["name"].(string)
		if name == "" {
			continue
		}
		seen[name] = true
		item := map[string]interface{}{
			"name": name,
			"kind": sv["kind"],
			"type": sv["type"],
		}
		if req, ok := sv["required"].(bool); ok {
			item["required"] = req
		}
		if u, ok := userMeta[name]; ok {
			if u.Default != "" {
				item["default"] = u.Default
			}
			if u.Description != "" {
				item["description"] = u.Description
			}
			if u.Required != nil {
				item["required"] = *u.Required
			}
			if u.Kind != "" {
				item["kind"] = u.Kind
			}
			if u.Type != "" {
				item["type"] = u.Type
			}
		}
		item["status"] = "active"
		out = append(out, item)
	}
	// 用户维护但 scan 已删除的变量：保留并标记 removed。
	for name, u := range userMeta {
		if seen[name] {
			continue
		}
		out = append(out, map[string]interface{}{
			"name": name, "kind": u.Kind, "type": u.Type,
			"default": u.Default, "description": u.Description,
			"status": "removed",
		})
	}
	return out
}
