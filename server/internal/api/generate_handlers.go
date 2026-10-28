package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"docforge/server/internal/assetimg"
	"docforge/server/internal/mailer"
	"docforge/server/internal/auth"
	"docforge/server/internal/models"
	"docforge/server/internal/render"
	"docforge/server/internal/sigauth"
	"docforge/server/internal/store"
	"docforge/server/internal/worker"
)

type genBody struct {
	TemplateCode string                 `json:"template_code"`
	TemplateID   string                 `json:"template_id"`
	Version      string                 `json:"version"`
	VersionID    string                 `json:"version_id"`
	Data         json.RawMessage        `json:"data"`
	Records      []json.RawMessage      `json:"records"`
	Format       string                 `json:"format"`
	Filename     string                 `json:"filename"`
	CallbackURL  string                 `json:"callback_url"`
	ExternalRef  string                 `json:"external_ref"`
	Sync         *bool                  `json:"sync"`
	Source       string                 `json:"source"`
	// Email 整任务发一封；Emails 每条记录各发一封（长度须与 records 一致）。
	// 两者可同时给：先按记录逐封发，再发一封整包汇总。
	Email  *mailer.EmailSpec  `json:"email"`
	Emails []mailer.EmailSpec `json:"emails"`
}

// handleGenerate 投递即生成。
func (s *Server) handleGenerate(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFrom(r)
	var body genBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "data_invalid", "请求体不是合法 JSON", nil)
		return
	}
	body.Source = "auto_api"
	s.runGenerate(w, r, p, &body)
}

// handleRender UI 渲染。
func (s *Server) handleRender(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFrom(r)
	if p == nil || p.User == nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "未登录", nil)
		return
	}
	var body genBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "data_invalid", "请求体不是合法 JSON", nil)
		return
	}
	body.Source = "ui"
	s.runGenerate(w, r, p, &body)
}

