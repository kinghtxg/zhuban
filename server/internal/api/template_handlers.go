package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"docforge/server/internal/auth"
	"docforge/server/internal/models"
	"docforge/server/internal/render"
	"docforge/server/internal/store"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

var codeRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$`)

// handleCreateTemplate multipart 上传 docx 建模板（即调 render /scan）。
func (s *Server) handleCreateTemplate(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFrom(r)
	if p == nil || p.User == nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "未登录", nil)
		return
	}
	maxBytes := int64(s.Cfg.MaxTemplateMB) * 1024 * 1024
	if err := r.ParseMultipartForm(maxBytes); err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge, "payload_too_large", "模板文件过大", nil)
		return
	}
	f, _, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "data_invalid", "缺少模板文件 file", nil)
		return
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		s.renderErr(w, err)
		return
	}
	if int64(len(body)) > maxBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, "payload_too_large", "模板文件超过大小限制", nil)
		return
	}

	form := r.MultipartForm.Value
	get := func(k string) string { if v, ok := form[k]; ok && len(v) > 0 { return v[0] }; return "" }
	code := strings.ToLower(strings.TrimSpace(get("code")))
	name := strings.TrimSpace(get("name"))
	visibility := get("visibility")
	if visibility == "" {
		visibility = "private"
	}
	if !codeRe.MatchString(code) {
		writeErr(w, http.StatusUnprocessableEntity, "data_invalid", "code 须匹配 ^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$", []render.FieldError{{Path: "code", Message: "模板编号格式不正确"}})
		return
	}
	if name == "" {
		writeErr(w, http.StatusUnprocessableEntity, "data_invalid", "name 不能为空", []render.FieldError{{Path: "name", Message: "模板名称必填"}})
		return
	}
	var cnt int64
	s.DB.Model(&models.Template{}).Where("code = ?", code).Count(&cnt)
	if cnt > 0 {
		writeErr(w, http.StatusConflict, "conflict", "模板编号已存在", nil)
		return
	}

	// 调 render /scan
	sc, serr := s.Renderer.Scan(r.Context(), body)
	if serr != nil {
		s.renderErr(w, serr)
		return
	}
	// error 级诊断 → 不创建
	var hasErr bool
	for _, d := range sc.Diagnostics {
		if d.Severity == "error" {
			hasErr = true
		}
	}
	if hasErr {
		details := make([]render.FieldError, 0, len(sc.Diagnostics))
		for _, d := range sc.Diagnostics {
			details = append(details, render.FieldError{Path: d.Location, Message: d.Message})
		}
		writeErr(w, http.StatusUnprocessableEntity, "template_invalid", "模板存在语法错误，请先修复占位符", details)
		return
	}

	tpl := models.Template{
		ID: store.NewTplID(), Code: code, Name: name,
		Category: get("category"), Description: get("description"),
		Visibility: visibility, Enabled: true, CreatedBy: p.User.ID,
	}
	ver := models.TemplateVersion{
		ID: store.NewVerID(), TemplateID: tpl.ID, Version: "1.0.0",
		FileSize: int64(len(body)), UploadedBy: p.User.ID,
		Scanned: true,
	}
	path, err := s.Store.TemplateVersionPath(tpl.ID, ver.ID)
	if err != nil {
		s.renderErr(w, err)
		return
	}
	if err := s.Store.WriteFile(path, body, 0o644); err != nil {
		s.renderErr(w, err)
		return
	}
	ver.FilePath = path
	ver.Variables, ver.SampleJSON, ver.JSONSchema = marshalAny(sc.Variables), marshalAny(sc.SampleJSON), marshalAny(sc.JSONSchema)
	ver.Diagnostics = marshalAny(sc.Diagnostics)
	tpl.CurrentVersionID = ver.ID

	if err := s.DB.Create(&ver).Error; err != nil {
		s.renderErr(w, err)
		return
	}
	if err := s.DB.Create(&tpl).Error; err != nil {
		s.renderErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, s.templateDetail(&tpl))
}

// handleListTemplates 模板列表。
func (s *Server) handleListTemplates(w http.ResponseWriter, r *http.Request) {
	q := s.DB.Model(&models.Template{})
	if kw := r.URL.Query().Get("q"); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("name LIKE ? OR code LIKE ?", like, like)
	}
	if cat := r.URL.Query().Get("category"); cat != "" {
		q = q.Where("category = ?", cat)
	}
	if en := r.URL.Query().Get("enabled"); en != "" {
		if b, err := strconv.ParseBool(en); err == nil {
			q = q.Where("enabled = ?", b)
		}
	}
	var total int64
	q.Count(&total)
	pg := getPage(r)
	var tpls []models.Template
	q.Order("created_at DESC").Offset((pg.Page - 1) * pg.PageSize).Limit(pg.PageSize).Find(&tpls)
	items := make([]map[string]interface{}, 0, len(tpls))
	for i := range tpls {
		items = append(items, s.templateListItem(&tpls[i]))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"items": items, "total": total})
}

// handleTemplateDetail 模板详情。
func (s *Server) handleTemplateDetail(w http.ResponseWriter, r *http.Request) {
	var tpl models.Template
	if err := s.DB.First(&tpl, "id = ?", chi.URLParam(r, "id")).Error; err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "模板不存在", nil)
		return
	}
	writeJSON(w, http.StatusOK, s.templateDetail(&tpl))
}

// handleTemplateByCode 按 code 解析模板。
func (s *Server) handleTemplateByCode(w http.ResponseWriter, r *http.Request) {
	var tpl models.Template
	if err := s.DB.First(&tpl, "code = ?", chi.URLParam(r, "code")).Error; err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "模板不存在", nil)
		return
	}
	if !tpl.Enabled {
		writeErr(w, http.StatusLocked, "template_disabled", "模板已停用", nil)
		return
	}
	writeJSON(w, http.StatusOK, s.templateDetail(&tpl))
}

// handleAddVersion 上传新版本。
func (s *Server) handleAddVersion(w http.ResponseWriter, r *http.Request) {
	var tpl models.Template
	if err := s.DB.First(&tpl, "id = ?", chi.URLParam(r, "id")).Error; err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "模板不存在", nil)
		return
	}
	maxBytes := int64(s.Cfg.MaxTemplateMB) * 1024 * 1024
	if err := r.ParseMultipartForm(maxBytes); err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge, "payload_too_large", "模板文件过大", nil)
		return
	}
	f, _, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "data_invalid", "缺少模板文件 file", nil)
		return
	}
	defer f.Close()
	body, _ := io.ReadAll(io.LimitReader(f, maxBytes+1))

	sc, serr := s.Renderer.Scan(r.Context(), body)
	if serr != nil {
		s.renderErr(w, serr)
		return
	}
	// 上传人必须记成真正操作的人。这里原来写死 "system"，
	// 于是版本时间线上的每一版都署名 system —— 多个人共用一个模板时，
	// 「这一版是谁传的」这个问题在页面上永远没有答案。
	//
	// 用 API Key 调用时 Principal.User 是空的（只有 APIKey），
	// 此时留空让页面显示「未知」，而不是倒退成一个假的 "system"。
	uploader := ""
	if pr := auth.PrincipalFrom(r); pr != nil && pr.User != nil {
		uploader = pr.User.ID
	}
	ver := models.TemplateVersion{
		ID: store.NewVerID(), TemplateID: tpl.ID, Version: s.nextVersionNumber(tpl.ID),
		FileSize: int64(len(body)), UploadedBy: uploader, Scanned: true,
		// change_summary 是可选的：前端不填就留空，不要默认写「上传新版本」之类
		// 的空话——时间线上写满空话等于没有信息。
		ChangeSummary: strings.TrimSpace(r.FormValue("change_summary")),
		Variables:     marshalAny(sc.Variables), SampleJSON: marshalAny(sc.SampleJSON),
		JSONSchema: marshalAny(sc.JSONSchema), Diagnostics: marshalAny(sc.Diagnostics),
	}
	if p, err := s.Store.TemplateVersionPath(tpl.ID, ver.ID); err == nil {
		s.Store.WriteFile(p, body, 0o644)
		ver.FilePath = p
	}
	s.DB.Create(&ver)
	if act := r.FormValue("activate"); act == "" || act == "true" {
		s.DB.Model(&tpl).Update("current_version_id", ver.ID)
		tpl.CurrentVersionID = ver.ID
	}
	writeJSON(w, http.StatusCreated, s.templateDetail(&tpl))
}

// handleActivateVersion 激活指定版本。
func (s *Server) handleActivateVersion(w http.ResponseWriter, r *http.Request) {
	var tpl models.Template
	if err := s.DB.First(&tpl, "id = ?", chi.URLParam(r, "id")).Error; err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "模板不存在", nil)
		return
	}
	vid := chi.URLParam(r, "vid")
	if err := s.DB.First(&models.TemplateVersion{}, "id = ? AND template_id = ?", vid, tpl.ID).Error; err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "版本不存在", nil)
		return
	}
	s.DB.Model(&tpl).Update("current_version_id", vid)
	s.audit(auth.PrincipalFrom(r), "version.activate", "version", vid, map[string]interface{}{"template_id": tpl.ID, "code": tpl.Code})
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "current_version_id": vid})
}

// handleDeleteTemplate 删除模板（软删）。
//
// 几个刻意的选择：
//   - 软删而不是物理删：历史 job / document 还引用着 template_id，物理删会让
//     「生成记录」里的模板名变成空白，审计链也失去对象。软删后这些记录照样可查。
//   - 保留版本文件与 assets 文件：已经生成的文档不受影响；磁盘占位交给保留期清理。
//   - 释放 code：置为 deleted:<id>，原值存 OriginalCode。不释放的话，删掉模板后
//     重建同名会撞 code 唯一索引 —— 这是软删最常被忽略的坑。
//   - 有未终结任务时拒绝：否则 worker 领到任务去读版本文件，人已经以为模板没了。
func (s *Server) handleDeleteTemplate(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFrom(r)
	var tpl models.Template
	if err := s.DB.First(&tpl, "id = ?", chi.URLParam(r, "id")).Error; err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "模板不存在或已删除", nil)
		return
	}
	if !canDeleteTemplate(p, &tpl) {
		writeErr(w, http.StatusForbidden, "forbidden", "只有创建者或管理员可以删除模板", nil)
		return
	}

	var running int64
	s.DB.Model(&models.Job{}).
		Where("template_id = ? AND status IN ?", tpl.ID, []string{"queued", "running"}).
		Count(&running)
	if running > 0 {
		writeErr(w, http.StatusConflict, "job_running",
			fmt.Sprintf("该模板还有 %d 个未完成的生成任务，请等任务结束后再删除", running), nil)
		return
	}

	origCode := tpl.Code
	err := s.DB.Transaction(func(tx *gorm.DB) error {
		upd := map[string]interface{}{
			"code":          "deleted:" + tpl.ID,
			"original_code": origCode,
			"enabled":       false,
		}
		if err := tx.Model(&tpl).Updates(upd).Error; err != nil {
			return err
		}
		// 软删本身：gorm.DeletedAt 会自动写 deleted_at
		return tx.Delete(&models.Template{}, "id = ?", tpl.ID).Error
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", "删除模板失败："+err.Error(), nil)
		return
	}

	s.audit(p, "template.delete", "template", tpl.ID, map[string]interface{}{
		"name": tpl.Name, "code": origCode,
	})
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "id": tpl.ID, "code": origCode})
}

func (s *Server) handleDisableTemplate(w http.ResponseWriter, r *http.Request) {
	s.setTemplateEnabled(w, r, false)
}
func (s *Server) handleEnableTemplate(w http.ResponseWriter, r *http.Request) {
	s.setTemplateEnabled(w, r, true)
}
func (s *Server) setTemplateEnabled(w http.ResponseWriter, r *http.Request, en bool) {
	var tpl models.Template
	if err := s.DB.First(&tpl, "id = ?", chi.URLParam(r, "id")).Error; err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "模板不存在", nil)
		return
	}
	s.DB.Model(&tpl).Update("enabled", en)
	action := "template.disable"
	if en {
		action = "template.enable"
	}
	s.audit(auth.PrincipalFrom(r), action, "template", tpl.ID, map[string]interface{}{"name": tpl.Name, "code": tpl.Code})
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "enabled": en})
}

// handleThumbnail 缩略图代理（磁盘缓存）。缓存缺失时用版本示例数据调 render
// 生成首页 PNG；任何失败（如 render 未就绪、LibreOffice 不可用）均返回 204，
// 由前端展示占位图，缩略图失败不阻断列表（契约 §4.3）。
func (s *Server) handleThumbnail(w http.ResponseWriter, r *http.Request) {
	tplID := chi.URLParam(r, "id")
	var ver models.TemplateVersion
	if vid := r.URL.Query().Get("version_id"); vid != "" {
		if err := s.DB.First(&ver, "id = ? AND template_id = ?", vid, tplID).Error; err != nil {
			writeErr(w, http.StatusNotFound, "not_found", "模板版本不存在", nil)
			return
		}
	} else {
		var t models.Template
		if err := s.DB.First(&t, "id = ?", tplID).Error; err != nil {
			writeErr(w, http.StatusNotFound, "not_found", "模板不存在", nil)
			return
		}
		if t.CurrentVersionID == "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if err := s.DB.First(&ver, "id = ?", t.CurrentVersionID).Error; err != nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	cachePath, _ := s.Store.ThumbPath(tplID, ver.ID)
	if data, err := readFileSafe(cachePath); err == nil && len(data) > 0 {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write(data)
		return
	}
	// 缓存缺失：用示例数据渲染首页（max_pages=1），再取首页 PNG。
	docx, err := readFileSafe(ver.FilePath)
	if err != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	dataJSON := ver.SampleJSON
	if len(dataJSON) == 0 || string(dataJSON) == "null" {
		dataJSON = []byte("{}")
	}
	res, rerr := s.Renderer.Preview(r.Context(), docx, dataJSON, 1)
	if rerr != nil || len(res.Pages) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	png, perr := s.Renderer.PreviewPage(r.Context(), res.PreviewID, 1)
	if perr != nil || len(png) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if cp, err := s.Store.ThumbPath(tplID, ver.ID); err == nil {
		_ = s.Store.WriteFile(cp, png, 0o644) // 缓存失败不影响本次返回
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write(png)
}

// handleDownloadVersion 下载版本原 docx。
func (s *Server) handleDownloadVersion(w http.ResponseWriter, r *http.Request) {
	var v models.TemplateVersion
	if err := s.DB.First(&v, "id = ? AND template_id = ?", chi.URLParam(r, "vid"), chi.URLParam(r, "id")).Error; err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "版本不存在", nil)
		return
	}
	data, err := readFileSafe(v.FilePath)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "文件不存在或已过期", nil)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
	w.Header().Set("Content-Disposition", `attachment; filename="template.docx"`)
	_, _ = w.Write(data)
}

// ---- 序列化辅助 ----

func (s *Server) templateListItem(t *models.Template) map[string]interface{} {
	item := map[string]interface{}{
		"id": t.ID, "code": t.Code, "name": t.Name, "category": t.Category,
		"description": t.Description, "visibility": t.Visibility, "enabled": t.Enabled,
		"current_version_id": t.CurrentVersionID, "created_by": t.CreatedBy,
		"current_version": "", "thumbnail_url": "",
		"created_at": t.CreatedAt, "updated_at": t.UpdatedAt,
	}
	if t.CurrentVersionID != "" {
		var ver models.TemplateVersion
		if err := s.DB.Select("version").First(&ver, "id = ?", t.CurrentVersionID).Error; err == nil {
			item["current_version"] = map[string]interface{}{"version": ver.Version}
		}
		item["thumbnail_url"] = "/api/v1/templates/" + t.ID + "/thumbnail"
	}
	return item
}

func (s *Server) templateDetail(t *models.Template) map[string]interface{} {
	out := s.templateListItem(t)
	var cur models.TemplateVersion
	curMap := map[string]interface{}{}
	if t.CurrentVersionID != "" {
		if err := s.DB.First(&cur, "id = ?", t.CurrentVersionID).Error; err == nil {
			curMap = versionMap(&cur, true)
		}
	}
	var vers []models.TemplateVersion
	s.DB.Where("template_id = ?", t.ID).Order("created_at").Find(&vers)
	vs := make([]map[string]interface{}, 0, len(vers))
	for i := range vers {
		vs = append(vs, versionMap(&vers[i], vers[i].ID == t.CurrentVersionID))
	}
	out["current_version"] = curMap
	out["versions"] = vs
	out["integration"] = map[string]interface{}{"template_code": t.Code}
	return out
}

func versionMap(v *models.TemplateVersion, current bool) map[string]interface{} {
	source := v.Source
	if source == "" {
		source = "upload"
	}
	return map[string]interface{}{
		"id": v.ID, "version": v.Version, "is_current": current,
		"variables": jsonOrNil(v.Variables), "sample_json": jsonOrNil(v.SampleJSON),
		"json_schema": jsonOrNil(v.JSONSchema), "diagnostics": jsonOrNil(v.Diagnostics),
		"file_size": v.FileSize, "created_at": v.CreatedAt,
		"source": source, "change_summary": v.ChangeSummary, "created_by": v.UploadedBy,
	}
}

func marshalAny(v interface{}) models.JSONB {
	b, _ := json.Marshal(v)
	return models.JSONB(b)
}

func jsonOrNil(b models.JSONB) interface{} {
	if len(b) == 0 {
		return nil
	}
	var v interface{}
	if json.Unmarshal(b, &v) == nil {
		return v
	}
	return nil
}

func readFileSafe(p string) ([]byte, error) {
	if p == "" {
		return nil, fmt.Errorf("empty path")
	}
	return osReadFile(p)
}
