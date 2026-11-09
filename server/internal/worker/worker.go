// Package worker 实现契约 §5 的单 goroutine 任务状态机：
// queued → running → succeeded/partial/failed；逐条调 render、文件落盘、
// 失败隔离、zip 打包、终结回调、保留期清理 ticker。
package worker

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"time"

	"docforge/server/internal/assetimg"
	"docforge/server/internal/callback"
	"docforge/server/internal/config"
	"docforge/server/internal/fname"
	"docforge/server/internal/mailer"
	"docforge/server/internal/models"
	"docforge/server/internal/render"
	"docforge/server/internal/sigauth"
	"docforge/server/internal/sign"
	"docforge/server/internal/storage"

	"gorm.io/gorm"
)

// Worker 消费 queued 任务。
type Worker struct {
	DB      *gorm.DB
	Cfg     *config.Config
	Store   *storage.Store
	Renderer render.Renderer
	Signer  *sign.Signer
	Sender  *callback.Sender
	// Mailer 为 nil 时整个邮件链路静默跳过（未启用邮件功能的部署）。
	Mailer  *mailer.Sender
}

// New 构造。
func New(db *gorm.DB, cfg *config.Config, st *storage.Store, rd render.Renderer, sg *sign.Signer, sender *callback.Sender, mail *mailer.Sender) *Worker {
	return &Worker{DB: db, Cfg: cfg, Store: st, Renderer: rd, Signer: sg, Sender: sender, Mailer: mail}
}

// Run 启动主循环与清理 ticker，直到 ctx 取消。
func (w *Worker) Run(ctx context.Context) {
	go w.cleanupLoop(ctx)
	t := time.NewTicker(w.Cfg.WorkerPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.tick(ctx)
		}
	}
}

// tick 领取并处理一个 queued 任务。
func (w *Worker) tick(ctx context.Context) {
	var job models.Job
	if err := w.DB.Where("status = ?", "queued").Order("created_at").First(&job).Error; err != nil {
		return
	}
	// 乐观抢占：仅当仍是 queued 时置 running。
	res := w.DB.Model(&models.Job{}).
		Where("id = ? AND status = ?", job.ID, "queued").
		Updates(map[string]interface{}{"status": "running", "started_at": time.Now()})
	if res.Error != nil || res.RowsAffected == 0 {
		return
	}
	w.processJob(ctx, &job)
}

// processJob 渲染任务的全部 document，失败隔离。
func (w *Worker) processJob(ctx context.Context, job *models.Job) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("worker panic on job %s: %v", job.ID, r)
			// 收尾本身也可能崩（比如 DB 连接断了），再兜一层：
			// 这里再 panic 出去，整个 worker 协程就没了，队列再也没人消费。
			func() {
				defer func() {
					if r2 := recover(); r2 != nil {
						log.Printf("worker panic 收尾时再次失败 job %s: %v", job.ID, r2)
					}
				}()
				// 队列里这批文档不会再有第二次机会：任务已被判失败，但文档还停在
				// queued/running 的话，前端会一直显示「生成中」，而任务已经结束，
				// 用户没有任何入口把它们改回来。
				w.failAll(ctx, job, "生成过程中出现内部错误，本条已中止")
				w.refreshCounts(job)
				w.finalize(ctx, job)
			}()
		}
	}()

	verDocx, err := w.loadTemplateDocx(job.VersionID)
	if err != nil {
		w.failAll(ctx, job, "模板文件不可用："+err.Error())
		// 必须重算计数再终态：failAll 只动了 documents 表，job 上的 succeeded/failed
		// 还是建任务时的 0/0。finalize 靠 job.Failed 判终态，0/0 会被判成 succeeded，
		// 于是「模板文件丢了」这件事在列表里显示为成功，用户点下载拿到一个空包。
		w.refreshCounts(job)
		w.finalize(ctx, job)
		return
	}
	tplID := w.templateIDOfVersion(job.VersionID)

	var docs []models.Document
	w.DB.Where("job_id = ?", job.ID).Order("record_index").Find(&docs)

	for i := range docs {
		d := &docs[i]
		if d.Status != "queued" {
			continue
		}
		w.DB.Model(d).Updates(map[string]interface{}{"status": "running", "started_at": time.Now()})
		w.renderOne(ctx, job, d, verDocx, tplID)
	}

	// 重新统计计数。
	w.refreshCounts(job)
	w.finalize(ctx, job)
}