func (s *Server) runGenerate(w http.ResponseWriter, r *http.Request, p *auth.Principal, body *genBody) {
	// 1) 解析模板
	var tpl models.Template
	var err error
	if body.TemplateCode != "" {
		if err = s.DB.First(&tpl, "code = ?", body.TemplateCode).Error; err != nil {
			writeErr(w, http.StatusNotFound, "not_found", "模板编号不存在："+body.TemplateCode, nil)
			return
		}
	} else if body.TemplateID != "" {
		if err = s.DB.First(&tpl, "id = ?", body.TemplateID).Error; err != nil {
			writeErr(w, http.StatusNotFound, "not_found", "模板不存在", nil)
			return
		}
	} else {
		writeErr(w, http.StatusUnprocessableEntity, "data_invalid", "必须提供 template_code 或 template_id", nil)
		return
	}
	if !tpl.Enabled {
		writeErr(w, http.StatusLocked, "template_disabled", "模板已停用，无法新生成", nil)
		return
	}

	// 2) 解析版本
	var ver models.TemplateVersion
	if body.VersionID != "" {
		err = s.DB.First(&ver, "id = ? AND template_id = ?", body.VersionID, tpl.ID).Error
	} else if body.Version != "" {
		err = s.DB.First(&ver, "version = ? AND template_id = ?", body.Version, tpl.ID).Error
	} else {
		err = s.DB.First(&ver, "id = ?", tpl.CurrentVersionID).Error
	}
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "指定模板版本不存在", nil)
		return
	}

	// 3) records
	records, totalBytes, aerr := buildRecords(body)
	if aerr != nil {
		writeErr(w, aerr.Status, aerr.Code, aerr.Message, aerr.Details)
		return
	}
	if len(records) > s.Cfg.MaxRecords {
		writeErr(w, http.StatusRequestEntityTooLarge, "payload_too_large",
			fmt.Sprintf("单次任务最多 %d 条记录", s.Cfg.MaxRecords), nil)
		return
	}
	if totalBytes > int64(s.Cfg.MaxJSONMB)*1024*1024 {
		writeErr(w, http.StatusRequestEntityTooLarge, "payload_too_large",
			fmt.Sprintf("JSON 总大小超过 %dMB", s.Cfg.MaxJSONMB), nil)
		return
	}
	format := body.Format
	if format == "" || format == "both" && len(records) > 1 {
		if format == "" {
			format = "docx"
		}
	}
	if format == "" {
		format = "docx"
	}

	// 4) 幂等
	if body.ExternalRef != "" {
		existing := s.findIdempotent(p, tpl.Code, body.ExternalRef)
		if existing != nil {
			writeJSON(w, http.StatusOK, s.asyncResponse(existing, false))
			return
		}
	}

	// 5) 校验（逐记录调 render /validate）
	verDocx, rerr := readFileSafe(ver.FilePath)
	if rerr != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", "模板文件不可用", nil)
		return
	}
	if ver.Scanned {
		for i, rec := range records {
			vr, verr := s.Renderer.Validate(r.Context(), verDocx, rec)
			if verr != nil {
				s.renderErr(w, verr)
				return
			}
			if !vr.Valid {
				details := vr.Errors
				writeErr(w, http.StatusUnprocessableEntity, "data_invalid",
					fmt.Sprintf("第 %d 条数据校验未通过", i+1), details)
				return
			}
		}
	}

	// 5.5) 签章授权预检（DryRun：只校验不扣次数；worker 真正渲染时原子扣减）。
	granteeType, granteeID, isAdmin := grantCaller(p)
	sigResolver := &sigauth.Resolver{DB: s.DB, Renderer: s.Renderer}
	sigNames := sigauth.VarsSigNames(ver.Variables)
	for _, rec := range records {
		d := (&assetimg.Resolver{DB: s.DB}).MergeDefaults(tpl.ID, rec)
		d = sigauth.InjectPlaceholders(d, sigNames)
		if _, serr := sigResolver.Inject(d, sigauth.Options{
			GranteeType: granteeType, GranteeID: granteeID, IsAdmin: isAdmin, DryRun: true,
			Ctx: r.Context(),
		}); serr != nil {
			if sigErrToHTTP(w, serr) {
				return
			}
			s.renderErr(w, serr)
			return
		}
	}

	// 6) source 修正
	source := body.Source
	switch source {
	case "ui":
	case "batch":
	default:
		if len(records) > 1 {
			source = "batch"
		} else if body.Sync != nil && *body.Sync {
			source = "sync_api"
		} else {
			source = "auto_api"
		}
	}

	// 5.6) 邮件预检：在任务真正排进去之前先把「发不出去」拦下来。
	//
	// 为什么不等任务跑完再说：跑完才发现 SMTP 没配或域名不在白名单，
	// 用户看到的是「文档生成成功了但没发出去」，而他已经把下载链接当结果用了。
	// 提前 422 让他改完参数重投，比事后补救清楚得多。
	emailSpec, perRecord, eerr := s.checkEmails(body, records)
	if eerr != nil {
		writeErr(w, eerr.Status, eerr.Code, eerr.Message, eerr.Details)
		return
	}

	// 7) 建 job + documents
	job, err := s.createJob(p, &tpl, &ver, source, body, records, format, emailSpec, perRecord)
	if err != nil {
		s.renderErr(w, err)
		return
	}

	// 8) 同步判定
	syncMode := false
	if len(records) == 1 && (totalBytes <= 1<<20) {
		syncMode = true
	}
	if body.Sync != nil {
		syncMode = *body.Sync && len(records) == 1
	}
	if source == "ui" {
		syncMode = syncMode || len(records) == 1
	}

	if !syncMode {
		writeJSON(w, http.StatusAccepted, s.asyncResponse(job, false))
		return
	}

	// 9) 同步等待最多 30s
	final := s.waitJob(job.ID, 30*time.Second)
	if final.Status == "running" || final.Status == "queued" {
		writeJSON(w, http.StatusAccepted, s.asyncResponse(final, false))
		return
	}
	if final.Failed > 0 && final.Succeeded == 0 {
		var docs []models.Document
		s.DB.Where("job_id = ?", final.ID).Find(&docs)
		details := []render.FieldError{}
		for _, d := range docs {
			details = append(details, render.FieldError{Path: d.PathErr, Message: d.Error})
		}
		writeErr(w, http.StatusUnprocessableEntity, "data_invalid", "渲染失败", details)
		return
	}
	writeJSON(w, http.StatusOK, s.syncResponse(final))
}

func buildRecords(body *genBody) ([][]byte, int64, *apiErr) {
	var records [][]byte
	var total int64
	if len(body.Records) > 0 {
		for _, rec := range body.Records {
			var v interface{}
			if json.Unmarshal(rec, &v) != nil {
				return nil, 0, &apiErr{http.StatusUnprocessableEntity, "data_invalid", "records 含非法 JSON", nil}
			}
			records = append(records, rec)
			total += int64(len(rec))
		}
	} else if len(body.Data) > 0 {
		var v interface{}
		if json.Unmarshal(body.Data, &v) != nil {
			return nil, 0, &apiErr{http.StatusUnprocessableEntity, "data_invalid", "data 不是合法 JSON", nil}
		}
		records = append(records, body.Data)
		total += int64(len(body.Data))
	} else {
		return nil, 0, &apiErr{http.StatusUnprocessableEntity, "data_invalid", "必须提供 data 或 records", nil}
	}
	return records, total, nil
}

