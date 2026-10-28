package api

import (
	"encoding/json"
	"net/http"

	"docforge/server/internal/auth"
	"docforge/server/internal/models"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

// handleListDocuments 文档生成分页列表（每条文档行聚合所属 job/template 字段，
// 以满足前端「文档生成中心」对来源、模板/版本、批次进度等列的展示，契约 §4.8）。
func (s *Server) handleListDocuments(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	q := s.DB.Model(&models.Document{}).
		Joins("JOIN jobs ON jobs.id = documents.job_id").
		Where("documents.expired = ? AND jobs.expired = ?", false, false)
	// 非管理员只看自己发起的任务产出的文档（与 handleListJobs 同一口径）。
	if !p.User.IsAdmin() {
		q = q.Where("jobs.created_by_user_id = ?", p.User.ID)
	}
	if st := r.URL.Query().Get("status"); st != "" {
		q = q.Where("documents.status = ?", st)
	}
	if src := r.URL.Query().Get("source"); src != "" {
		q = q.Where("jobs.source = ?", src)
	}
	if tpl := r.URL.Query().Get("template_id"); tpl != "" {
		q = q.Where("jobs.template_id = ?", tpl)
	}
	if kw := r.URL.Query().Get("q"); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("documents.filename LIKE ?", like)
	}
	if from := r.URL.Query().Get("from"); from != "" {
		q = q.Where("documents.created_at >= ?", from)
	}
	if to := r.URL.Query().Get("to"); to != "" {
		q = q.Where("documents.created_at <= ?", to)
	}
	var total int64
	q.Count(&total)
	pg := getPage(r)
	var docs []models.Document
	q.Order("documents.created_at DESC").Offset((pg.Page - 1) * pg.PageSize).Limit(pg.PageSize).Find(&docs)

	// 批量加载相关 job / template / version，避免 N+1。
	jobIDs := map[string]bool{}
	for i := range docs {
		jobIDs[docs[i].JobID] = true
	}
	jobMap := map[string]models.Job{}
	tplIDs := map[string]bool{}
	verIDs := map[string]bool{}
	if len(jobIDs) > 0 {
		ids := make([]string, 0, len(jobIDs))
		for id := range jobIDs {
			ids = append(ids, id)
		}
		var jobs []models.Job
		s.DB.Where("id IN ?", ids).Find(&jobs)
		for _, j := range jobs {
			jobMap[j.ID] = j
			tplIDs[j.TemplateID] = true
			verIDs[j.VersionID] = true
		}
	}
	tplMap := map[string]models.Template{}
	if len(tplIDs) > 0 {
		ids := make([]string, 0, len(tplIDs))
		for id := range tplIDs {
			ids = append(ids, id)
		}
		var tpls []models.Template
		s.DB.Where("id IN ?", ids).Find(&tpls)
		for _, t := range tpls {
			tplMap[t.ID] = t
		}
	}
	verMap := map[string]models.TemplateVersion{}
	if len(verIDs) > 0 {
		ids := make([]string, 0, len(verIDs))
		for id := range verIDs {
			if id != "" {
				ids = append(ids, id)
			}
		}
		if len(ids) > 0 {
			var vers []models.TemplateVersion
			s.DB.Where("id IN ?", ids).Find(&vers)
			for _, v := range vers {
				verMap[v.ID] = v
			}
		}
	}

	items := make([]map[string]interface{}, 0, len(docs))
	for i := range docs {
		d := docs[i]
		job := jobMap[d.JobID]
		row := map[string]interface{}{
			"id": d.ID, "job_id": d.JobID, "record_index": d.RecordIndex,
			"filename": d.Filename, "status": d.Status, "error": d.Error,
			"duration_ms": d.DurationMS, "download_count": d.DownloadCount,
			"created_at": d.CreatedAt, "started_at": d.StartedAt, "finished_at": d.FinishedAt,
			"source": job.Source, "template_id": job.TemplateID, "version_id": job.VersionID,
			"total": job.Total, "succeeded": job.Succeeded, "failed": job.Failed,
			"format": job.Format, "external_ref": job.ExternalRef,
			"expires_at": job.ExpiresAt,
		}
		if t, ok := tplMap[job.TemplateID]; ok {
			row["template_name"] = t.Name
			row["template_code"] = t.Code
		}
		if v, ok := verMap[job.VersionID]; ok {
			row["version"] = v.Version
		}
		row["operator"] = s.operatorName(&job)
		items = append(items, row)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"items": items, "total": total})
}

// handleDocumentDetail 文档详情。
func (s *Server) handleDocumentDetail(w http.ResponseWriter, r *http.Request) {
	var d models.Document
	if err := s.DB.First(&d, "id = ?", chi.URLParam(r, "id")).Error; err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "文档不存在", nil)
		return
	}
	var job models.Job
	s.DB.First(&job, "id = ?", d.JobID)
	if p := auth.PrincipalFrom(r); !canSeeDocument(p, &d, &job) {
		writeErr(w, http.StatusNotFound, "not_found", "文档不存在", nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"id": d.ID, "job_id": d.JobID, "record_index": d.RecordIndex, "filename": d.Filename,
		"status": d.Status, "error": d.Error, "duration_ms": d.DurationMS,
		"download_count": d.DownloadCount, "input_data": jsonOrNil(d.InputData),
		"operator": s.operatorName(&job), "expires_at": job.ExpiresAt, "created_at": d.CreatedAt,
	})
}

