package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"docforge/server/internal/models"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

// handlePreview 工作台预览：透传 render，改写 pages[].url 为本服务代理路径。
func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TemplateID   string          `json:"template_id"`
		TemplateCode string          `json:"template_code"`
		VersionID    string          `json:"version_id"`
		Data         json.RawMessage `json:"data"`
		MaxPages     int             `json:"max_pages"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "data_invalid", "请求体不是合法 JSON", nil)
		return
	}
	ver, err := s.resolveVersion(body.TemplateID, body.TemplateCode, body.VersionID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "模板版本不存在", nil)
		return
	}
	docx, err := readFileSafe(ver.FilePath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", "模板文件不可用", nil)
		return
	}
	mp := body.MaxPages
	if mp <= 0 {
		mp = 6
	}
	res, rerr := s.Renderer.Preview(r.Context(), docx, body.Data, mp)
	if rerr != nil {
		s.renderErr(w, rerr)
		return
	}
	// 改写页面 url 为本服务代理。
	for i := range res.Pages {
		res.Pages[i].URL = "/api/v1/preview/" + res.PreviewID + "/page/" + strconv.Itoa(res.Pages[i].Page)
	}
	writeJSON(w, http.StatusOK, res)
}

// handlePreviewPage 代理渲染服务的单页 PNG。
func (s *Server) handlePreviewPage(w http.ResponseWriter, r *http.Request) {
	pv := chi.URLParam(r, "pv")
	n, _ := strconv.Atoi(chi.URLParam(r, "n"))
	data, err := s.Renderer.PreviewPage(r.Context(), pv, n)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "预览页不存在或已过期", nil)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(data)
}

// handleValidate 只校验不渲染。
func (s *Server) handleValidate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TemplateID   string          `json:"template_id"`
		TemplateCode string          `json:"template_code"`
		VersionID    string          `json:"version_id"`
		Data         json.RawMessage `json:"data"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "data_invalid", "请求体不是合法 JSON", nil)
		return
	}
	ver, err := s.resolveVersion(body.TemplateID, body.TemplateCode, body.VersionID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "模板版本不存在", nil)
		return
	}
	docx, err := readFileSafe(ver.FilePath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", "模板文件不可用", nil)
		return
	}
	res, rerr := s.Renderer.Validate(r.Context(), docx, body.Data)
	if rerr != nil {
		s.renderErr(w, rerr)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// resolveVersion 定位到具体模板版本。
//
// 三个入口（generate / render / preview / validate）必须认同同一套"引用哪个模板"的
// 说法。generate 与 render 一直同时接受 template_code 和 template_id，而 preview 与
// validate 原先只认 template_id —— 外部系统传 code 会拿到 404「模板版本不存在」，
// 报错还指向了错误的字段，排查成本极高。
// 所以这里统一支持两条路：code 是稳定的业务标识，id 是内部主键（重建环境就变）。
func (s *Server) resolveVersion(tplID, tplCode, verID string) (*models.TemplateVersion, error) {
	var ver models.TemplateVersion
	if verID != "" {
		if err := s.DB.First(&ver, "id = ?", verID).Error; err == nil {
			return &ver, nil
		}
	}
	var tpl models.Template
	q := s.DB
	if tplID != "" {
		q = q.Where("id = ?", tplID)
	} else if tplCode != "" {
		q = q.Where("code = ?", tplCode)
	} else {
		return nil, gorm.ErrRecordNotFound
	}
	if err := q.First(&tpl).Error; err != nil {
		return nil, err
	}
	if err := s.DB.First(&ver, "id = ?", tpl.CurrentVersionID).Error; err != nil {
		return nil, err
	}
	return &ver, nil
}