func (s *Server) findIdempotent(p *auth.Principal, code, ref string) *models.Job {
	q := s.DB.Model(&models.Job{}).Where("external_ref = ? AND status <> ?", ref, "failed")
	if p.APIKey != nil {
		q = q.Where("api_key_id = ?", p.APIKey.ID)
	} else {
		q = q.Where("created_by_user_id = ? AND api_key_id = ?", p.User.ID, "")
	}
	var job models.Job
	if err := q.Order("created_at DESC").First(&job).Error; err != nil {
		return nil
	}
	return &job
}

func (s *Server) createJob(p *auth.Principal, tpl *models.Template, ver *models.TemplateVersion, source string, body *genBody, records [][]byte, format string, emailSpec *mailer.EmailSpec, perRecord []*mailer.EmailSpec) (*models.Job, error) {
	jobID, err := store.NextJobID(s.DB)
	if err != nil {
		return nil, err
	}
	expires := time.Now().AddDate(0, 0, s.Cfg.RetentionDays)
	job := models.Job{
		ID: jobID, TemplateID: tpl.ID, VersionID: ver.ID, Source: source,
		ExternalRef: body.ExternalRef, CallbackURL: body.CallbackURL,
		CallbackStatus: "", Status: "queued", Total: len(records),
		Format: format, FilenameRule: body.Filename, ExpiresAt: &expires,
	}
	if emailSpec != nil {
		if b, merr := json.Marshal(emailSpec); merr == nil {
			job.EmailSpec = models.JSONB(b)
			job.EmailStatus = "pending"
		}
	}
	if p.APIKey != nil {
		job.APIKeyID = p.APIKey.ID
	} else if p.User != nil {
		job.CreatedByUserID = p.User.ID
	}
	if err := s.DB.Create(&job).Error; err != nil {
		return nil, err
	}
	for i, rec := range records {
		docID, _ := store.NextDocumentID(s.DB)
		filename := worker.BuildFilename(body.Filename, decodeMap(rec), i, tpl.Code, format)
		d := models.Document{
			ID: docID, JobID: job.ID, RecordIndex: i, Filename: filename,
			Status: "queued", InputData: rec,
		}
		if i < len(perRecord) && perRecord[i] != nil {
			if b, merr := json.Marshal(perRecord[i]); merr == nil {
				d.EmailSpec = models.JSONB(b)
				d.EmailStatus = "pending"
			}
		}
		s.DB.Create(&d)
	}
	return &job, nil
}