func (w *Worker) loadTemplateDocx(verID string) ([]byte, error) {
	var v models.TemplateVersion
	if err := w.DB.First(&v, "id = ?", verID).Error; err != nil {
		return nil, err
	}
	if v.FilePath == "" {
		return nil, fmt.Errorf("版本 %s 无文件", v.Version)
	}
	return os.ReadFile(v.FilePath)
}

func (w *Worker) templateIDOfVersion(verID string) string {
	var v models.TemplateVersion
	if err := w.DB.First(&v, "id = ?", verID).Error; err != nil {
		return ""
	}
	return v.TemplateID
}

// renderOne 渲染单条 document（docx/pdf/both）。
func (w *Worker) renderOne(ctx context.Context, job *models.Job, d *models.Document, verDocx []byte, tplID string) {
	start := time.Now()
	upd := map[string]interface{}{"finished_at": time.Now()}
	jobDir, err := w.Store.JobDir(job.ID)
	if err != nil {
		upd["status"] = "failed"
		upd["error"] = "存储目录不可用"
		w.DB.Model(d).Updates(upd)
		return
	}

	doPdf := job.Format == "pdf" || job.Format == "both"
	doDocx := job.Format == "docx" || job.Format == "both"

	// 合并模板资源图片默认值（bind_var 注入 data:URI；用户数据优先）。
	data := (&assetimg.Resolver{DB: w.DB}).MergeDefaults(tplID, d.InputData)
	// 从版本 variables 中提取 sig_/seal_ 占位变量名，以空值合并进 data，供签章校验与注入。
	var verRow models.TemplateVersion
	if err := w.DB.First(&verRow, "id = ?", job.VersionID).Error; err == nil {
		if sigNames := sigauth.VarsSigNames(verRow.Variables); len(sigNames) > 0 {
			data = sigauth.InjectPlaceholders(data, sigNames)
		}
	}
	// 签章授权与 fail-closed 注入（原子扣减 grant）。
	data, err = (&sigauth.Resolver{DB: w.DB, Renderer: w.Renderer}).Inject(data, sigauth.Options{
		GranteeType: granteeType(job), GranteeID: granteeID(job),
		IsAdmin: w.isAdmin(job.CreatedByUserID),
		JobID: job.ID, ExternalRef: job.ExternalRef,
		Ctx: ctx,
	})
	if err != nil {
		upd["status"] = "failed"
		upd["error"] = friendlyErr(err)
		w.DB.Model(d).Updates(upd)
		return
	}

	if doDocx {
		out, _, rerr := w.Renderer.Render(ctx, verDocx, data, "docx")
		if rerr != nil {
			w.markDocError(d, upd, rerr)
			w.DB.Model(d).Updates(upd)
			return
		}
		p := filepath.Join(jobDir, d.ID+".docx")
		if werr := os.WriteFile(p, out, 0o644); werr != nil {
			upd["status"] = "failed"
			upd["error"] = "写入产物失败"
			w.DB.Model(d).Updates(upd)
			return
		}
		upd["docx_path"] = p
	}
	if doPdf {
		out, _, rerr := w.Renderer.Render(ctx, verDocx, data, "pdf")
		if rerr != nil {
			// both 时 docx 已成功，记 pdf 失败但不判整条失败（以 docx 为主）。
			if doDocx {
				upd["status"] = "succeeded"
				upd["duration_ms"] = time.Since(start).Milliseconds()
				upd["error"] = "PDF 导出失败：" + friendlyErr(rerr)
				w.DB.Model(d).Updates(upd)
				return
			}
			w.markDocError(d, upd, rerr)
			w.DB.Model(d).Updates(upd)
			return
		}
		p := filepath.Join(jobDir, d.ID+".pdf")
		if werr := os.WriteFile(p, out, 0o644); werr != nil {
			upd["status"] = "failed"
			upd["error"] = "写入产物失败"
			w.DB.Model(d).Updates(upd)
			return
		}
		upd["pdf_path"] = p
	}

	upd["status"] = "succeeded"
	upd["duration_ms"] = time.Since(start).Milliseconds()
	w.DB.Model(d).Updates(upd)
}

