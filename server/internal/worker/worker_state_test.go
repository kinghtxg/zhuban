package worker

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"docforge/server/internal/models"
	"docforge/server/internal/render"
)

// 这一组补的是 worker.go 的状态机与分支，而不是「happy path 能不能跑」。
//
// 为什么值得单独写：worker 是整个系统里唯一「没有请求上下文」的部分——
// 没有用户在旁边看着，出错只体现为数据库里一行状态。所以它的每个分支
// 都必须有可断言的落点（状态、文件、zip 内容），不能靠"跑一遍看看"。

// scriptedRenderer 在 mockRenderer 之上按「第几次调用」定制行为。
//
// 为什么要按调用序号而不是按格式：一次 Render 可能是 docx 也可能是 pdf，
// 想造「docx 成功、pdf 失败」这种 both 模式下的真实场景，必须知道当前是第几次。
type scriptedRenderer struct {
	*mockRenderer
	mu    sync.Mutex
	n     int
	calls []renderCall
	hook  func(i int, format string) ([]byte, error)
}

type renderCall struct {
	Format string
	Data   []byte
}

func newScripted(hook func(i int, format string) ([]byte, error)) *scriptedRenderer {
	return &scriptedRenderer{mockRenderer: &mockRenderer{}, hook: hook}
}

func (m *scriptedRenderer) Render(ctx context.Context, docx, data []byte, format string) ([]byte, string, error) {
	m.mu.Lock()
	i := m.n
	m.n++
	m.calls = append(m.calls, renderCall{Format: format, Data: append([]byte(nil), data...)})
	m.mu.Unlock()
	if m.hook != nil {
		b, err := m.hook(i, format)
		if err != nil {
			return nil, "", err
		}
		return b, "application/octet-stream", nil
	}
	return []byte("FAKE-" + format), "application/octet-stream", nil
}

func (m *scriptedRenderer) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.n
}

func (m *scriptedRenderer) formats() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.calls))
	for _, c := range m.calls {
		out = append(out, c.Format)
	}
	return out
}

// ---- 脚手架 ----