func (s *Server) waitJob(jobID string, timeout time.Duration) *models.Job {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var job models.Job
		if err := s.DB.First(&job, "id = ?", jobID).Error; err == nil {
			if job.Status == "succeeded" || job.Status == "partial" || job.Status == "failed" {
				return &job
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	var job models.Job
	s.DB.First(&job, "id = ?", jobID)
	return &job
}

// asyncResponse 组装契约 §4.4 异步响应。
func (s *Server) asyncResponse(job *models.Job, _ bool) map[string]interface{} {
	tok, _ := s.Signer.SignJob(job.ID)
	resp := map[string]interface{}{
		"job_id": job.ID, "status": job.Status, "sync": false,
		"template_code": s.templateCode(job.TemplateID), "external_ref": job.ExternalRef,
		"status_url":   "/api/v1/jobs/" + job.ID,
		"download_url": "/api/v1/jobs/" + job.ID + "/download",
		"signed_url":   "/api/v1/s/" + tok,
	}
	for k, v := range jobEmailFields(job) {
		resp[k] = v
	}
	return resp
}

// syncResponse 组装同步成功响应。
func (s *Server) syncResponse(job *models.Job) map[string]interface{} {
	var docs []models.Document
	s.DB.Where("job_id = ?", job.ID).Order("record_index").Find(&docs)
	items := make([]map[string]interface{}, 0, len(docs))
	for _, d := range docs {
		if d.Status != "succeeded" {
			continue
		}
		tok, _ := s.Signer.SignDocument(d.ID)
		signedURL := "/api/v1/s/" + tok
		primaryURL, pdfURL := docURLs(d)
		if d.DocxPath == "" && d.PdfPath != "" {
			// 仅 PDF 文档：签名直链默认走 docx 会 410，必须显式指定 format=pdf
			signedURL += "?format=pdf"
		}
		item := map[string]interface{}{
			"document_id": d.ID, "filename": d.Filename,
			"download_url": primaryURL,
			"signed_url":   signedURL,
		}
		if pdfURL != "" {
			item["pdf_download_url"] = pdfURL
		}
		for k, v := range docEmailFields(&d) {
			item[k] = v
		}
		items = append(items, item)
	}
	resp := map[string]interface{}{
		"job_id": job.ID, "status": job.Status, "sync": true, "documents": items,
	}
	for k, v := range jobEmailFields(job) {
		resp[k] = v
	}
	return resp
}

// jobEmailFields 任务级邮件状态。没带邮件投递的任务不输出这几个键——
// 契约里凭空多出 email_status: "" 只会让调用方写分支去猜。
func jobEmailFields(job *models.Job) map[string]interface{} {
	if job.EmailStatus == "" {
		return nil
	}
	return map[string]interface{}{
		"email_status":   job.EmailStatus,
		"email_error":    job.EmailError,
		"email_attempts": job.EmailAttempts,
	}
}

func docEmailFields(d *models.Document) map[string]interface{} {
	if d.EmailStatus == "" {
		return nil
	}
	return map[string]interface{}{
		"email_status":   d.EmailStatus,
		"email_error":    d.EmailError,
		"email_attempts": d.EmailAttempts,
	}
}

func (s *Server) templateCode(tplID string) string {
	var t models.Template
	if err := s.DB.First(&t, "id = ?", tplID).Error; err == nil {
		return t.Code
	}
	return ""
}

func decodeMap(b []byte) map[string]interface{} {
	var m map[string]interface{}
	_ = json.Unmarshal(b, &m)
	return m
}

// grantCaller 从 principal 解析签章授权的被授权人类型/ID与是否 admin。
func grantCaller(p *auth.Principal) (string, string, bool) {
	if p == nil {
		return "", "", false
	}
	if p.APIKey != nil {
		return "api_key", p.APIKey.ID, false
	}
	if p.User != nil {
		return "user", p.User.ID, p.User.IsAdmin()
	}
	return "", "", false
}

var _ = strings.TrimSpace

// checkEmails 校验并归一化邮件参数。
//
// 返回 (整任务规格, 逐条规格, 错误)。逐条规格的下标与 records 一一对应。
//
// 三条硬规则：
//  1. 要发邮件就得先配好 SMTP——没配就 422，而不是让任务跑完了才失败；
//  2. 收件人域名必须在白名单里（管理端测试邮件不受此限，那是另一条路）；
//  3. emails 长度必须等于 records 长度——长度对不上说明调用方自己也没想清楚
//     哪封信对应哪份文档，宁可报错也不猜。
func (s *Server) checkEmails(body *genBody, records [][]byte) (*mailer.EmailSpec, []*mailer.EmailSpec, *apiErr) {
	hasTop := body.Email != nil && !body.Email.Empty()
	hasPer := len(body.Emails) > 0
	if !hasTop && !hasPer {
		return nil, nil, nil
	}
	if s.Mailer == nil {
		return nil, nil, &apiErr{http.StatusServiceUnavailable, "internal_error", "邮件模块未初始化", nil}
	}
	c := s.Mailer.Load()
	if !c.Enabled() {
		return nil, nil, &apiErr{http.StatusUnprocessableEntity, "mail_not_configured",
			"邮件服务未配置：服务端尚未填写 SMTP 主机，无法投递（请联系管理员在「系统设置 · 邮件服务」配置）", nil}
	}

	check := func(spec mailer.EmailSpec, label string) *apiErr {
		if spec.Empty() {
			return nil
		}
		if err := spec.Validate(); err != nil {
			return &apiErr{http.StatusUnprocessableEntity, "data_invalid", label + err.Error(), nil}
		}
		if err := c.CheckRecipients(spec.To, spec.CC, spec.BCC); err != nil {
			return &apiErr{http.StatusUnprocessableEntity, "data_invalid", label + err.Error(), nil}
		}
		return nil
	}

	var top *mailer.EmailSpec
	if hasTop {
		cp := *body.Email
		if err := check(cp, "email："); err != nil {
			return nil, nil, err
		}
		top = &cp
	}
	var per []*mailer.EmailSpec
	if hasPer {
		if len(body.Emails) != len(records) {
			return nil, nil, &apiErr{http.StatusUnprocessableEntity, "data_invalid",
				fmt.Sprintf("emails 长度（%d）必须等于 records 长度（%d）", len(body.Emails), len(records)), nil}
		}
		for i, e := range body.Emails {
			cp := e
			if err := check(cp, fmt.Sprintf("emails[%d]：", i)); err != nil {
				return nil, nil, err
			}
			per = append(per, &cp)
		}
	}
	return top, per, nil
}