// handleDocumentDownload 下载文档（计数、过期 410）。
func (s *Server) handleDocumentDownload(w http.ResponseWriter, r *http.Request) {
	var d models.Document
	if err := s.DB.First(&d, "id = ?", chi.URLParam(r, "id")).Error; err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "文档不存在", nil)
		return
	}
	var owner models.Job
	s.DB.First(&owner, "id = ?", d.JobID)
	if p := auth.PrincipalFrom(r); !canSeeDocument(p, &d, &owner) {
		writeErr(w, http.StatusNotFound, "not_found", "文档不存在", nil)
		return
	}
	if d.Expired {
		writeErr(w, http.StatusGone, "expired", "文档已过期", nil)
		return
	}
	format := r.URL.Query().Get("format")
	path := d.DocxPath
	ct := "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	if format == "pdf" {
		path = d.PdfPath
		ct = "application/pdf"
	}
	// 区分「没生成这种格式」和「生成过但文件没了」：前者是调用方式问题（客户端该换
	// 另一个链接，或按 job.format 判断），后者才是真的过期。混成一句 expired 会让人
	// 以为产物丢了 —— 排查方向直接跑偏。
	if d.DocxPath == "" && d.PdfPath == "" {
		writeErr(w, http.StatusGone, "expired", "文件不存在或已清理", nil)
		return
	}
	if path == "" {
		msg := "该文档未生成 PDF，请改用 docx 下载链接"
		if format != "pdf" {
			msg = "该文档未生成 docx，请改用 pdf 下载链接"
		}
		writeErr(w, http.StatusNotFound, "format_not_available", msg, nil)
		return
	}
	// 流式发送：这里的「几百 KB」是常见值而不是上限——模板里贴几十张高清图，
	// 一份 docx 就能到几十 MB。同一条路径如果一半走读全量、一半走流式，
	// 下次审查还得重新判断一遍，所以统一。
	//
	// 下载计数改成原子自增：d.DownloadCount+1 是读-改-写，两次并发下载会读到同一个 n
	// 再都写 n+1，少计一次。计数本身不影响业务，但「明明下了 10 次只显示 9」一定会被当 bug 报上来。
	err := serveFileDownload(w, r, path, d.Filename, ct, func() {
		s.DB.Model(&models.Document{}).Where("id = ?", d.ID).
			UpdateColumn("download_count", gorm.Expr("download_count + 1"))
	})
	if err != nil {
		writeErr(w, http.StatusGone, "expired", "文件不存在或已清理", nil)
		return
	}
}

// handleDocumentRegenerate 用原数据再生成。
func (s *Server) handleDocumentRegenerate(w http.ResponseWriter, r *http.Request) {
	var d models.Document
	if err := s.DB.First(&d, "id = ?", chi.URLParam(r, "id")).Error; err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "文档不存在", nil)
		return
	}
	body := genBody{
		TemplateID: "", VersionID: "",
		Data: json.RawMessage(d.InputData), Format: "docx", Source: "ui",
	}
	var job models.Job
	s.DB.First(&job, "id = ?", d.JobID)
	if p := auth.PrincipalFrom(r); !canSeeDocument(p, &d, &job) {
		writeErr(w, http.StatusNotFound, "not_found", "文档不存在", nil)
		return
	}
	body.TemplateID = job.TemplateID
	body.VersionID = job.VersionID
	s.runGenerate(w, r, auth.PrincipalFrom(r), &body)
}

// handleSignedURL 签发直链。
func (s *Server) handleSignedURL(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DocumentID string `json:"document_id"`
		JobID      string `json:"job_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "data_invalid", "请求体不是合法 JSON", nil)
		return
	}
	var tok string
	var exp interface{}
	var pdfOnly bool
	if body.DocumentID != "" {
		var doc models.Document
		if err := s.DB.First(&doc, "id = ?", body.DocumentID).Error; err != nil {
			writeErr(w, http.StatusNotFound, "not_found", "文档不存在", nil)
			return
		}
		// 直链签发是最容易被忽略的一处越权：签出来的 token 本身不带身份，
		// 谁拿到都能下载。所以签发这一刻必须确认申请人是产物主人，
		// 否则等于「不用登录也能拿别人的文件」多了一条快捷路径。
		var owner models.Job
		s.DB.First(&owner, "id = ?", doc.JobID)
		if p := auth.PrincipalFrom(r); !canSeeDocument(p, &doc, &owner) {
			writeErr(w, http.StatusNotFound, "not_found", "文档不存在", nil)
			return
		}
		tok, exp = s.Signer.SignDocument(body.DocumentID)
		pdfOnly = doc.DocxPath == "" && doc.PdfPath != ""
	} else if body.JobID != "" {
		var job models.Job
		if err := s.DB.First(&job, "id = ?", body.JobID).Error; err != nil {
			writeErr(w, http.StatusNotFound, "not_found", "任务不存在", nil)
			return
		}
		if p := auth.PrincipalFrom(r); !canSeeJob(p, &job) {
			writeErr(w, http.StatusNotFound, "not_found", "任务不存在", nil)
			return
		}
		tok, exp = s.Signer.SignJob(body.JobID)
	} else {
		writeErr(w, http.StatusUnprocessableEntity, "data_invalid", "必须提供 document_id 或 job_id", nil)
		return
	}
	signedURL := "/api/v1/s/" + tok
	if pdfOnly {
		signedURL += "?format=pdf"
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"signed_url": signedURL, "expires_at": exp})
}

func (s *Server) operatorName(job *models.Job) string {
	if job.APIKeyID != "" {
		var ak models.APIKey
		if err := s.DB.First(&ak, "id = ?", job.APIKeyID).Error; err == nil {
			return ak.Name
		}
	}
	var u models.User
	if err := s.DB.First(&u, "id = ?", job.CreatedByUserID).Error; err == nil {
		return u.DisplayName
	}
	return "system"
}
