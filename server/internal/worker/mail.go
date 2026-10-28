package worker

import (
	"encoding/json"
	"log"
	"os"
	"strings"
	"time"

	"docforge/server/internal/mailer"
	"docforge/server/internal/models"
)

// 本文件负责「任务渲染完成后按规格发信」这一段的编排。
//
// 三条边界先说清：
//  1. SMTP 配置只在服务端，API 侧只能给收件人/主题/正文/附件模式——收件人看到
//     的 From 永远是服务端配置的那个，调用方改不了；
//  2. 发信不阻塞任务：走 SendAsync，job 终态先落库，邮件状态后回写；
//  3. 整批全失败时不发信。一封没有任何附件的"你的文档已生成"比不发更糟。

// dispatchEmails 在 zip 打完之后调用：任务级一封 + 每份文档各一封。
func (w *Worker) dispatchEmails(job *models.Job) {
	if w.Mailer == nil {
		return
	}
	// 全失败：产物一份都没有，发信没有意义。
	if job.Succeeded == 0 {
		w.markEmailSkipped(job, "没有成功的产物，已跳过发信")
		return
	}

	docs := w.succeededDocs(job.ID)
	base := w.externalBase()
	// 调用人只查一次：逐条各发一封时，每封都回查一次 users 表就是 N+1，
	// 500 份的批量任务能白烧 500 次查询。
	actor := w.actorName(job)

	if spec, ok := w.jobEmailSpec(job); ok {
		w.sendJobEmail(job, docs, spec, base, actor)
	}
	for i := range docs {
		d := &docs[i]
		if spec, ok := w.docEmailSpec(d); ok {
			w.sendDocEmail(job, d, spec, base, actor)
		}
	}
}

func (w *Worker) succeededDocs(jobID string) []models.Document {
	var docs []models.Document
	w.DB.Where("job_id = ? AND status = ?", jobID, "succeeded").Order("record_index").Find(&docs)
	return docs
}

func (w *Worker) jobEmailSpec(job *models.Job) (mailer.EmailSpec, bool) {
	return decodeSpec(job.EmailSpec)
}

func (w *Worker) docEmailSpec(d *models.Document) (mailer.EmailSpec, bool) {
	return decodeSpec(d.EmailSpec)
}

func decodeSpec(raw models.JSONB) (mailer.EmailSpec, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return mailer.EmailSpec{}, false
	}
	var spec mailer.EmailSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		log.Printf("[worker] 邮件规格无法解析，已跳过：%v", err)
		return mailer.EmailSpec{}, false
	}
	if spec.Empty() {
		return mailer.EmailSpec{}, false
	}
	return spec, true
}

// markEmailSkipped 全失败时把所有待发规格统一置 skipped，避免任务详情里
// 永远停在 pending——pending 对调用方来说和"发了但没结果"无法区分。
func (w *Worker) markEmailSkipped(job *models.Job, msg string) {
	if job.EmailStatus == "pending" {
		w.DB.Model(&models.Job{}).Where("id = ?", job.ID).
			Updates(map[string]interface{}{"email_status": "skipped", "email_error": msg})
	}
	w.DB.Model(&models.Document{}).
		Where("job_id = ? AND email_status = ?", job.ID, "pending").
		Updates(map[string]interface{}{"email_status": "skipped", "email_error": msg})
}

// sendJobEmail 整任务一封。
func (w *Worker) sendJobEmail(job *models.Job, docs []models.Document, spec mailer.EmailSpec, base, actor string) {
	// 任务级邮件的变量来源：只有一条记录时就是它的数据；多条时取第一条，
	// 并额外注入 job.* 命名空间（总数/成功数/模板编号等）。
	data := map[string]interface{}{}
	if len(docs) > 0 {
		data = docData(&docs[0])
	}
	data["job"] = map[string]interface{}{
		"id":            job.ID,
		"total":         job.Total,
		"succeeded":     job.Succeeded,
		"failed":        job.Failed,
		"template_code": w.templateCode(job.TemplateID),
		"external_ref":  job.ExternalRef,
		"created_at":    job.CreatedAt.Format("2006-01-02 15:04"),
		"date":          time.Now().Format("2006-01-02"),
	}

	rendered, missing := mailer.Render(spec, data)
	if len(missing) > 0 {
		log.Printf("[worker] job %s 邮件变量缺失（已置空）：%v", job.ID, missing)
	}
	msg := mailer.Message{
		To: rendered.To, CC: rendered.CC, BCC: rendered.BCC,
		Subject: defaultSubject(rendered.Subject, w.templateName(job.TemplateID)),
		Body:    rendered.Body,
	}
	zipName := w.templateCode(job.TemplateID) + "-" + shortID(job.ID) + ".zip"
	msg.Attach = w.jobAttachments(job, docs, rendered.Attach, zipName)
	if rendered.WantsLink() {
		msg.Body = appendLink(msg.Body, w.jobLink(job, docs, base))
	}
	w.Mailer.SendAsync(mailer.Outbox{
		JobID: job.ID, Source: "job", Message: msg,
		ActorUserID: job.CreatedByUserID, ActorName: actor, APIKeyID: job.APIKeyID,
	})
}

