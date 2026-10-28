package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"docforge/server/internal/assetimg"
	"docforge/server/internal/auth"
	"docforge/server/internal/models"
	"docforge/server/internal/render"
	"docforge/server/internal/sigauth"
	"docforge/server/internal/store"

	"github.com/go-chi/chi/v5"
)

// parseCache 缓存 /parse 结果，按 version_id 命中（版本 docx 不可变，缓存天然失效）。
type parseCache struct {
	mu sync.Mutex
	m  map[string]*render.ParseResult
}

func newParseCache() *parseCache { return &parseCache{m: map[string]*render.ParseResult{}} }

func (c *parseCache) get(k string) (*render.ParseResult, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[k]
	return v, ok
}

func (c *parseCache) set(k string, v *render.ParseResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// 简单容量上限：超过 64 条清空重建（单实例、低并发，足够）。
	if len(c.m) > 64 {
		c.m = map[string]*render.ParseResult{}
	}
	c.m[k] = v
}

// editorModelCache 是 Server 级的 /parse 缓存。
var editorModelCache = newParseCache()

// loadEditableTemplate 取模板并校验编辑权限；失败已写响应。
func (s *Server) loadEditableTemplate(w http.ResponseWriter, r *http.Request) (*auth.Principal, *models.Template) {
	p := s.requireUser(w, r)
	if p == nil {
		return nil, nil
	}
	var tpl models.Template
	if err := s.DB.First(&tpl, "id = ?", chi.URLParam(r, "id")).Error; err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "模板不存在", nil)
		return nil, nil
	}
	if !canEditTemplate(p, &tpl) {
		writeErr(w, http.StatusForbidden, "forbidden", "仅模板创建者、public 模板成员或管理员可编辑", nil)
		return nil, nil
	}
	return p, &tpl
}

// currentVersion 取模板当前激活版本。
func (s *Server) currentVersion(tpl *models.Template) (*models.TemplateVersion, error) {
	var ver models.TemplateVersion
	if err := s.DB.First(&ver, "id = ?", tpl.CurrentVersionID).Error; err != nil {
		return nil, err
	}
	return &ver, nil
}

// nextVersionNumber 取该模板历史版本号中最大的补丁号 +1，不依赖行数。
// 版本号形如 X.Y.Z；统一在 1.0.x 下递增。历史有跳号时取最大 Z，避免冲突。
func (s *Server) nextVersionNumber(tplID string) string {
	var vers []models.TemplateVersion
	s.DB.Where("template_id = ?", tplID).Select("version").Find(&vers)
	maxZ := -1
	for _, v := range vers {
		var major, minor, patch int
		n, _ := fmt.Sscanf(v.Version, "%d.%d.%d", &major, &minor, &patch)
		if n >= 3 {
			if patch > maxZ {
				maxZ = patch
			}
			continue
		}
		// 兼容 X.Y 两字段形式。
		var maj, min int
		if n2, _ := fmt.Sscanf(v.Version, "%d.%d", &maj, &min); n2 == 2 {
			if min > maxZ {
				maxZ = min
			}
		}
	}
	if maxZ < 0 {
		maxZ = 0
	}
	return fmt.Sprintf("1.0.%d", maxZ+1)
}

// handleEditorModel GET /templates/{id}/editor/model
// 取当前激活版本 docx 调 render /parse，按 version_id 缓存。
func (s *Server) handleEditorModel(w http.ResponseWriter, r *http.Request) {
	p, tpl := s.loadEditableTemplate(w, r)
	if p == nil {
		return
	}
	ver, err := s.currentVersion(tpl)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "模板尚无当前版本", nil)
		return
	}
	if cached, ok := editorModelCache.get(ver.ID); ok {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"blocks": rawJSON(cached.Blocks), "warnings": cached.Warnings,
			"page":       rawJSON(cached.Page),
			"version_id": ver.ID, "version_no": ver.Version,
		})
		return
	}
	docx, err := readFileSafe(ver.FilePath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", "模板文件不可用", nil)
		return
	}
	res, rerr := s.Renderer.ParseModel(r.Context(), docx)
	if rerr != nil {
		s.renderErr(w, rerr)
		return
	}
	editorModelCache.set(ver.ID, res)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"blocks":     rawJSON(res.Blocks),
		"warnings":   res.Warnings,
		"page":       rawJSON(res.Page),
		"version_id": ver.ID, "version_no": ver.Version,
	})
}

// rawJSON 把原始 JSON 字节解码为通用值（透传 blocks）。
func rawJSON(b json.RawMessage) interface{} {
	if len(b) == 0 {
		return nil
	}
	var v interface{}
	if json.Unmarshal(b, &v) == nil {
		return v
	}
	return nil
}

