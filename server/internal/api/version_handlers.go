package api

import (
	"net/http"

	"docforge/server/internal/models"
	"docforge/server/internal/store"

	"github.com/go-chi/chi/v5"
)

// handleListVersions GET /templates/{id}/versions
// 【改造】完整版本时间线：source / change_summary / created_by / 文件大小 / 是否激活。
func (s *Server) handleListVersions(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	var tpl models.Template
	if err := s.DB.First(&tpl, "id = ?", chi.URLParam(r, "id")).Error; err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "模板不存在", nil)
		return
	}
	var vers []models.TemplateVersion
	s.DB.Where("template_id = ?", tpl.ID).Order("created_at DESC").Find(&vers)
	items := make([]map[string]interface{}, 0, len(vers))
	for i := range vers {
		items = append(items, versionMap(&vers[i], vers[i].ID == tpl.CurrentVersionID))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"items": items})
}

// handleRollbackVersion POST /templates/{id}/versions/{vid}/rollback
// 把历史版本文件复制为新版本再激活（历史不丢、版本号递增）。
func (s *Server) handleRollbackVersion(w http.ResponseWriter, r *http.Request) {
	p, tpl := s.loadEditableTemplate(w, r)
	if p == nil {
		return
	}
	vid := chi.URLParam(r, "vid")
	var hist models.TemplateVersion
	if err := s.DB.First(&hist, "id = ? AND template_id = ?", vid, tpl.ID).Error; err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "历史版本不存在", nil)
		return
	}
	// 读取历史版本文件字节。
	data, err := readFileSafe(hist.FilePath)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "历史版本文件不可用", nil)
		return
	}
	ver, cerr := s.doRollback(tpl, &hist, data, p.User)
	if cerr != nil {
		s.renderErr(w, cerr)
		return
	}
	s.audit(p, "template.rollback", "version", ver.ID, map[string]interface{}{
		"template_id": tpl.ID, "code": tpl.Code,
		"rolled_from_version": hist.Version, "new_version": ver.Version,
	})
	writeJSON(w, http.StatusCreated, versionMap(ver, true))
}

// doRollback 把历史版本文件字节复制为新版本并激活（历史不丢、版本号递增）。
// 抽取为方法便于单测。
func (s *Server) doRollback(tpl *models.Template, hist *models.TemplateVersion, data []byte, user *models.User) (*models.TemplateVersion, error) {
	ver := models.TemplateVersion{
		ID:            store.NewVerID(),
		TemplateID:    tpl.ID,
		Version:       s.nextVersionNumber(tpl.ID),
		FileSize:      int64(len(data)),
		UploadedBy:    user.ID,
		Source:        "rollback",
		ChangeSummary: "回滚自版本 " + hist.Version,
		Scanned:       hist.Scanned,
		Variables:     hist.Variables, SampleJSON: hist.SampleJSON,
		JSONSchema: hist.JSONSchema, Diagnostics: hist.Diagnostics,
		VariableMeta: hist.VariableMeta,
	}
	path, werr := s.Store.TemplateVersionPath(tpl.ID, ver.ID)
	if werr != nil {
		return nil, werr
	}
	if werr := s.Store.WriteFile(path, data, 0o644); werr != nil {
		return nil, werr
	}
	ver.FilePath = path
	if cerr := s.DB.Create(&ver).Error; cerr != nil {
		return nil, cerr
	}
	s.DB.Model(tpl).Update("current_version_id", ver.ID)
	return &ver, nil
}
