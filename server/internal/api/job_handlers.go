package api

import (
	"encoding/json"
	"net/http"

	"docforge/server/internal/auth"
	"docforge/server/internal/models"

	"github.com/go-chi/chi/v5"
)

// handleListJobs 任务分页列表。
func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	q := s.DB.Model(&models.Job{}).Where("expired = ?", false)
	// 非管理员只看自己发起的任务。这一句和 handleJobDetail 里的 canSeeJob
	// 是同一把尺子，改一处必须改另一处。
	if !p.User.IsAdmin() {
		q = q.Where("created_by_user_id = ?", p.User.ID)
	}
	if st := r.URL.Query().Get("status"); st != "" {
		q = q.Where("status = ?", st)
	}
	if src := r.URL.Query().Get("source"); src != "" {
		q = q.Where("source = ?", src)
	}
	if cb := r.URL.Query().Get("callback_status"); cb != "" {
		q = q.Where("callback_status = ?", cb)
	}
	if er := r.URL.Query().Get("external_ref"); er != "" {
		q = q.Where("external_ref LIKE ?", "%"+er+"%")
	}
	var total int64
	q.Count(&total)
	pg := getPage(r)
	var jobs []models.Job
	q.Order("created_at DESC").Offset((pg.Page - 1) * pg.PageSize).Limit(pg.PageSize).Find(&jobs)

	// 批量带出模板名/编号，避免 N+1。
	tplIDs := make([]string, 0, len(jobs))
	for i := range jobs {
		tplIDs = append(tplIDs, jobs[i].TemplateID)
	}
	tplMap := map[string]models.Template{}
	if len(tplIDs) > 0 {
		var tpls []models.Template
		s.DB.Where("id IN ?", tplIDs).Find(&tpls)
		for i := range tpls {
			tplMap[tpls[i].ID] = tpls[i]
		}
	}
	items := make([]map[string]interface{}, 0, len(jobs))
	for i := range jobs {
		m := map[string]interface{}{}
		b, _ := json.Marshal(&jobs[i])
		_ = json.Unmarshal(b, &m)
		if t, ok := tplMap[jobs[i].TemplateID]; ok {
			m["template_name"] = t.Name
			m["template_code"] = t.Code
		}
		items = append(items, m)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"items": items, "total": total})
}