// readEditorModelForm 解析 apply/save 的 multipart 表单。
func readEditorModelForm(r *http.Request) (modelJSON []byte, baseVerID, summary string, err error) {
	maxBytes := int64(64 << 20)
	if perr := r.ParseMultipartForm(maxBytes); perr != nil {
		return nil, "", "", perr
	}
	form := r.MultipartForm.Value
	get := func(k string) string {
		if v, ok := form[k]; ok && len(v) > 0 {
			return v[0]
		}
		return ""
	}
	modelStr := get("model")
	if modelStr == "" {
		return nil, "", "", fmt.Errorf("缺少 model 字段")
	}
	// 校验 model 是合法 JSON。
	var probe interface{}
	if jerr := json.Unmarshal([]byte(modelStr), &probe); jerr != nil {
		return nil, "", "", fmt.Errorf("model 不是合法 JSON: %w", jerr)
	}
	return []byte(modelStr), get("base_version_id"), get("change_summary"), nil
}

// imgResolver 返回模板图片资源解析器（仅依赖 DB）。
func (s *Server) imgResolver() *assetimg.Resolver {
	return &assetimg.Resolver{DB: s.DB}
}

// editorOverrides 从本次 model 提取图片占位绑定，生成 name→图片对象覆盖。
// 实时预览 / export pdf 把这些覆盖合并进渲染 data（用户数据优先）。
func (s *Server) editorOverrides(tplID string, modelJSON []byte) map[string]interface{} {
	return s.imgResolver().OverridesFromBindings(tplID, assetimg.ScanBindings(modelJSON))
}

// renderPreviewForEditor 用给定 docx + 模板示例数据调 render /preview，改写页面 URL。
func (s *Server) renderPreviewForEditor(r *http.Request, docx []byte, sampleJSON []byte) (*render.PreviewResult, time.Time, error) {
	data := sampleJSON
	if len(data) == 0 || string(data) == "null" {
		data = []byte("{}")
	}
	res, err := s.Renderer.Preview(r.Context(), docx, data, 6)
	if err != nil {
		return nil, time.Time{}, err
	}
	for i := range res.Pages {
		res.Pages[i].URL = "/api/v1/preview/" + res.PreviewID + "/page/" + itoa(res.Pages[i].Page)
	}
	return res, time.Now().Add(30 * time.Minute), nil
}

// injectSigAuth 对编辑器渲染 data 做 fail-closed 签章校验与注入。
// dryRun=true（apply 预览）：两道关卡校验但不扣次数；false（export 实际产出）：原子扣减。
// 返回新 data；ErrStatus/ErrParams → 422，ErrDenied → 403 grant_denied。
func (s *Server) injectSigAuth(ctx context.Context, p *auth.Principal, data []byte, dryRun bool) ([]byte, error) {
	opts := sigauth.Options{
		GranteeType: "user", GranteeID: p.User.ID,
		IsAdmin: p.User.IsAdmin(), DryRun: dryRun,
		Ctx: ctx,
	}
	return (&sigauth.Resolver{DB: s.DB, Renderer: s.Renderer}).Inject(data, opts)
}

// sigErrToHTTP 把 sigauth 错误转成 HTTP 响应。
func sigErrToHTTP(w http.ResponseWriter, err error) bool {
	switch e := err.(type) {
	case *sigauth.ErrStatus:
		writeErr(w, http.StatusUnprocessableEntity, "signature_unavailable", e.Error(), nil)
		return true
	case *sigauth.ErrParams:
		writeErr(w, http.StatusUnprocessableEntity, "data_invalid", e.Error(), nil)
		return true
	case *sigauth.ErrDenied:
		writeErr(w, http.StatusForbidden, "grant_denied",
			"无权使用签章："+e.Reason+"（"+e.Name+"）", nil)
		return true
	}
	return false
}