// sendDocEmail 每条记录各一封：附件只带这一份，变量用这一条的数据。
func (w *Worker) sendDocEmail(job *models.Job, d *models.Document, spec mailer.EmailSpec, base, actor string) {
	data := docData(d)
	data["job"] = map[string]interface{}{
		"id": job.ID, "total": job.Total,
		"succeeded": job.Succeeded, "failed": job.Failed,
		"template_code": w.templateCode(job.TemplateID),
		"external_ref":  job.ExternalRef,
		"date":          time.Now().Format("2006-01-02"),
	}
	rendered, missing := mailer.Render(spec, data)
	if len(missing) > 0 {
		log.Printf("[worker] doc %s 邮件变量缺失（已置空）：%v", d.ID, missing)
	}
	msg := mailer.Message{
		To: rendered.To, CC: rendered.CC, BCC: rendered.BCC,
		Subject: defaultSubject(rendered.Subject, stripExt(d.Filename)),
		Body:    rendered.Body,
	}
	msg.Attach = w.docAttachment(d, rendered.Attach)
	if rendered.WantsLink() {
		link := base + "/api/v1/s/" + w.docToken(d)
		msg.Body = appendLink(msg.Body, link)
	}
	w.Mailer.SendAsync(mailer.Outbox{
		JobID: job.ID, DocumentID: d.ID, Source: "job", Message: msg,
		ActorUserID: job.CreatedByUserID, ActorName: actor, APIKeyID: job.APIKeyID,
	})
}

// jobAttachments 决定整包邮件带什么附件。
//
// auto 的规则刻意做成「一份就带那份，多份才带 zip」：收件人拿到一个装着
// 单个文件的压缩包，多半会以为系统出错了。
func (w *Worker) jobAttachments(job *models.Job, docs []models.Document, mode, zipName string) []mailer.Attachment {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case mailer.AttachNone:
		return nil
	case mailer.AttachZip:
		return w.readZip(job, zipName)
	}
	if len(docs) == 1 {
		return w.docAttachment(&docs[0], mode)
	}
	if mode == mailer.AttachDocx || mode == mailer.AttachPDF || mode == mailer.AttachBoth {
		// 显式指定单格式但有多份：全部按该格式带上，不擅自换成 zip。
		var out []mailer.Attachment
		for i := range docs {
			out = append(out, w.docAttachment(&docs[i], mode)...)
		}
		return out
	}
	return w.readZip(job, zipName)
}

func (w *Worker) readZip(job *models.Job, zipName string) []mailer.Attachment {
	if job.ZipPath == "" {
		return nil
	}
	b, err := os.ReadFile(job.ZipPath)
	if err != nil {
		log.Printf("[worker] 读取 zip 失败 job=%s: %v", job.ID, err)
		return nil
	}
	return []mailer.Attachment{{Filename: zipName, Content: b}}
}