// handleJobDetail 任务详情。
func (s *Server) handleJobDetail(w http.ResponseWriter, r *http.Request) {
	var job models.Job
	if err := s.DB.First(&job, "id = ?", chi.URLParam(r, "id")).Error; err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "任务不存在", nil)
		return
	}
	// 不是发起人也不是管理员：按「不存在」回，不区分 403。
	// 403 等于确认这个 ID 真实存在，正好配合按日递增的 ID 做枚举。
	if p := auth.PrincipalFrom(r); !canSeeJob(p, &job) {
		writeErr(w, http.StatusNotFound, "not_found", "任务不存在", nil)
		return
	}
	var tpl models.Template
	s.DB.First(&tpl, "id = ?", job.TemplateID)
	var ver models.TemplateVersion
	s.DB.First(&ver, "id = ?", job.VersionID)
	var docs []models.Document
	s.DB.Where("job_id = ?", job.ID).Order("record_index").Find(&docs)
	docItems := make([]map[string]interface{}, 0, len(docs))
	for _, d := range docs {
		tok, _ := s.Signer.SignDocument(d.ID)
		signedURL := "/api/v1/s/" + tok
		primaryURL, pdfURL := docURLs(d)
		if d.DocxPath == "" && d.PdfPath != "" {
			signedURL += "?format=pdf"
		}
		item := map[string]interface{}{
			"document_id": d.ID, "record_index": d.RecordIndex, "filename": d.Filename,
			"status": d.Status, "error": d.Error,
			"download_url": primaryURL,
			"signed_url":   signedURL,
		}
		if pdfURL != "" {
			item["pdf_download_url"] = pdfURL
		}
		for k, v := range docEmailFields(&d) {
			item[k] = v
		}
		docItems = append(docItems, item)
	}
	prog := 0.0
	if job.Total > 0 {
		prog = float64(job.Succeeded+job.Failed) / float64(job.Total)
	}
	tok, _ := s.Signer.SignJob(job.ID)
	resp := map[string]interface{}{
		"id": job.ID, "status": job.Status, "source": job.Source,
		"template_id": job.TemplateID, "template_name": tpl.Name, "template_code": tpl.Code,
		"version": ver.Version, "format": job.Format, "filename_rule": job.FilenameRule,
		"total": job.Total, "succeeded": job.Succeeded, "failed": job.Failed, "progress": prog,
		"callback_url": job.CallbackURL, "callback_status": job.CallbackStatus,
		"callback_attempts": job.CallbackAttempts, "external_ref": job.ExternalRef,
		"zip_download_url": "/api/v1/jobs/" + job.ID + "/download",
		"signed_url":       "/api/v1/s/" + tok,
		"errors":    jobErrorList(job.ErrorDetails),
		"documents": docItems,
		"created_at": job.CreatedAt, "started_at": job.StartedAt,
		"finished_at": job.FinishedAt, "expires_at": job.ExpiresAt,
	}
	for k, v := range jobEmailFields(&job) {
		resp[k] = v
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleJobDownload 下载 zip（未终结 409）。
func (s *Server) handleJobDownload(w http.ResponseWriter, r *http.Request) {
	var job models.Job
	if err := s.DB.First(&job, "id = ?", chi.URLParam(r, "id")).Error; err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "任务不存在", nil)
		return
	}
	if p := auth.PrincipalFrom(r); !canSeeJob(p, &job) {
		writeErr(w, http.StatusNotFound, "not_found", "任务不存在", nil)
		return
	}
	if job.Status == "queued" || job.Status == "running" {
		writeErr(w, http.StatusConflict, "not_ready", "任务尚未结束，暂不可下载", nil)
		return
	}
	if job.ZipPath == "" {
		writeErr(w, http.StatusGone, "expired", "产物已过期或被清理", nil)
		return
	}
	// 流式发送：批量 zip 可能上百 MB，整包读进内存的峰值 = 并发数 × 包体积。
	if err := serveFileDownload(w, r, job.ZipPath, job.ID+".zip", "application/zip", nil); err != nil {
		writeErr(w, http.StatusGone, "expired", "产物文件不存在", nil)
		return
	}
}

// handleJobRetry 重试（only_failed 重置失败条目）。
func (s *Server) handleJobRetry(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFrom(r)
	var job models.Job
	if err := s.DB.First(&job, "id = ?", chi.URLParam(r, "id")).Error; err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "任务不存在", nil)
		return
	}
	// 重试会重新消耗渲染资源，等于替别人重跑一遍任务，同样按归属拦。
	if !canSeeJob(p, &job) {
		writeErr(w, http.StatusNotFound, "not_found", "任务不存在", nil)
		return
	}
	var body struct {
		OnlyFailed bool `json:"only_failed"`
	}
	_ = decodeJSON(r, &body)
	q := s.DB.Model(&models.Document{}).Where("job_id = ?", job.ID)
	if body.OnlyFailed {
		q = q.Where("status = ?", "failed")
	}
	q.Updates(map[string]interface{}{"status": "queued", "error": "", "path_err": "", "started_at": nil, "finished_at": nil})
	s.DB.Model(&job).Updates(map[string]interface{}{"status": "queued", "succeeded": 0, "failed": 0, "finished_at": nil})
	s.handleJobDetail(w, r)
}

func jobErrorList(b models.JSONB) interface{} {
	if len(b) == 0 {
		return []interface{}{}
	}
	var v interface{}
	_ = jsonUnmarshal(b, &v)
	return v
}
