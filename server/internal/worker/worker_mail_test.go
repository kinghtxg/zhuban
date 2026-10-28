package worker

import (
	"bufio"
	"context"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"docforge/server/internal/mailer"
	"docforge/server/internal/models"
)

// 邮件这一段的测试分两层：
//
//  1. **编排逻辑**（附件怎么挑、变量怎么填、链接什么时候加）——纯函数，直接断言；
//  2. **真的发出去**（起一个假 SMTP 服务器，验证收件人/主题/附件真的到了对端）。
//
// 为什么第 2 层必须做：SendAsync 是 goroutine，错误只写进日志和 email_status。
// 只测第 1 层的话，「邮件根本没发出去」这件事在测试里完全看不出来。

// ---- 假 SMTP 服务器 ----

type fakeMsg struct {
	From  string
	Rcpts []string
	Data  string
}

type fakeSMTP struct {
	ln   net.Listener
	mu   sync.Mutex
	msgs []fakeMsg
	wg   sync.WaitGroup
}

func newFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("起假 SMTP 失败：%v", err)
	}
	f := &fakeSMTP{ln: ln}
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			f.wg.Add(1)
			go func() {
				defer f.wg.Done()
				f.serve(conn)
			}()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		f.wg.Wait()
	})
	return f
}

func (f *fakeSMTP) port() int { return f.ln.Addr().(*net.TCPAddr).Port }

func (f *fakeSMTP) received() []fakeMsg {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeMsg, len(f.msgs))
	copy(out, f.msgs)
	return out
}

func (f *fakeSMTP) serve(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	rw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
	say := func(s string) {
		_, _ = rw.WriteString(s + "\r\n")
		_ = rw.Flush()
	}
	say("220 fake ESMTP ready")

	var cur fakeMsg
	for {
		line, err := rw.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		upper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upper, "EHLO"):
			// 多行响应：textproto 按 "250-" 续行读
			say("250-fake")
			say("250-SIZE 10485760")
			say("250 OK")
		case strings.HasPrefix(upper, "HELO"):
			say("250 OK")
		case strings.HasPrefix(upper, "MAIL FROM:"):
			cur = fakeMsg{From: strings.Trim(strings.TrimSpace(line[10:]), "<>")}
			say("250 OK")
		case strings.HasPrefix(upper, "RCPT TO:"):
			cur.Rcpts = append(cur.Rcpts, strings.Trim(strings.TrimSpace(line[8:]), "<>"))
			say("250 OK")
		case upper == "DATA":
			say("354 go ahead")
			var body strings.Builder
			for {
				dl, derr := rw.ReadString('\n')
				if derr != nil {
					return
				}
				if dl == ".\r\n" || dl == ".\n" {
					break
				}
				// 还原 dot-stuffing：行首的 ".." 是被 DotWriter 加上去的
				if strings.HasPrefix(dl, "..") {
					dl = dl[1:]
				}
				body.WriteString(dl)
			}
			cur.Data = body.String()
			f.mu.Lock()
			f.msgs = append(f.msgs, cur)
			f.mu.Unlock()
			cur = fakeMsg{}
			say("250 OK queued")
		case upper == "QUIT":
			say("221 bye")
			return
		case upper == "RSET":
			cur = fakeMsg{}
			say("250 OK")
		default:
			say("250 OK")
		}
	}
}

// ---- 脚手架 ----

// mailWorker 装配一个指向假 SMTP 的 worker。
func mailWorker(t *testing.T) (*Worker, *fakeSMTP) {
	t.Helper()
	w, cfg := setupWorker(t)
	smtp := newFakeSMTP(t)
	cfg.SMTPHost = "127.0.0.1"
	cfg.SMTPPort = smtp.port()
	cfg.SMTPFrom = "noreply@example.com"
	cfg.SMTPTLS = "none"
	cfg.EmailAllowedDomains = []string{"example.com"}
	cfg.EmailMaxRecipients = 10
	cfg.EmailMaxAttachMB = 10
	cfg.EmailMaxBodyChars = 8000
	cfg.EmailTimeout = 5 * time.Second
	cfg.ExternalBaseURL = "https://zb.example.com"
	w.Mailer = mailer.New(w.DB, cfg)
	return w, smtp
}