func (w *Worker) markDocError(d *models.Document, upd map[string]interface{}, rerr error) {
	upd["status"] = "failed"
	upd["error"] = friendlyErr(rerr)
	if re, ok := rerr.(*render.Error); ok && len(re.Details) > 0 {
		upd["path_err"] = re.Details[0].Path
	}
}

func friendlyErr(err error) string {
	if re, ok := err.(*render.Error); ok {
		if re.Message != "" {
			return re.Message
		}
		return "渲染失败（HTTP " + itoa(re.StatusCode) + "）"
	}
	return err.Error()
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

// refreshCounts 重新统计成功/失败数。
func (w *Worker) refreshCounts(job *models.Job) {
	var succeeded, failed int64
	w.DB.Model(&models.Document{}).Where("job_id = ? AND status = ?", job.ID, "succeeded").Count(&succeeded)
	w.DB.Model(&models.Document{}).Where("job_id = ? AND status = ?", job.ID, "failed").Count(&failed)
	job.Succeeded = int(succeeded)
	job.Failed = int(failed)
}

func (w *Worker) failAll(ctx context.Context, job *models.Job, msg string) {
	w.DB.Model(&models.Document{}).Where("job_id = ? AND status <> ?", job.ID, "failed").
		Updates(map[string]interface{}{"status": "failed", "error": msg, "finished_at": time.Now()})
}

// finalize 决定任务终态、打 zip、登记过期时间、触发回调。
func (w *Worker) finalize(ctx context.Context, job *models.Job) {
	status := "succeeded"
	switch {
	case job.Failed > 0 && job.Succeeded == 0:
		status = "failed"
	case job.Failed > 0:
		status = "partial"
	}
	zipPath, err := w.zipJob(job)
	if err != nil {
		log.Printf("zip job %s failed: %v", job.ID, err)
	}
	// 汇总失败明细到 job.error_details（契约 §4.7 任务详情 errors 列表）。
	var errDetails []map[string]interface{}
	var failedDocs []models.Document
	w.DB.Where("job_id = ? AND status = ?", job.ID, "failed").Order("record_index").Find(&failedDocs)
	for _, d := range failedDocs {
		errDetails = append(errDetails, map[string]interface{}{
			"record_index": d.RecordIndex, "filename": d.Filename,
			"error": d.Error, "path": d.PathErr,
		})
	}
	if errDetails == nil {
		errDetails = []map[string]interface{}{}
	}
	errJSON, _ := json.Marshal(errDetails)
	expires := time.Now().AddDate(0, 0, w.Cfg.RetentionDays)
	upd := map[string]interface{}{
		"status": status, "finished_at": time.Now(),
		"succeeded": job.Succeeded, "failed": job.Failed,
		"error_details": models.JSONB(errJSON),
		"expires_at": expires, "zip_path": zipPath,
	}
	w.DB.Model(job).Updates(upd)
	job.ErrorDetails = models.JSONB(errJSON)
	job.Status = status
	job.ZipPath = zipPath
	job.ExpiresAt = &expires
	// 邮件派发放在 zip 之后：整包附件要读刚打好的 zip。
	w.dispatchEmails(job)
	if job.CallbackURL != "" {
		w.Sender.SendAsync(job)
	}
}

// zipJob 打包整包：both 时 docx/pdf 分子目录，失败条目写 errors.json。
func (w *Worker) zipJob(job *models.Job) (string, error) {
	jobDir, err := w.Store.JobDir(job.ID)
	if err != nil {
		return "", err
	}
	var docs []models.Document
	w.DB.Where("job_id = ?", job.ID).Order("record_index").Find(&docs)

	zipPath := filepath.Join(jobDir, "result.zip")
	zf, err := os.Create(zipPath)
	if err != nil {
		return "", err
	}
	defer zf.Close()
	zw := zip.NewWriter(zf)
	defer zw.Close()

	both := job.Format == "both"
	for _, d := range docs {
		if d.Status == "succeeded" {
			if both {
				// 条目名必须用正斜杠。filepath.Join 在 Windows 上产出反斜杠，
				// 而 ZIP 的条目分隔符按规范（APPNOTE 4.4.17.1）固定是 "/"：
				// 用反斜杠打出来的包，在 Linux/macOS 上解压会得到一批名字里
				// 带 "\" 的单文件，而不是 docx/ 与 pdf/ 两个目录。
				w.writeZipFile(zw, path.Join("docx", d.Filename), d.DocxPath)
				pdfName := changeExt(d.Filename, ".pdf")
				w.writeZipFile(zw, path.Join("pdf", pdfName), d.PdfPath)
			} else {
				src := d.DocxPath
				if job.Format == "pdf" {
					src = d.PdfPath
				}
				w.writeZipFile(zw, d.Filename, src)
			}
		}
	}
	// errors.json
	var errs []map[string]interface{}
	for _, d := range docs {
		if d.Status == "failed" {
			errs = append(errs, map[string]interface{}{
				"record_index": d.RecordIndex, "filename": d.Filename,
				"error": d.Error, "path": d.PathErr,
			})
		}
	}
	if len(errs) > 0 {
		b, _ := json.MarshalIndent(map[string]interface{}{"errors": errs}, "", "  ")
		if ew, e2 := zw.Create("errors.json"); e2 == nil {
			_, _ = ew.Write(b)
		}
	}
	return zipPath, nil
}

func (w *Worker) writeZipFile(zw *zip.Writer, name, src string) {
	if src == "" {
		return
	}
	f, err := os.Open(src)
	if err != nil {
		return
	}
	defer f.Close()
	wf, err := zw.Create(name)
	if err != nil {
		return
	}
	_, _ = io.Copy(wf, f)
}

func changeExt(name, ext string) string {
	if i := bytesLastDot([]byte(name)); i > 0 {
		return string(name[:i]) + ext
	}
	return name + ext
}

func bytesLastDot(b []byte) int {
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] == '.' {
			return i
		}
	}
	return -1
}