// handleEditorApply POST /templates/{id}/editor/apply
// 回写 docx（不落库），随后用示例数据跑预览。
func (s *Server) handleEditorApply(w http.ResponseWriter, r *http.Request) {
	p, tpl := s.loadEditableTemplate(w, r)
	if p == nil {
		return
	}
	modelJSON, baseVerID, _, err := readEditorModelForm(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "data_invalid", err.Error(), nil)
		return
	}
	// 以 base_version_id（若提供且存在）否则当前版本 docx 为底。
	baseVer, verr := s.resolveBaseVersion(tpl, baseVerID)
	if verr != nil {
		writeErr(w, http.StatusNotFound, "not_found", baseVerID+" 对应的版本不存在", nil)
		return
	}
	baseDocx, rerr := readFileSafe(baseVer.FilePath)
	if rerr != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", "模板文件不可用", nil)
		return
	}
	newDocx, aerr := s.Renderer.ApplyModel(r.Context(), baseDocx, modelJSON)
	if aerr != nil {
		s.renderErr(w, aerr)
		return
	}
	// 预览用当前激活版本的示例数据，并合并本次 model 的图片资源覆盖（用户数据优先）。
	cur, _ := s.currentVersion(tpl)
	var sample models.JSONB
	if cur != nil {
		sample = cur.SampleJSON
	}
	data := assetimg.MergeInto(sample, s.editorOverrides(tpl.ID, modelJSON))
	// 把编辑器新插入的 sig_/seal_ 占位名以空值合并进 data（用户已提供图片对象则保留）。
	data = sigauth.InjectPlaceholders(data, sigauth.ScanPlaceholders(modelJSON))
	// 签章两道关卡（预览 DryRun：校验不扣次数）。
	data, serr := s.injectSigAuth(r.Context(), p, data, true)
	if serr != nil {
		if sigErrToHTTP(w, serr) {
			return
		}
		s.renderErr(w, serr)
		return
	}
	prev, expires, perr := s.renderPreviewForEditor(r, newDocx, data)
	if perr != nil {
		s.renderErr(w, perr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"preview_id": prev.PreviewID,
		"pages":      prev.Pages,
		"expires_at": expires,
	})
}

// handleEditorSave POST /templates/{id}/editor/save
// 带 base_version_id 乐观锁落新版本，自动 scan 后激活。
func (s *Server) handleEditorSave(w http.ResponseWriter, r *http.Request) {
	p, tpl := s.loadEditableTemplate(w, r)
	if p == nil {
		return
	}
	modelJSON, baseVerID, summary, err := readEditorModelForm(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "data_invalid", err.Error(), nil)
		return
	}
	if baseVerID == "" {
		writeErr(w, http.StatusBadRequest, "data_invalid", "缺少 base_version_id", nil)
		return
	}
	// 乐观锁：base_version_id 必须等于当前激活版本。
	cur, cerr := s.currentVersion(tpl)
	if cerr != nil {
		writeErr(w, http.StatusNotFound, "not_found", "模板尚无当前版本", nil)
		return
	}
	if baseVerID != cur.ID {
		writeJSON(w, http.StatusConflict, map[string]interface{}{
			"error": map[string]interface{}{
				"code":    "version_conflict",
				"message": "该模板已被更新，请刷新后重试",
				"details": []render.FieldError{},
			},
			"current_version_id": cur.ID, "current_version": cur.Version,
		})
		return
	}
	baseDocx, rerr := readFileSafe(cur.FilePath)
	if rerr != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", "模板文件不可用", nil)
		return
	}
	newDocx, aerr := s.Renderer.ApplyModel(r.Context(), baseDocx, modelJSON)
	if aerr != nil {
		s.renderErr(w, aerr)
		return
	}
	// 持久化图片占位绑定到资源 bind_var（保存后正常渲染/投递也能出图）。
	s.imgResolver().PersistBindings(tpl.ID, assetimg.ScanBindings(modelJSON))
	// 自动 scan 刷新变量/诊断。
	sc, serr := s.Renderer.Scan(r.Context(), newDocx)
	if serr != nil {
		s.renderErr(w, serr)
		return
	}
	ver := models.TemplateVersion{
		ID:           store.NewVerID(),
		TemplateID:   tpl.ID,
		Version:      s.nextVersionNumber(tpl.ID),
		FileSize:     int64(len(newDocx)),
		UploadedBy:   p.User.ID,
		Source:       "online_edit",
		ChangeSummary: summary,
		Scanned:      true,
		Variables:    marshalAny(sc.Variables), SampleJSON: marshalAny(sc.SampleJSON),
		JSONSchema: marshalAny(sc.JSONSchema), Diagnostics: marshalAny(sc.Diagnostics),
		// 继承当前版本的变量元数据。
		VariableMeta: cur.VariableMeta,
	}
	path, werr := s.Store.TemplateVersionPath(tpl.ID, ver.ID)
	if werr != nil {
		s.renderErr(w, werr)
		return
	}
	if werr := s.Store.WriteFile(path, newDocx, 0o644); werr != nil {
		s.renderErr(w, werr)
		return
	}
	ver.FilePath = path
	if cerr := s.DB.Create(&ver).Error; cerr != nil {
		s.renderErr(w, cerr)
		return
	}
	s.DB.Model(tpl).Update("current_version_id", ver.ID)
	s.audit(p, "template.online_save", "version", ver.ID, map[string]interface{}{
		"template_id": tpl.ID, "code": tpl.Code, "version": ver.Version,
		"change_summary": summary,
	})
	writeJSON(w, http.StatusCreated, versionMap(&ver, true))
}