// mailJob 造一个「已渲染完成」的任务：n 份成功文档各有真实文件，外加一个 zip。
func mailJob(t *testing.T, w *Worker, id string, n int, spec string) *models.Job {
	t.Helper()
	dir := filepath.Join(w.Cfg.DataDir, "jobs", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	zipPath := filepath.Join(dir, "result.zip")
	if err := os.WriteFile(zipPath, []byte("PK\x03\x04fake-zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	job := &models.Job{
		ID: id, TemplateID: "tpl_mail", VersionID: "ver_mail",
		Status: "succeeded", Total: n, Succeeded: n, Format: "docx",
		ZipPath: zipPath, CreatedByUserID: "u_actor",
	}
	if spec != "" {
		job.EmailSpec = models.JSONB(spec)
		job.EmailStatus = "pending"
	}
	if err := w.DB.Create(job).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		docx := filepath.Join(dir, fmt.Sprintf("d%d.docx", i))
		if err := os.WriteFile(docx, []byte("docx-bytes"), 0o644); err != nil {
			t.Fatal(err)
		}
		d := &models.Document{
			ID: fmt.Sprintf("%s_d%d", id, i), JobID: id, RecordIndex: i,
			Filename: fmt.Sprintf("报告%d.docx", i+1), Status: "succeeded",
			DocxPath: docx, InputData: models.JSONB(`{"unit":"示例信息"}`),
		}
		if err := w.DB.Create(d).Error; err != nil {
			t.Fatal(err)
		}
	}
	return job
}

// waitJobEmailStatus 等异步发信落库。SendAsync 是 goroutine，
// 不等就只能断言「没报错」，那等于没测。
func waitJobEmailStatus(t *testing.T, w *Worker, jobID, want string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		var j models.Job
		if err := w.DB.First(&j, "id = ?", jobID).Error; err == nil && j.EmailStatus == want {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	var j models.Job
	w.DB.First(&j, "id = ?", jobID)
	t.Fatalf("等 email_status=%s 超时，实际 %q（error=%s）", want, j.EmailStatus, j.EmailError)
}

// ---- 真的发出去 ----

// 任务级邮件：一封，收件人是规格里的，主题里的 {{ job.* }} 要填好，附件是整包 zip。
func TestDispatchEmails_JobLevel(t *testing.T) {
	w, smtp := mailWorker(t)
	job := mailJob(t, w, "jb_mail_job", 3,
		`{"to":["ops@example.com"],"subject":"{{ job.template_code }} 已完成 {{ job.succeeded }}/{{ job.total }}","body":"见附件","attach":"zip"}`)

	w.dispatchEmails(job)
	waitJobEmailStatus(t, w, job.ID, "sent")

	msgs := smtp.received()
	if len(msgs) != 1 {
		t.Fatalf("任务级规格应只发 1 封，实际 %d 封", len(msgs))
	}
	m := msgs[0]
	if len(m.Rcpts) != 1 || m.Rcpts[0] != "ops@example.com" {
		t.Errorf("收件人应为 ops@example.com，实际 %v", m.Rcpts)
	}
	// 主题里有中文时整体会被 RFC 2047 编码，不能直接在报文里搜明文。
	subj := decodeSubject(t, m.Data)
	if !strings.Contains(subj, "3/3") {
		t.Errorf("主题里的 {{ job.succeeded }}/{{ job.total }} 没填上，解码后主题是 %q", subj)
	}
	// 模板不存在时 templateCode 回落到 ID，主题里应出现该回落值
	if !strings.Contains(subj, "tpl_mail") {
		t.Errorf("主题里应含模板编号（缺失时用 ID 回落），解码后是 %q", subj)
	}
	// 附件名是「模板编号-任务尾号.zip」，模板不存在时回落到 templateID
	if !strings.Contains(m.Data, ".zip") {
		t.Errorf("整包邮件应带 zip 附件，实际报文里没有附件名")
	}
	if strings.Contains(m.Data, "{{") {
		t.Errorf("报文里残留未替换的占位符：%s", head(m.Data, 600))
	}
}

// 每份一封：3 份成功文档 → 每个带各自 EmailSpec 的发一封。
func TestDispatchEmails_PerDocument(t *testing.T) {
	w, smtp := mailWorker(t)
	job := mailJob(t, w, "jb_mail_doc", 3, "")

	// 只给第 1、3 份配规格，第 2 份不配 → 只该发 2 封
	w.DB.Model(&models.Document{}).Where("id = ?", "jb_mail_doc_d0").Updates(map[string]interface{}{
		"email_spec":   models.JSONB(`{"to":["a@example.com"],"subject":"第 {{ filename }} 份","attach":"docx"}`),
		"email_status": "pending",
	})
	w.DB.Model(&models.Document{}).Where("id = ?", "jb_mail_doc_d2").Updates(map[string]interface{}{
		"email_spec":   models.JSONB(`{"to":["c@example.com"],"attach":"none"}`),
		"email_status": "pending",
	})

	w.dispatchEmails(job)

	// 任务级没有规格，job.email_status 不会变；要等的是文档上的状态。
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		var sent int64
		w.DB.Model(&models.Document{}).Where("job_id = ? AND email_status = ?", job.ID, "sent").Count(&sent)
		if sent >= 2 {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}

	msgs := smtp.received()
	if len(msgs) != 2 {
		t.Fatalf("只有 2 份配了规格，应发 2 封，实际 %d 封（收件人：%v）", len(msgs), rcptsOf(msgs))
	}
	got := map[string]bool{}
	for _, m := range msgs {
		for _, r := range m.Rcpts {
			got[r] = true
		}
	}
	if !got["a@example.com"] || !got["c@example.com"] {
		t.Errorf("两封应分别发给 a@ 与 c@，实际 %v", rcptsOf(msgs))
	}
}

// 全失败时不发信，并把所有 pending 置 skipped。
//
// 一封没有任何附件的「你的文档已生成」比不发更糟：收件人会去追问附件在哪。
func TestDispatchEmails_AllFailedMarksSkipped(t *testing.T) {
	w, smtp := mailWorker(t)
	job := mailJob(t, w, "jb_mail_fail", 2,
		`{"to":["ops@example.com"],"subject":"x","attach":"zip"}`)
	// 把两份都改成失败，任务计数也改成 0 成功
	w.DB.Model(&models.Document{}).Where("job_id = ?", job.ID).Updates(map[string]interface{}{"status": "failed"})
	job.Succeeded = 0
	job.Failed = 2
	w.DB.Model(&models.Job{}).Where("id = ?", job.ID).Updates(map[string]interface{}{"succeeded": 0, "failed": 2})

	w.dispatchEmails(job)

	if n := len(smtp.received()); n != 0 {
		t.Fatalf("全失败不该发信，实际发了 %d 封", n)
	}
	var j models.Job
	w.DB.First(&j, "id = ?", job.ID)
	if j.EmailStatus != "skipped" {
		t.Errorf("全失败应把 email_status 置 skipped，实际 %q", j.EmailStatus)
	}
	if !strings.Contains(j.EmailError, "跳过发信") {
		t.Errorf("应写明为什么没发，实际 %q", j.EmailError)
	}
	var pendingDoc int64
	w.DB.Model(&models.Document{}).Where("job_id = ? AND email_status = ?", job.ID, "pending").Count(&pendingDoc)
	if pendingDoc != 0 {
		t.Errorf("文档上的 pending 也要一起收尾，还剩 %d 条", pendingDoc)
	}
}

// 没配 SMTP 的部署：整条链路静默跳过，不能因为邮件功能缺失就影响任务终态。
func TestDispatchEmails_NoMailerIsNoop(t *testing.T) {
	w, _ := setupWorker(t)
	job := mailJob(t, w, "jb_mail_none", 1, `{"to":["ops@example.com"]}`)
	w.Mailer = nil
	w.dispatchEmails(job) // 不 panic 即可
	if j := reloadJob(t, w, job.ID); j.EmailStatus != "pending" {
		t.Errorf("没有发信器时不该改动 email_status，实际 %q", j.EmailStatus)
	}
}

// ---- 附件怎么挑 ----

// auto 的核心规则：一份就带那份，多份才带 zip。
// 收件人拿到一个装着单个文件的压缩包，多半会以为系统出错了。
func TestJobAttachments_AutoRules(t *testing.T) {
	w, _ := mailWorker(t)
	job := mailJob(t, w, "jb_att", 2, "")
	docs := reloadDocs(t, w, job.ID)

	one := w.jobAttachments(job, docs[:1], mailer.AttachAuto, "x.zip")
	if len(one) != 1 || !strings.HasSuffix(one[0].Filename, ".docx") {
		t.Errorf("单份时 auto 应直接带那份 docx，实际 %v", namesOf(one))
	}

	many := w.jobAttachments(job, docs, mailer.AttachAuto, "整包.zip")
	if len(many) != 1 || many[0].Filename != "整包.zip" {
		t.Errorf("多份时 auto 应带整包 zip，实际 %v", namesOf(many))
	}

	if got := w.jobAttachments(job, docs, mailer.AttachNone, "x.zip"); got != nil {
		t.Errorf("none 模式不该带附件，实际 %v", namesOf(got))
	}

	// 显式指定 pdf 但有多份：全部按 pdf 带上，不擅自换成 zip
	pdfs := w.jobAttachments(job, docs, mailer.AttachPDF, "x.zip")
	for _, a := range pdfs {
		if !strings.HasSuffix(a.Filename, ".pdf") {
			t.Errorf("显式 pdf 模式不该混进其他格式，实际 %v", namesOf(pdfs))
		}
	}
}

func TestDocAttachment_Modes(t *testing.T) {
	w, _ := mailWorker(t)
	job := mailJob(t, w, "jb_att2", 1, "")
	d := reloadDocs(t, w, job.ID)[0]

	if got := w.docAttachment(&d, mailer.AttachNone); got != nil {
		t.Errorf("none 不该带附件，实际 %v", namesOf(got))
	}
	if got := w.docAttachment(&d, mailer.AttachDocx); len(got) != 1 || !strings.HasSuffix(got[0].Filename, ".docx") {
		t.Errorf("docx 模式应只带 docx，实际 %v", namesOf(got))
	}
	// 该文档没有 pdf：不能凭空造一个空附件
	if got := w.docAttachment(&d, mailer.AttachPDF); len(got) != 0 {
		t.Errorf("没有 pdf 时不该带附件，实际 %v", namesOf(got))
	}
	// 文件被清理：跳过而不是塞空附件
	missing := d
	missing.DocxPath = filepath.Join(w.Cfg.DataDir, "not-there.docx")
	if got := w.docAttachment(&missing, mailer.AttachDocx); len(got) != 0 {
		t.Errorf("产物文件不存在时应跳过附件，实际 %v", namesOf(got))
	}
}

// 只有 PDF 的文档，直链必须带 ?format=pdf，否则下载接口会回「未生成 docx」。
func TestDocToken_PdfOnlyAppendsFormat(t *testing.T) {
	w, _ := mailWorker(t)
	pdfOnly := &models.Document{ID: "dc_pdf", PdfPath: "/x/a.pdf"}
	if tok := w.docToken(pdfOnly); !strings.HasSuffix(tok, "?format=pdf") {
		t.Errorf("只有 pdf 时直链要带 format=pdf，实际 %q", tok)
	}
	both := &models.Document{ID: "dc_both", DocxPath: "/x/a.docx", PdfPath: "/x/a.pdf"}
	if tok := w.docToken(both); strings.Contains(tok, "format=") {
		t.Errorf("有 docx 时不该带 format 参数，实际 %q", tok)
	}
}

// 单份给文档直链，多份给整包直链。
func TestJobLink_SingleVsBatch(t *testing.T) {
	w, _ := mailWorker(t)
	job := mailJob(t, w, "jb_link", 2, "")
	docs := reloadDocs(t, w, job.ID)
	base := "https://zb.example.com"

	if got := w.jobLink(job, docs[:1], base); !strings.HasPrefix(got, base+"/api/v1/s/") {
		t.Errorf("单份应是文档直链，实际 %q", got)
	}
	// 两个链接必须不同：都指向同一个 token 说明单份/多份分支写反了
	if a, b := w.jobLink(job, docs[:1], base), w.jobLink(job, docs, base); a == b {
		t.Errorf("单份与多份的直链相同（%q），分支可能写反了", a)
	}
}

// ---- 纯函数 ----

func TestAppendLink(t *testing.T) {
	link := "https://zb.example.com/api/v1/s/abc"

	if got := appendLink("正文", link); !strings.Contains(got, link) || !strings.Contains(got, "下载链接") {
		t.Errorf("应把链接追加到正文末尾，实际 %q", got)
	}
	// 没配 ExternalBaseURL 时不追加：印一条 "http:///api/v1/s/xxx" 比不给链接更糟
	if got := appendLink("正文", "/api/v1/s/abc"); got != "正文" {
		t.Errorf("相对链接不该被追加，实际 %q", got)
	}
	if got := appendLink("正文", ""); got != "正文" {
		t.Errorf("空链接不该改动正文，实际 %q", got)
	}
	// 正文里已经有这条链接：不要重复追加
	if got := appendLink("点这里 "+link, link); strings.Count(got, link) != 1 {
		t.Errorf("链接已存在时不该重复追加，实际 %q", got)
	}
}

func TestDefaultSubject(t *testing.T) {
	if got := defaultSubject("   ", "审计报告"); got != "审计报告" {
		t.Errorf("空主题要回落到默认值，实际 %q", got)
	}
	if got := defaultSubject("自定义", "审计报告"); got != "自定义" {
		t.Errorf("有主题时不该被覆盖，实际 %q", got)
	}
}

func TestStripExtAndShortID(t *testing.T) {
	cases := map[string]string{
		"审计报告.docx": "审计报告",
		"a.b.pdf":   "a.b",
		"noext":     "noext",
		"路径/文件.docx": "路径/文件",
	}
	for in, want := range cases {
		if got := stripExt(in); got != want {
			t.Errorf("stripExt(%q) = %q，期望 %q", in, got, want)
		}
	}
	if got := shortID("jb_20261028_0012"); got != "028_0012" {
		t.Errorf("shortID 应取尾 8 位，实际 %q", got)
	}
	if got := shortID("short"); got != "short" {
		t.Errorf("短 ID 应原样返回，实际 %q", got)
	}
}

func TestDocDataMergesInputAndMeta(t *testing.T) {
	d := &models.Document{ID: "d1", Filename: "报告.docx", RecordIndex: 3,
		InputData: models.JSONB(`{"unit":"示例","amount":12}`)}
	got := docData(d)
	if got["unit"] != "示例" || got["amount"] != float64(12) {
		t.Errorf("业务字段要合并进来，实际 %v", got)
	}
	if got["filename"] != "报告.docx" || got["record_index"] != 3 {
		t.Errorf("filename / record_index 必须注入，实际 %v", got)
	}
	// 坏 JSON 不能 panic，也不能让整个任务失败
	if m := docData(&models.Document{InputData: models.JSONB(`{bad`)}); m == nil {
		t.Error("坏 JSON 应返回空 map 而不是 nil")
	}
}

func TestDecodeSpec(t *testing.T) {
	if _, ok := decodeSpec(models.JSONB(``)); ok {
		t.Error("空规格应判为「不发」")
	}
	if _, ok := decodeSpec(models.JSONB(`null`)); ok {
		t.Error("null 应判为「不发」")
	}
	if _, ok := decodeSpec(models.JSONB(`{"to":[]}`)); ok {
		t.Error("没有收件人等于不发")
	}
	if _, ok := decodeSpec(models.JSONB(`{bad json`)); ok {
		t.Error("坏 JSON 应跳过而不是发一封空信")
	}
	spec, ok := decodeSpec(models.JSONB(`{"to":["a@b.com"]}`))
	if !ok || len(spec.To) != 1 {
		t.Errorf("合法规格应被解析出来，实际 ok=%v spec=%+v", ok, spec)
	}
}

func TestTemplateCodeAndNameFallback(t *testing.T) {
	w, _ := mailWorker(t)
	w.DB.Create(&models.Template{ID: "tpl_known", Code: "audit-report", Name: "代码审计报告"})

	if got := w.templateCode("tpl_known"); got != "audit-report" {
		t.Errorf("应按模板取 code，实际 %q", got)
	}
	if got := w.templateName("tpl_known"); got != "代码审计报告" {
		t.Errorf("应按模板取 name，实际 %q", got)
	}
	// 模板被删了：回落到 ID 与「文档」，不能返回空串（空串会变成空文件名/空主题）
	if got := w.templateCode("tpl_deleted"); got != "tpl_deleted" {
		t.Errorf("模板缺失时应回落到 ID，实际 %q", got)
	}
	if got := w.templateName("tpl_deleted"); got != "文档" {
		t.Errorf("模板缺失时名字应回落为「文档」，实际 %q", got)
	}
	if got := w.templateCode(""); got != "documents" {
		t.Errorf("连 ID 都没有时应回落为 documents，实际 %q", got)
	}
}

// actorName 决定审计里的「调用人」。走 API Key 的任务记 Key 名，
// 这样「这把 Key 发了什么信」能按 Key 追溯。
func TestActorName(t *testing.T) {
	w, _ := mailWorker(t)
	w.DB.Create(&models.User{ID: "u_named", Username: "zhangsan", DisplayName: "张三"})
	w.DB.Create(&models.User{ID: "u_plain", Username: "lisi"})
	w.DB.Create(&models.APIKey{ID: "ak_1", UserID: "u_named", Name: "报表机器人", KeyHash: "h"})

	if got := w.actorName(&models.Job{APIKeyID: "ak_1", CreatedByUserID: "u_named"}); got != "报表机器人" {
		t.Errorf("走 Key 的任务应记 Key 名，实际 %q", got)
	}
	if got := w.actorName(&models.Job{CreatedByUserID: "u_named"}); got != "张三" {
		t.Errorf("有显示名时优先用显示名，实际 %q", got)
	}
	if got := w.actorName(&models.Job{CreatedByUserID: "u_plain"}); got != "lisi" {
		t.Errorf("没有显示名时用登录名，实际 %q", got)
	}
	if got := w.actorName(&models.Job{}); got != "" {
		t.Errorf("匿名任务没有调用人，实际 %q", got)
	}
}

// ---- 小工具 ----

func head(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// decodeSubject 解出报文里的 Subject 头。
//
// 必须解码：含中文的主题会被整体做 RFC 2047 编码（=?UTF-8?B?...?=），
// 直接在原始报文里搜明文永远搜不到，测试会得到一个假失败。
func decodeSubject(t *testing.T, raw string) string {
	t.Helper()
	msg, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("报文不是合法 MIME：%v（前 300 字节：%s）", err, head(raw, 300))
	}
	v := msg.Header.Get("Subject")
	dec := &mime.WordDecoder{}
	if out, derr := dec.DecodeHeader(v); derr == nil {
		return out
	}
	return v
}

func namesOf(atts []mailer.Attachment) []string {
	out := make([]string, 0, len(atts))
	for _, a := range atts {
		out = append(out, a.Filename)
	}
	return out
}

func rcptsOf(msgs []fakeMsg) []string {
	var out []string
	for _, m := range msgs {
		out = append(out, m.Rcpts...)
	}
	return out
}

var _ = context.Background