// cleanupLoop 每小时清理保留期到期的产物：删文件 + 标记 expired。
func (w *Worker) cleanupLoop(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	w.cleanup(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.cleanup(ctx)
		}
	}
}

func (w *Worker) cleanup(ctx context.Context) {
	// 过期会话没有第二个清理入口：cookie 到期只是浏览器不再发送，
	// 服务端这行会一直留着，一年下来就是一张只涨不消的表。
	w.DB.Where("expires_at < ?", time.Now()).Delete(&models.Session{})

	var jobs []models.Job
	w.DB.Where("expires_at < ? AND expired = ?", time.Now(), false).Find(&jobs)
	for i := range jobs {
		j := &jobs[i]
		var docs []models.Document
		w.DB.Where("job_id = ?", j.ID).Find(&docs)
		for _, d := range docs {
			os.Remove(d.DocxPath)
			os.Remove(d.PdfPath)
			w.DB.Model(d).Updates(map[string]interface{}{"expired": true, "docx_path": "", "pdf_path": ""})
		}
		if j.ZipPath != "" {
			os.Remove(j.ZipPath)
		}
		w.DB.Model(j).Updates(map[string]interface{}{"expired": true, "zip_path": ""})
	}

	w.cleanupDeletedTemplates()
}

// cleanupDeletedTemplates 回收软删模板留在磁盘上的版本文件与缩略图。
//
// handleDeleteTemplate 是软删：只改数据库（code 释放成 deleted:<id>、enabled=false、
// 写 deleted_at），那里的注释承诺「磁盘占位交给保留期清理」。但清理协程此前只扫
// jobs / documents，templates/{tpl}/ 与 thumbnails/ 从来没被回收过——每删一个模板
// 就在磁盘上永久多两个孤儿文件，删得越多攒得越多。
//
// 为什么不在删的那一刻删：保留期内 POST /documents/{id}/regenerate 还要读回版本
// 文件重新渲染，当场删会把「重新生成」打成 500。所以按 deleted_at 起算，过了
// RetentionDays 才真正回收，语义与 job 的 expires_at 一致。
//
// 只回收磁盘，不动数据库软删行：历史 job / document 还引用着 template_id，
// 物理删行会让「生成记录」里的模板名变空白，审计链断掉。
func (w *Worker) cleanupDeletedTemplates() {
	// Root 为空或是相对路径时，filepath.Join 会退化成相对路径，
	// os.RemoveAll 就变成删进程当前目录。宁可这批不清，也不冒这个险。
	if w.Store == nil || w.Store.Root == "" || !filepath.IsAbs(w.Store.Root) {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -w.Cfg.RetentionDays)

	var tpls []models.Template
	// Unscoped：软删行被 GORM 默认过滤，不绕过就一行都查不到。
	err := w.DB.Unscoped().
		Where("deleted_at IS NOT NULL AND deleted_at < ?", cutoff).
		Find(&tpls).Error
	if err != nil {
		log.Printf("[worker] 查询待回收模板失败: %v", err)
		return
	}

	reclaimed := 0
	for i := range tpls {
		t := &tpls[i]
		dir := filepath.Join(w.Store.Root, "templates", t.ID)
		// 缩略图按 tpl_<id>_ver_<vid>.png 命名，一个模板可能有多个版本
		thumbs, _ := filepath.Glob(filepath.Join(w.Store.Root, "thumbnails", t.ID+"_*.png"))
		if _, statErr := os.Stat(dir); os.IsNotExist(statErr) && len(thumbs) == 0 {
			continue // 上一轮已经清过，别每小时重复 log
		}
		if err := os.RemoveAll(dir); err != nil {
			log.Printf("[worker] 删除模板目录失败 %s: %v", t.ID, err)
			continue
		}
		for _, f := range thumbs {
			os.Remove(f)
		}
		reclaimed++
	}
	if reclaimed > 0 {
		log.Printf("[worker] 已回收 %d 个软删模板的磁盘占位（保留期 %d 天）", reclaimed, w.Cfg.RetentionDays)
	}
}

// BuildFilename 供 API 层在创建 document 时计算文件名。
func BuildFilename(rule string, record map[string]interface{}, index int, code, format string) string {
	base := fname.Render(rule, record, index, code)
	return fname.WithExt(base, format)
}

// MarshalData 把 record 序列化为 JSON 字节。
func MarshalData(v interface{}) []byte {
	b, _ := json.Marshal(v)
	return b
}

func granteeType(job *models.Job) string {
	if job.APIKeyID != "" {
		return "api_key"
	}
	return "user"
}
func granteeID(job *models.Job) string {
	if job.APIKeyID != "" {
		return job.APIKeyID
	}
	return job.CreatedByUserID
}
func (w *Worker) isAdmin(userID string) bool {
	if userID == "" {
		return false
	}
	var u models.User
	if err := w.DB.First(&u, "id = ?", userID).Error; err != nil {
		return false
	}
	return u.IsAdmin()
}