// resolveBaseVersion 按 base_version_id 解析底版；为空则返回当前版本。
func (s *Server) resolveBaseVersion(tpl *models.Template, baseVerID string) (*models.TemplateVersion, error) {
	if baseVerID != "" {
		var v models.TemplateVersion
		if err := s.DB.First(&v, "id = ? AND template_id = ?", baseVerID, tpl.ID).Error; err == nil {
			return &v, nil
		}
	}
	return s.currentVersion(tpl)
}

// handleEditorExport POST /templates/{id}/editor/export
// 一键导出当前编辑草稿（不落版本、不入库）。docx 直接回传；pdf 走 /render format=pdf。
func (s *Server) handleEditorExport(w http.ResponseWriter, r *http.Request) {
	p, tpl := s.loadEditableTemplate(w, r)
	if p == nil {
		return
	}
	if perr := r.ParseMultipartForm(int64(64 << 20)); perr != nil {
		writeErr(w, http.StatusBadRequest, "data_invalid", "无法解析 multipart 表单", nil)
		return
	}
	form := r.MultipartForm.Value
	get := func(k string) string {
		if v, ok := form[k]; ok && len(v) > 0 {
			return v[0]
		}
		return ""
	}
	modelStr := get("model")
	if modelStr == "" {
		writeErr(w, http.StatusUnprocessableEntity, "data_invalid", "缺少 model 字段", nil)
		return
	}
	var probe interface{}
	if jerr := json.Unmarshal([]byte(modelStr), &probe); jerr != nil {
		writeErr(w, http.StatusUnprocessableEntity, "data_invalid", "model 不是合法 JSON: "+jerr.Error(), nil)
		return
	}
	modelJSON := []byte(modelStr)

	format := get("format")
	if format == "" {
		format = "docx"
	}
	if format != "docx" && format != "pdf" {
		writeErr(w, http.StatusBadRequest, "data_invalid", "format 仅支持 docx 或 pdf", nil)
		return
	}

	// 底版：当前激活版本 docx。
	cur, cerr := s.currentVersion(tpl)
	if cerr != nil {
		writeErr(w, http.StatusNotFound, "not_found", "模板尚无当前版本", nil)
		return
	}
	baseDocx, rerr := readFileSafe(cur.FilePath)
	if rerr != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", "模板文件不可用", nil)
		return
	}
	newDocx, aerr := s.Renderer.ApplyModel(r.Context(), baseDocx, modelJSON)
	if aerr != nil {
		s.renderErr(w, aerr)
		return
	}

	// 文件名：code 优先，其次 name；RFC 5987 编码（兼容中文）。
	base := tpl.Code
	if base == "" {
		base = tpl.Name
	}
	if base == "" {
		base = "template"
	}
	dispBase := url.PathEscape(base)

	if format == "docx" {
		w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
		w.Header().Set("Content-Disposition",
			`attachment; filename="`+dispBase+`-draft.docx"; filename*=UTF-8''`+dispBase+`-draft.docx`)
		_, _ = w.Write(newDocx)
		return
	}

	// pdf：用该 docx 走 /render format=pdf；data 缺省取当前版本 sample_json。
	dataStr := get("data")
	if dataStr == "" {
		dataStr = string(cur.SampleJSON)
	}
	if dataStr == "" || dataStr == "null" {
		dataStr = "{}"
	}
	// 合并本次 model 的图片资源覆盖（用户数据优先）。
	merged := assetimg.MergeInto([]byte(dataStr), s.editorOverrides(tpl.ID, modelJSON))
	// 把本次 model 新插入的 sig_/seal_ 占位名以空值合并进 data。
	merged = sigauth.InjectPlaceholders(merged, sigauth.ScanPlaceholders(modelJSON))
	// 签章授权正式扣减（export 是实际产出）。
	merged, serr := s.injectSigAuth(r.Context(), p, merged, false)
	if serr != nil {
		if sigErrToHTTP(w, serr) {
			return
		}
		s.renderErr(w, serr)
		return
	}
	pdf, _, rerr2 := s.Renderer.Render(r.Context(), newDocx, merged, "pdf")
	if rerr2 != nil {
		s.renderErr(w, rerr2)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition",
		`attachment; filename="`+dispBase+`-draft.pdf"; filename*=UTF-8''`+dispBase+`-draft.pdf`)
	_, _ = w.Write(pdf)
}