// seedTemplate 建一个可用模板版本，返回 version id。
func seedTemplate(t *testing.T, w *Worker, tplID, verID, code string) string {
	t.Helper()
	fp := filepath.Join(t.TempDir(), "orig.docx")
	if err := os.WriteFile(fp, []byte("DOCX-BYTES"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := w.DB.Create(&models.Template{
		ID: tplID, Code: code, Name: "测试模板", CurrentVersionID: verID,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := w.DB.Create(&models.TemplateVersion{
		ID: verID, TemplateID: tplID, Version: "1.0.0", FilePath: fp,
	}).Error; err != nil {
		t.Fatal(err)
	}
	return verID
}

// seedJob 建任务与 n 条 queued document。
//
// 文件名要按 format 生成扩展名：生产里 BuildFilename 就是拿请求的 format 做的，
// 写死 .docx 会让「pdf 任务的 zip 里是 .docx」这种假象出现，测出来的是测试数据的错。
func seedJob(t *testing.T, w *Worker, id, tplID, verID, format string, n int) *models.Job {
	t.Helper()
	job := &models.Job{ID: id, TemplateID: tplID, VersionID: verID, Status: "queued", Total: n, Format: format}
	if err := w.DB.Create(job).Error; err != nil {
		t.Fatal(err)
	}
	ext := ".docx"
	if format == "pdf" {
		ext = ".pdf"
	}
	for i := 0; i < n; i++ {
		if err := w.DB.Create(&models.Document{
			ID: fmt.Sprintf("%s_d%d", id, i), JobID: id, RecordIndex: i,
			Filename:  fmt.Sprintf("第 %d 份%s", i+1, ext),
			Status:    "queued",
			InputData: models.JSONB(`{"unit":"测试单位"}`),
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	return job
}

func reloadJob(t *testing.T, w *Worker, id string) models.Job {
	t.Helper()
	var j models.Job
	if err := w.DB.First(&j, "id = ?", id).Error; err != nil {
		t.Fatalf("读任务 %s 失败：%v", id, err)
	}
	return j
}

func reloadDocs(t *testing.T, w *Worker, jobID string) []models.Document {
	t.Helper()
	var docs []models.Document
	if err := w.DB.Where("job_id = ?", jobID).Order("record_index").Find(&docs).Error; err != nil {
		t.Fatalf("读文档失败：%v", err)
	}
	return docs
}

func zipEntryNames(t *testing.T, p string) []string {
	t.Helper()
	r, err := zip.OpenReader(p)
	if err != nil {
		t.Fatalf("打开 zip 失败（%s）：%v", p, err)
	}
	defer r.Close()
	names := make([]string, 0, len(r.File))
	for _, f := range r.File {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	return names
}

func readZipEntry(t *testing.T, p, name string) []byte {
	t.Helper()
	r, err := zip.OpenReader(p)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for _, f := range r.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		defer rc.Close()
		buf := make([]byte, f.UncompressedSize64)
		if _, err := rc.Read(buf); err != nil && err.Error() != "EOF" {
			t.Fatal(err)
		}
		return buf
	}
	t.Fatalf("zip 里没有 %s，实际有 %v", name, zipEntryNames(t, p))
	return nil
}

// ---- 状态机与抢占 ----

// 只有 queued 的任务能被领走。已 running 的不能被重复领取——
// 否则两个 tick 会同时对同一批 document 渲染，产物互相覆盖。
func TestTick_OnlyClaimsQueuedJob(t *testing.T) {
	w, _ := setupWorker(t)
	seedTemplate(t, w, "tpl_a", "ver_a", "code-a")

	running := &models.Job{ID: "jb_already_running", TemplateID: "tpl_a", VersionID: "ver_a",
		Status: "running", Total: 1, Format: "docx"}
	if err := w.DB.Create(running).Error; err != nil {
		t.Fatal(err)
	}
	w.tick(context.Background())

	if got := reloadJob(t, w, running.ID); got.Status != "running" {
		t.Fatalf("已在运行的任务状态被改成了 %s，tick 不该碰它", got.Status)
	}

	job := seedJob(t, w, "jb_queued", "tpl_a", "ver_a", "docx", 1)
	w.tick(context.Background())

	final := reloadJob(t, w, job.ID)
	if final.Status != "succeeded" {
		t.Fatalf("queued 任务应被处理成 succeeded，实际 %s（error=%s）", final.Status, final.ErrorDetails)
	}
}

// 没有 queued 任务时 tick 必须安静返回，不能因为 First() 报错就 panic 或改状态。
func TestTick_NoQueuedJobIsNoop(t *testing.T) {
	w, _ := setupWorker(t)
	w.tick(context.Background())
	w.tick(context.Background())
}

// 渲染器 panic 时，任务要被兜住并落到 failed，而不是整个 worker 协程崩掉
// （崩掉之后没人再消费队列，线上表现为"任务永远排队"）。
func TestProcessJob_PanicIsContained(t *testing.T) {
	w, _ := setupWorker(t)
	seedTemplate(t, w, "tpl_p", "ver_p", "code-p")
	job := seedJob(t, w, "jb_panic", "tpl_p", "ver_p", "docx", 2)

	w.Renderer = newScripted(func(i int, format string) ([]byte, error) {
		panic("渲染器内部炸了")
	})
	w.processJob(context.Background(), job)

	if got := reloadJob(t, w, job.ID).Status; got != "failed" {
		t.Fatalf("panic 后任务应为 failed，实际 %s", got)
	}
	// 遗留的 running 文档必须一起收尾：否则前端会一直显示「生成中」，
	// 而任务本身已经结束了，用户没有任何入口把它们改回来。
	for _, d := range reloadDocs(t, w, job.ID) {
		if d.Status == "running" || d.Status == "queued" {
			t.Errorf("panic 后文档 %s 仍停在 %s，应该一起置 failed", d.ID, d.Status)
		}
	}
}

// ---- 模板不可用 ----

// 版本记录在、但文件被删了：属于运维事故（清理脚本误删），
// 必须把整批标失败并给出可读原因，而不是留一批 queued 永远不动。
func TestProcessJob_TemplateFileGone(t *testing.T) {
	w, _ := setupWorker(t)
	fp := filepath.Join(t.TempDir(), "gone.docx")
	if err := os.WriteFile(fp, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	w.DB.Create(&models.Template{ID: "tpl_g", Code: "code-g", Name: "n", CurrentVersionID: "ver_g"})
	w.DB.Create(&models.TemplateVersion{ID: "ver_g", TemplateID: "tpl_g", Version: "1.0.0", FilePath: fp})
	if err := os.Remove(fp); err != nil {
		t.Fatal(err)
	}
	job := seedJob(t, w, "jb_gone", "tpl_g", "ver_g", "docx", 2)

	w.processJob(context.Background(), job)

	final := reloadJob(t, w, job.ID)
	if final.Status != "failed" {
		t.Fatalf("模板文件缺失应判 failed，实际 %s", final.Status)
	}
	if final.Failed != 2 {
		t.Errorf("两条文档都该标失败，实际 failed=%d", final.Failed)
	}
	docs := reloadDocs(t, w, job.ID)
	for _, d := range docs {
		if !strings.Contains(d.Error, "模板文件不可用") {
			t.Errorf("文档 %s 的错误信息应说明模板不可用，实际 %q", d.ID, d.Error)
		}
	}
}

// 版本 FilePath 为空（历史数据/上传中断留下的半条记录）。
func TestProcessJob_VersionWithoutFile(t *testing.T) {
	w, _ := setupWorker(t)
	w.DB.Create(&models.Template{ID: "tpl_n", Code: "code-n", Name: "n", CurrentVersionID: "ver_n"})
	w.DB.Create(&models.TemplateVersion{ID: "ver_n", TemplateID: "tpl_n", Version: "1.0.0", FilePath: ""})
	job := seedJob(t, w, "jb_nofile", "tpl_n", "ver_n", "docx", 1)

	w.processJob(context.Background(), job)

	if got := reloadJob(t, w, job.ID).Status; got != "failed" {
		t.Fatalf("版本无文件应判 failed，实际 %s", got)
	}
}

// ---- 格式三态与 zip 结构 ----

// both：docx 与 pdf 都要出，zip 里分 docx/ 与 pdf/ 两个子目录。
// 不分目录的话两份同名文件会互相覆盖（zip 允许重名，解压时只剩一份）。
func TestFormatBoth_ProducesBothAndSplitsZipDirs(t *testing.T) {
	w, _ := setupWorker(t)
	seedTemplate(t, w, "tpl_b", "ver_b", "code-b")
	job := seedJob(t, w, "jb_both", "tpl_b", "ver_b", "both", 2)

	w.processJob(context.Background(), job)

	final := reloadJob(t, w, job.ID)
	if final.Status != "succeeded" || final.Succeeded != 2 {
		t.Fatalf("both 模式两条都该成功，实际 %s succeeded=%d failed=%d", final.Status, final.Succeeded, final.Failed)
	}
	for _, d := range reloadDocs(t, w, job.ID) {
		if d.DocxPath == "" || d.PdfPath == "" {
			t.Errorf("both 模式下 %s 应同时有 docx 与 pdf 路径，实际 docx=%q pdf=%q", d.ID, d.DocxPath, d.PdfPath)
		}
	}
	names := zipEntryNames(t, final.ZipPath)
	want := []string{
		"docx/第 1 份.docx", "docx/第 2 份.docx",
		"pdf/第 1 份.pdf", "pdf/第 2 份.pdf",
	}
	if strings.Join(names, "|") != strings.Join(want, "|") {
		t.Fatalf("zip 条目不对\n实际：%v\n期望：%v", names, want)
	}
}

// pdf-only：只渲染一次、只落 pdf，zip 里是平铺的 pdf 而不是 docx。
func TestFormatPDF_OnlyRendersPdf(t *testing.T) {
	w, _ := setupWorker(t)
	seedTemplate(t, w, "tpl_pdf", "ver_pdf", "code-pdf")
	job := seedJob(t, w, "jb_pdf", "tpl_pdf", "ver_pdf", "pdf", 1)
	sr := newScripted(nil)
	w.Renderer = sr

	w.processJob(context.Background(), job)

	if got := sr.formats(); len(got) != 1 || got[0] != "pdf" {
		t.Fatalf("pdf 模式只该调一次 pdf 渲染，实际调用了 %v", got)
	}
	d := reloadDocs(t, w, job.ID)[0]
	if d.DocxPath != "" || d.PdfPath == "" {
		t.Fatalf("pdf 模式不该产出 docx，实际 docx=%q pdf=%q", d.DocxPath, d.PdfPath)
	}
	names := zipEntryNames(t, reloadJob(t, w, job.ID).ZipPath)
	if len(names) != 1 || names[0] != "第 1 份.pdf" {
		t.Fatalf("zip 里应只有平铺的 pdf，实际 %v", names)
	}
}

// both 模式里 pdf 失败但 docx 成功：文档仍算成功（用户拿到的是 docx），
// 但错误信息必须写清「PDF 导出失败」，否则用户会以为 PDF 也好了。
func TestFormatBoth_PdfFailsDocxStillSucceeds(t *testing.T) {
	w, _ := setupWorker(t)
	seedTemplate(t, w, "tpl_pf", "ver_pf", "code-pf")
	job := seedJob(t, w, "jb_pdf_fail", "tpl_pf", "ver_pf", "both", 1)
	w.Renderer = newScripted(func(i int, format string) ([]byte, error) {
		if format == "pdf" {
			return nil, &render.Error{StatusCode: 500, Code: "convert_failed", Message: "LibreOffice 超时"}
		}
		return []byte("DOCX"), nil
	})

	w.processJob(context.Background(), job)

	d := reloadDocs(t, w, job.ID)[0]
	if d.Status != "succeeded" {
		t.Fatalf("docx 成功时整条不该判失败，实际 %s", d.Status)
	}
	if d.PdfPath != "" {
		t.Errorf("pdf 渲染失败却写了 pdf 路径 %q", d.PdfPath)
	}
	if !strings.Contains(d.Error, "PDF 导出失败") {
		t.Errorf("错误信息应点明 PDF 导出失败，实际 %q", d.Error)
	}
	if !strings.Contains(d.Error, "LibreOffice 超时") {
		t.Errorf("错误信息应带上渲染服务给的原因，实际 %q", d.Error)
	}
	if final := reloadJob(t, w, job.ID); final.Status != "succeeded" {
		t.Errorf("有 docx 产出时任务应为 succeeded，实际 %s", final.Status)
	}
}

// 部分失败：成功的那份进 zip，失败的那份进 errors.json，两者都不能丢。
func TestZipCarriesErrorsJSONForFailedRecords(t *testing.T) {
	w, _ := setupWorker(t)
	seedTemplate(t, w, "tpl_m", "ver_m", "code-m")
	job := seedJob(t, w, "jb_mixed", "tpl_m", "ver_m", "docx", 3)
	// 第 2 条失败：这是 batch 场景里最常见的形态（一条脏数据不该毁掉整批）。
	w.Renderer = newScripted(func(i int, format string) ([]byte, error) {
		if i == 1 {
			return nil, &render.Error{StatusCode: 422, Code: "data_invalid",
				Message: "变量 unit 类型不匹配",
				Details: []render.FieldError{{Path: "unit", Message: "应为字符串"}}}
		}
		return []byte("DOCX"), nil
	})

	w.processJob(context.Background(), job)

	final := reloadJob(t, w, job.ID)
	if final.Status != "partial" {
		t.Fatalf("3 条里失败 1 条应判 partial，实际 %s", final.Status)
	}
	if final.Succeeded != 2 || final.Failed != 1 {
		t.Fatalf("计数应为 2 成功 / 1 失败，实际 %d / %d", final.Succeeded, final.Failed)
	}

	names := zipEntryNames(t, final.ZipPath)
	if !contains(names, "errors.json") {
		t.Fatalf("部分失败时 zip 必须带 errors.json，实际条目 %v", names)
	}
	raw := readZipEntry(t, final.ZipPath, "errors.json")
	var payload struct {
		Errors []struct {
			RecordIndex int    `json:"record_index"`
			Filename    string `json:"filename"`
			Error       string `json:"error"`
			Path        string `json:"path"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("errors.json 不是合法 JSON：%v", err)
	}
	if len(payload.Errors) != 1 {
		t.Fatalf("errors.json 应有 1 条，实际 %d", len(payload.Errors))
	}
	e := payload.Errors[0]
	if e.RecordIndex != 1 {
		t.Errorf("失败的应是第 2 条（record_index=1），实际 %d", e.RecordIndex)
	}
	if !strings.Contains(e.Error, "类型不匹配") {
		t.Errorf("errors.json 里的原因应是渲染服务给的可读信息，实际 %q", e.Error)
	}
	// 字段级定位（哪个变量错了）是给调用方修正数据用的，丢了就只能靠猜。
	if e.Path != "unit" {
		t.Errorf("错误定位路径应为 unit，实际 %q", e.Path)
	}
}

// 已完成的 document 不该被重复渲染：worker 崩溃重启后重跑任务时，
// 重渲染会把已交付的文件改一遍（时间戳、内嵌图片都会变）。
func TestProcessJob_SkipsAlreadyFinishedDocs(t *testing.T) {
	w, _ := setupWorker(t)
	seedTemplate(t, w, "tpl_s", "ver_s", "code-s")
	job := seedJob(t, w, "jb_skip", "tpl_s", "ver_s", "docx", 2)
	// 把第 1 条预先置成 succeeded（模拟上次跑到一半崩了）
	w.DB.Model(&models.Document{}).Where("id = ?", "jb_skip_d0").
		Updates(map[string]interface{}{"status": "succeeded", "docx_path": "/already/done.docx"})

	sr := newScripted(nil)
	w.Renderer = sr
	w.processJob(context.Background(), job)

	if got := sr.count(); got != 1 {
		t.Fatalf("只有 1 条 queued 需要渲染，实际渲染了 %d 次", got)
	}
	docs := reloadDocs(t, w, job.ID)
	if docs[0].DocxPath != "/already/done.docx" {
		t.Errorf("已完成的产物路径被覆盖成了 %q", docs[0].DocxPath)
	}
}

// 产物目录都建不出来时，每条文档要各自失败，而不是整批静默不动。
func TestRenderOne_StorageBroken(t *testing.T) {
	w, cfg := setupWorker(t)
	seedTemplate(t, w, "tpl_st", "ver_st", "code-st")
	job := seedJob(t, w, "jb_st", "tpl_st", "ver_st", "docx", 1)
	// 让数据目录变成一个普通文件，JobDir 的 MkdirAll 必然失败
	blocker := filepath.Join(cfg.DataDir, "jobs")
	if err := os.RemoveAll(blocker); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blocker, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}

	w.processJob(context.Background(), job)

	docs := reloadDocs(t, w, job.ID)
	if docs[0].Status != "failed" {
		t.Fatalf("存储目录不可用时文档应判失败，实际 %s", docs[0].Status)
	}
	if !strings.Contains(docs[0].Error, "存储目录") {
		t.Errorf("错误信息应说明存储目录问题，实际 %q", docs[0].Error)
	}
}

// ---- 终态与过期 ----

// expires_at 必须按 RetentionDays 算，不能写死。保留期是可配置项，
// 写死会让「配了 1 天却 7 天后才清」这种不一致一直存在，且没人会去验证。
func TestFinalize_ExpiresAtFollowsRetention(t *testing.T) {
	w, cfg := setupWorker(t)
	cfg.RetentionDays = 3
	seedTemplate(t, w, "tpl_e", "ver_e", "code-e")
	job := seedJob(t, w, "jb_exp", "tpl_e", "ver_e", "docx", 1)

	w.processJob(context.Background(), job)

	final := reloadJob(t, w, job.ID)
	if final.ExpiresAt == nil {
		t.Fatal("终态必须写 expires_at，否则保留期清理永远扫不到它")
	}
	want := time.Now().AddDate(0, 0, 3)
	if d := final.ExpiresAt.Sub(want); d > 2*time.Minute || d < -2*time.Minute {
		t.Fatalf("expires_at 应约为 now+3 天，实际 %s（差 %s）", final.ExpiresAt, d)
	}
}

// 清理：过期任务删文件、清路径、打 expired；过期会话删行。
//
// 会话那条容易被忽略：cookie 到期只是浏览器不再发送，服务端的行会一直留着，
// 一年下来就是一张只涨不消的表。
func TestCleanup_ExpiresArtifactsAndSessions(t *testing.T) {
	w, _ := setupWorker(t)
	seedTemplate(t, w, "tpl_c", "ver_c", "code-c")
	job := seedJob(t, w, "jb_clean", "tpl_c", "ver_c", "docx", 1)
	w.processJob(context.Background(), job)

	final := reloadJob(t, w, job.ID)
	docs := reloadDocs(t, w, job.ID)
	docxPath, zipPath := docs[0].DocxPath, final.ZipPath
	if _, err := os.Stat(docxPath); err != nil {
		t.Fatalf("前置条件不成立，docx 不存在：%v", err)
	}

	past := time.Now().Add(-time.Hour)
	w.DB.Model(&models.Job{}).Where("id = ?", job.ID).Update("expires_at", past)
	w.DB.Create(&models.Session{TokenHash: "expired_hash", UserID: "u_1", ExpiresAt: past})
	w.DB.Create(&models.Session{TokenHash: "live_hash", UserID: "u_1", ExpiresAt: time.Now().Add(time.Hour)})

	w.cleanup(context.Background())

	if _, err := os.Stat(docxPath); !os.IsNotExist(err) {
		t.Errorf("过期后产物文件应被删除，实际 stat 返回 %v", err)
	}
	if _, err := os.Stat(zipPath); !os.IsNotExist(err) {
		t.Errorf("过期后整包应被删除，实际 stat 返回 %v", err)
	}
	after := reloadDocs(t, w, job.ID)[0]
	if !after.Expired || after.DocxPath != "" {
		t.Errorf("过期文档应打 expired 并清空路径，实际 expired=%v docx_path=%q", after.Expired, after.DocxPath)
	}
	if got := reloadJob(t, w, job.ID); !got.Expired || got.ZipPath != "" {
		t.Errorf("过期任务应打 expired 并清空 zip_path，实际 expired=%v zip=%q", got.Expired, got.ZipPath)
	}

	var expiredCnt, liveCnt int64
	w.DB.Model(&models.Session{}).Where("token_hash = ?", "expired_hash").Count(&expiredCnt)
	w.DB.Model(&models.Session{}).Where("token_hash = ?", "live_hash").Count(&liveCnt)
	if expiredCnt != 0 {
		t.Error("过期会话没有被清理")
	}
	if liveCnt != 1 {
		t.Error("未过期会话被误删了——清理条件写错了会把人踢下线")
	}
}

// ---- 纯函数与辅助 ----

func TestChangeExt(t *testing.T) {
	cases := []struct{ in, ext, want string }{
		{"审计报告.docx", ".pdf", "审计报告.pdf"},
		{"a.b.docx", ".pdf", "a.b.pdf"},      // 多点：只替最后一段
		{"noext", ".pdf", "noext.pdf"},       // 没有扩展名：追加
		{"trailingdot.", ".pdf", "trailingdot.pdf"},
		{"路径/带.点/文件.docx", ".pdf", "路径/带.点/文件.pdf"}, // 文件名里的点不误伤
	}
	for _, c := range cases {
		if got := changeExt(c.in, c.ext); got != c.want {
			t.Errorf("changeExt(%q,%q) = %q，期望 %q", c.in, c.ext, got, c.want)
		}
	}
}

func TestBuildFilename(t *testing.T) {
	rec := map[string]interface{}{"unit": "示例信息", "month": "10月"}
	got := BuildFilename("{{ unit }}-{{ month }}-{{ index }}", rec, 8, "audit-report", "pdf")
	if !strings.HasSuffix(got, ".pdf") {
		t.Errorf("扩展名应跟随 format，实际 %q", got)
	}
	if !strings.Contains(got, "示例信息") || !strings.Contains(got, "8") || !strings.Contains(got, "10月") {
		t.Errorf("规则里的三段变量都要渲染出来，实际 %q", got)
	}
	// 规则为空时用模板 code + 序号兜底，不能产出空文件名（.docx 这种）。
	if got := BuildFilename("", rec, 3, "audit-report", "docx"); got == "" || got == ".docx" {
		t.Errorf("空规则要有兜底名，实际 %q", got)
	}
}

// 签章的授权对象：走 API Key 的任务扣 Key 的次数，走页面的扣用户次数。
// 判错的后果是「用 Key 调用却扣了管理员额度」，或在 Key 额度耗尽时仍然放行。
func TestGranteeFollowsJobOrigin(t *testing.T) {
	byKey := &models.Job{ID: "j1", APIKeyID: "ak_x", CreatedByUserID: "u_x"}
	if got := granteeType(byKey); got != "api_key" {
		t.Errorf("带 APIKeyID 的任务应记 api_key，实际 %q", got)
	}
	if got := granteeID(byKey); got != "ak_x" {
		t.Errorf("授权对象应是 Key，实际 %q", got)
	}
	byUser := &models.Job{ID: "j2", CreatedByUserID: "u_y"}
	if got := granteeType(byUser); got != "user" {
		t.Errorf("页面发起的任务应记 user，实际 %q", got)
	}
	if got := granteeID(byUser); got != "u_y" {
		t.Errorf("授权对象应是用户，实际 %q", got)
	}
}

func TestIsAdmin(t *testing.T) {
	w, _ := setupWorker(t)
	w.DB.Create(&models.User{ID: "u_admin", Username: "admin", Role: "admin"})
	w.DB.Create(&models.User{ID: "u_member", Username: "member", Role: "member"})

	if !w.isAdmin("u_admin") {
		t.Error("管理员应判定为 admin")
	}
	if w.isAdmin("u_member") {
		t.Error("普通成员不该被判定为 admin")
	}
	if w.isAdmin("") || w.isAdmin("u_not_exist") {
		t.Error("空用户与不存在的用户都必须按非 admin 处理（fail-closed）")
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