// docAttachment 单份文档的附件，按模式挑文件。
func (w *Worker) docAttachment(d *models.Document, mode string) []mailer.Attachment {
	var out []mailer.Attachment
	add := func(path, ext string) {
		if path == "" {
			return
		}
		b, err := os.ReadFile(path)
		if err != nil {
			log.Printf("[worker] 读取产物失败 doc=%s path=%s: %v", d.ID, path, err)
			return
		}
		out = append(out, mailer.Attachment{Filename: stripExt(d.Filename) + ext, Content: b})
	}
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case mailer.AttachDocx:
		add(d.DocxPath, ".docx")
	case mailer.AttachPDF:
		add(d.PdfPath, ".pdf")
	// auto 必须在这里有分支。
	//
	// 之前漏了：jobAttachments 在「只有一份产物」时把 mode（可能是 auto）原样传进来，
	// 而这里不认识 auto 就落到 switch 外面，返回空附件列表——于是
	// 「单份报告 + 不指定 attach（默认 auto）」这个最常见的组合，邮件里一个附件都没有，
	// 收件人只看到一句「你的文档已生成」，附件无处可寻，而且没有任何报错。
	case mailer.AttachAuto, mailer.AttachBoth, "":
		add(d.DocxPath, ".docx")
		add(d.PdfPath, ".pdf")
	case mailer.AttachZip:
		// 单份也要 zip：调用方明确要了就给，附 zip 名。
		add(d.DocxPath, ".docx")
		add(d.PdfPath, ".pdf")
	case mailer.AttachNone:
		return nil
	}
	return out
}

// jobLink 任务级邮件正文的下载直链：只有一份时给文档直链，多份给整包直链。
func (w *Worker) jobLink(job *models.Job, docs []models.Document, base string) string {
	if len(docs) == 1 {
		return base + "/api/v1/s/" + w.docToken(&docs[0])
	}
	return base + "/api/v1/s/" + w.jobToken(job)
}

func (w *Worker) docToken(d *models.Document) string {
	tok, _ := w.Signer.SignDocument(d.ID)
	if d.DocxPath == "" && d.PdfPath != "" {
		return tok + "?format=pdf"
	}
	return tok
}

func (w *Worker) jobToken(job *models.Job) string {
	tok, _ := w.Signer.SignJob(job.ID)
	return tok
}

func (w *Worker) externalBase() string {
	if w.Cfg != nil {
		return strings.TrimRight(w.Cfg.ExternalBaseURL, "/")
	}
	return ""
}

func (w *Worker) templateCode(tplID string) string {
	var t models.Template
	if err := w.DB.First(&t, "id = ?", tplID).Error; err == nil && t.Code != "" {
		return t.Code
	}
	if tplID != "" {
		return tplID
	}
	return "documents"
}

func (w *Worker) templateName(tplID string) string {
	var t models.Template
	if err := w.DB.First(&t, "id = ?", tplID).Error; err == nil && t.Name != "" {
		return t.Name
	}
	return "文档"
}

// appendLink 把下载直链追加到正文末尾。没配 ExternalBaseURL 时不追加——
// 印一条 http:///api/v1/s/xxx 出来比不给链接更糟。
func appendLink(body, link string) string {
	if link == "" || !strings.HasPrefix(link, "http") {
		return body
	}
	tail := "\n\n下载链接：" + link
	if strings.Contains(body, link) {
		return body
	}
	return strings.TrimRight(body, "\n") + tail
}

func defaultSubject(subject, fallback string) string {
	if strings.TrimSpace(subject) == "" {
		return fallback
	}
	return subject
}

func docData(d *models.Document) map[string]interface{} {
	out := map[string]interface{}{}
	if len(d.InputData) > 0 {
		_ = json.Unmarshal(d.InputData, &out)
	}
	if out == nil {
		out = map[string]interface{}{}
	}
	out["filename"] = d.Filename
	out["record_index"] = d.RecordIndex
	return out
}

func stripExt(name string) string {
	if i := strings.LastIndex(name, "."); i > 0 {
		return name[:i]
	}
	return name
}

func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[len(id)-8:]
}

// actorName 投递审计里的「调用人」。worker 拿不到 Principal，
// 只能按任务的创建者回查一次；查不到也不要紧，审计里还有 user_id。
func (w *Worker) actorName(job *models.Job) string {
	if job.APIKeyID != "" {
		var ak models.APIKey
		if err := w.DB.First(&ak, "id = ?", job.APIKeyID).Error; err == nil {
			return ak.Name
		}
		return ""
	}
	if job.CreatedByUserID == "" {
		return ""
	}
	var u models.User
	if err := w.DB.First(&u, "id = ?", job.CreatedByUserID).Error; err != nil {
		return ""
	}
	if u.DisplayName != "" {
		return u.DisplayName
	}
	return u.Username
}
