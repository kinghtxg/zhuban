package mailer

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"

	"docforge/server/internal/auditx"
	"docforge/server/internal/config"
	"docforge/server/internal/models"
	"docforge/server/internal/store"
)

// 这轮测试的核心不是"覆盖了多少行"，而是把几条**错了就会出事故**的规则钉死：
//   - 中文主题/附件名不编码 → 收件人看到乱码
//   - BCC 写进信头 → 密送名单被公开
//   - base64 不折行 → 被中间网关截断
//   - 白名单为空却放行 → 本系统变成开放中继
//   - 变量找不到却原样保留 → {{ 变量 }} 字面印进客户邮件
// 这些都由假 SMTP 服务器在协议层验证，不依赖外网。

// ------------------------------------------------------------------ 假 SMTP

// fakeSMTP 一台内存里的 SMTP 服务器。
//
// 自己写而不连真实邮件服务，是因为这条链路的正确性不该依赖外网可达，
// 也不该在 CI 里往外发信。握手每一步的返回码都由我们控制，才能断言
// 「客户端确实是按协议发的」。
type fakeSMTP struct {
	ln net.Listener

	mu       sync.Mutex
	gotAuth  string
	gotUser  string
	gotPass  string
	gotFrom  string
	gotRcpts []string
	gotData  string
	sessions int

	rejectPass bool
	noAuth     bool
}

func newFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln}
	go f.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return f
}

func (f *fakeSMTP) host() string { return "127.0.0.1" }
func (f *fakeSMTP) port() int    { return f.ln.Addr().(*net.TCPAddr).Port }

func (f *fakeSMTP) serve() {
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return
		}
		f.mu.Lock()
		f.sessions++
		f.mu.Unlock()
		go f.handle(conn)
	}
}

// handle 按 SMTP 状态机应答。AUTH LOGIN 是三步握手，用会话内的 loginStep 记阶段。
func (f *fakeSMTP) handle(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	tp := textproto.NewConn(conn)

	write := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
	write("220 fake ESMTP ready")

	var body strings.Builder
	inData := false
	loginStep := 0 // 1=等用户名 2=等密码

	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		if inData {
			if line == "." {
				inData = false
				f.mu.Lock()
				f.gotData = body.String()
				f.mu.Unlock()
				write("250 2.0.0 Ok: queued")
				continue
			}
			if strings.HasPrefix(line, "..") { // 还原 dot-stuffing
				line = line[1:]
			}
			body.WriteString(line + "\n")
			continue
		}

		// AUTH LOGIN 后续两步是裸 base64，不是命令字
		if loginStep > 0 {
			b, e := base64.StdEncoding.DecodeString(strings.TrimSpace(line))
			if e != nil {
				write("501 5.5.2 base64 解码失败")
				return
			}
			f.mu.Lock()
			if loginStep == 1 {
				f.gotUser = string(b)
				loginStep = 2
			} else {
				f.gotPass = string(b)
				loginStep = 0
			}
			bad := f.rejectPass
			f.mu.Unlock()
			if loginStep == 2 {
				write("334 UGFzc3dvcmQ6")
				continue
			}
			if bad {
				write("535 5.7.8 Authentication credentials invalid")
			} else {
				write("235 2.7.0 Authentication successful")
			}
			continue
		}

		up := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(up, "EHLO"):
			write("250-fake greets you")
			if !f.noAuth {
				write("250-AUTH LOGIN PLAIN")
			}
			write("250 SIZE 10485760")
		case strings.HasPrefix(up, "HELO"):
			write("250 fake")
		case up == "AUTH LOGIN":
			f.mu.Lock()
			f.gotAuth = "LOGIN"
			f.mu.Unlock()
			loginStep = 1
			write("334 VXNlcm5hbWU6")
		case strings.HasPrefix(up, "AUTH PLAIN"):
			f.mu.Lock()
			f.gotAuth = "PLAIN"
			if b, e := base64.StdEncoding.DecodeString(strings.TrimSpace(line[len("AUTH PLAIN"):])); e == nil {
				if p := strings.Split(string(b), "\x00"); len(p) == 3 {
					f.gotUser, f.gotPass = p[1], p[2]
				}
			}
			bad := f.rejectPass
			f.mu.Unlock()
			if bad {
				write("535 5.7.8 Authentication credentials invalid")
			} else {
				write("235 2.7.0 Authentication successful")
			}
		case strings.HasPrefix(up, "MAIL FROM"):
			f.mu.Lock()
			f.gotFrom = extractAddr(line)
			f.mu.Unlock()
			write("250 2.1.0 Ok")
		case strings.HasPrefix(up, "RCPT TO"):
			f.mu.Lock()
			f.gotRcpts = append(f.gotRcpts, extractAddr(line))
			f.mu.Unlock()
			write("250 2.1.5 Ok")
		case up == "DATA":
			inData = true
			body.Reset()
			write("354 End data with <CR><LF>.<CR><LF>")
		case up == "QUIT":
			write("221 2.0.0 Bye")
			return
		default:
			write("502 5.5.2 Command not implemented")
		}
	}
}

func extractAddr(line string) string {
	if i := strings.Index(line, "<"); i >= 0 {
		if j := strings.LastIndex(line, ">"); j > i {
			return line[i+1 : j]
		}
	}
	f := strings.Fields(line)
	if len(f) > 0 {
		return f[len(f)-1]
	}
	return line
}

func (f *fakeSMTP) cfg() Config {
	return Config{Host: f.host(), Port: f.port(), TLSMode: TLSNone, Timeout: 5 * time.Second}
}

func (f *fakeSMTP) snapshot() (auth, user, pass, from string, rcpts []string, data string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gotAuth, f.gotUser, f.gotPass, f.gotFrom, append([]string{}, f.gotRcpts...), f.gotData
}

func (f *fakeSMTP) sessionCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sessions
}

func build(t *testing.T, m Message) []byte {
	t.Helper()
	raw, err := buildMIME("noreply@example.com", m, time.Now(), newMessageID("noreply@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// ------------------------------------------------------------------ MIME

func TestBuildMIME_ChineseSubjectEncoded(t *testing.T) {
	s := string(build(t, Message{
		To: []string{"a@example.com"}, Subject: "您的对账单已生成", Body: "正文",
	}))
	if strings.Contains(s, "Subject: 您的对账单已生成") {
		t.Fatal("中文主题未做 RFC 2047 编码，网关会显示乱码")
	}
	if !strings.Contains(s, "=?utf-8?b?") && !strings.Contains(s, "=?UTF-8?B?") {
		t.Fatal("主题缺少 RFC 2047 编码头")
	}
}

func TestBuildMIME_AttachmentFilenameDualEncoded(t *testing.T) {
	s := string(build(t, Message{
		To: []string{"a@example.com"}, Subject: "s", Body: "b",
		Attach: []Attachment{{Filename: "对账单-2026年10月.pdf", Content: []byte("%PDF-1.4 fake")}},
	}))
	// 老客户端读 filename=（RFC 2047），新客户端读 filename*（RFC 2231）。只给一种，
	// 总有一半客户端看到乱码文件名。
	if !strings.Contains(s, "filename=") {
		t.Fatal("附件头缺少 filename=")
	}
	if !strings.Contains(s, "filename*=UTF-8''") {
		t.Fatal("附件头缺少 RFC 2231 的 filename*")
	}
	if !strings.Contains(s, "application/pdf") {
		t.Fatal("PDF 附件 MIME 类型不对")
	}
	if !strings.Contains(s, "base64") {
		t.Fatal("附件缺少 Content-Transfer-Encoding: base64")
	}
}

func TestBuildMIME_LinesWithinLimits(t *testing.T) {
	s := string(build(t, Message{
		To: []string{"a@example.com"}, Subject: "s",
		Body: strings.Repeat("这份合同共计一百二十八字用于测试折行行为。", 400),
	}))
	for _, line := range strings.Split(s, "\r\n") {
		if len(line) > 998 {
			t.Fatalf("存在超长行（%d 字符），RFC 5321 上限 998", len(line))
		}
	}
	idx := strings.Index(s, "\r\n\r\n")
	if idx < 0 {
		t.Fatal("信头与信体之间缺少空行分隔")
	}
	for _, line := range strings.Split(s[idx+4:], "\r\n") {
		if strings.HasPrefix(line, "--") || strings.Contains(line, "Content-") {
			continue
		}
		if len(line) > 76 {
			t.Fatalf("base64 数据行未折行：%d 字符（RFC 2045 要求 ≤76）", len(line))
		}
	}
}

func TestBuildMIME_BCCHiddenFromHeader(t *testing.T) {
	s := string(build(t, Message{
		To: []string{"a@example.com"}, BCC: []string{"secret@example.com"},
		Subject: "s", Body: "b",
	}))
	if strings.Contains(s, "secret@example.com") {
		t.Fatal("BCC 出现在信头/信体里，等于把密送名单公开给所有收件人")
	}
}

func TestNewMessageID_UniquePerCall(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		id := newMessageID("noreply@example.com")
		if seen[id] {
			t.Fatalf("Message-ID 重复：%s（网关会判成重复投递直接丢弃）", id)
		}
		seen[id] = true
	}
	if !strings.HasSuffix(newMessageID("noreply@example.com"), "@example.com>") {
		t.Fatal("Message-ID 应带发件域名")
	}
}

func TestMessageAllRecipients_IncludesBCC(t *testing.T) {
	m := Message{To: []string{"a@e.com"}, CC: []string{"c@e.com"}, BCC: []string{"b@e.com"}}
	all := m.AllRecipients()
	if len(all) != 3 {
		t.Fatalf("信封收件人应含 BCC，实得 %v", all)
	}
	if m.TotalBytes() != 0 {
		t.Fatal("无附件时字节数应为 0")
	}
	m.Attach = []Attachment{{Content: make([]byte, 1024)}}
	if m.TotalBytes() != 1024 {
		t.Fatalf("附件字节数统计不对：%d", m.TotalBytes())
	}
}

// ------------------------------------------------------------------ 规格

func TestSpecValidate(t *testing.T) {
	cases := []struct {
		name string
		spec EmailSpec
		want string
	}{
		{"收件人为空", EmailSpec{Subject: "s"}, "email.to 不能为空"},
		{"地址缺@", EmailSpec{To: []string{"zhangsan"}}, "收件人地址不合法"},
		{"附件模式非法", EmailSpec{To: []string{"a@b.com"}, Attach: "rar"}, "email.attach 取值非法"},
		{"主题过长", EmailSpec{To: []string{"a@b.com"}, Subject: strings.Repeat("x", 201)}, "email.subject 过长"},
		{"正文过长", EmailSpec{To: []string{"a@b.com"}, Body: strings.Repeat("x", 8001)}, "email.body 过长"},
		{"收件人空值", EmailSpec{To: []string{"a@b.com", "  "}}, "收件人含空值"},
	}
	for _, c := range cases {
		if err := c.spec.Validate(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s：期望含 %q，实得 %v", c.name, c.want, err)
		}
	}
	for _, mode := range []string{"", AttachAuto, AttachDocx, AttachPDF, AttachBoth, AttachZip, AttachNone} {
		if err := (EmailSpec{To: []string{"a@b.com"}, Attach: mode}).Validate(); err != nil {
			t.Fatalf("合法附件模式 %q 被拒：%v", mode, err)
		}
	}
}

func TestSpecRender_FillsAndBlanks(t *testing.T) {
	spec := EmailSpec{
		To:      []string{"{{ contact.email }}"},
		Subject: "{{ company }} 的{{ title }}",
		Body:    "您好 {{ contact.name }}，金额 {{ amount }} 元，备注 {{ missing.field }}",
	}
	data := map[string]interface{}{
		"company": "示例信息", "title": "对账单", "amount": 12800,
		"contact": map[string]interface{}{"name": "张三", "email": "zs@example.com"},
	}
	out, missing := Render(spec, data)
	if out.To[0] != "zs@example.com" {
		t.Fatalf("收件人变量未替换：%v", out.To)
	}
	if out.Subject != "示例信息 的对账单" {
		t.Fatalf("主题渲染结果不对：%q", out.Subject)
	}
	if !strings.Contains(out.Body, "您好 张三") || !strings.Contains(out.Body, "金额 12800 元") {
		t.Fatalf("正文渲染结果不对：%q", out.Body)
	}
	// 找不到的变量置空，绝不能把 {{ missing.field }} 字面印进客户邮件。
	if strings.Contains(out.Body, "{{") {
		t.Fatalf("占位符原样留在正文里：%q", out.Body)
	}
	if len(missing) != 1 || missing[0] != "missing.field" {
		t.Fatalf("缺失变量上报不对：%v", missing)
	}
}

func TestSpecRender_ArrayIndex(t *testing.T) {
	spec := EmailSpec{To: []string{"a@b.com"}, Subject: "{{ orders.0.name }}/{{ orders.1.name }}"}
	data := map[string]interface{}{"orders": []interface{}{
		map[string]interface{}{"name": "一单"},
		map[string]interface{}{"name": "二单"},
	}}
	out, _ := Render(spec, data)
	if out.Subject != "一单/二单" {
		t.Fatalf("数组下标取值不对：%q", out.Subject)
	}
}

func TestSpecRender_NoJinjaSyntax(t *testing.T) {
	// 刻意不支持过滤器/循环/if：邮件模板只需要"把变量填进去"。
	// 引入完整模板引擎会让调用方能写的东西远超我们愿意维护的范围。
	spec := EmailSpec{To: []string{"a@b.com"}, Subject: "{{ name | upper }}"}
	out, _ := Render(spec, map[string]interface{}{"name": "tom"})
	if strings.Contains(out.Subject, "TOM") {
		t.Fatal("不该支持 Jinja 过滤器")
	}
}

func TestSpec_EmptyAndLink(t *testing.T) {
	if !(EmailSpec{}).Empty() {
		t.Fatal("空规格应判定为不发信")
	}
	if !(EmailSpec{}).WantsLink() {
		t.Fatal("include_link 未指定时默认应带下载链接")
	}
	f := false
	if (EmailSpec{IncludeLink: &f}).WantsLink() {
		t.Fatal("显式 false 时不该带链接")
	}
}

// ------------------------------------------------------------------ 收件人

func TestCheckRecipients_FailClosedWhenNoWhitelist(t *testing.T) {
	c := Config{MaxRecipients: 10}
	err := c.CheckRecipients([]string{"someone@evil.com"}, nil, nil)
	if err == nil {
		t.Fatal("白名单为空时必须拒绝（fail-closed）：否则本系统就是一台开放中继")
	}
	if _, ok := err.(*ErrDomainNotAllowed); !ok {
		t.Fatalf("错误类型不对：%T", err)
	}
	// 管理端「测试邮件」旁路白名单：运维自己验链路时不该被自己还没填完的白名单挡住。
	if err := c.CheckRecipientsLoose([]string{"someone@evil.com"}, nil, nil); err != nil {
		t.Fatalf("测试邮件不该被白名单挡：%v", err)
	}
}

func TestCheckRecipients_WhitelistAndLimit(t *testing.T) {
	c := Config{MaxRecipients: 2, AllowedDomains: []string{"example.com", ".corp.cn"}}
	if err := c.CheckRecipients([]string{"a@example.com"}, []string{"b@mail.corp.cn"}, nil); err != nil {
		t.Fatalf("合法收件人被拒：%v", err)
	}
	if err := c.CheckRecipients([]string{"a@other.com"}, nil, nil); err == nil {
		t.Fatal("名单外域名应被拒")
	}
	if err := c.CheckRecipients([]string{"a@e.com", "b@e.com", "c@e.com"}, nil, nil); err == nil {
		t.Fatal("超出收件人上限应被拒")
	}
	if err := c.CheckRecipients([]string{"张三 <a@example.com>"}, nil, nil); err != nil {
		t.Fatalf("显示名形式应被接受：%v", err)
	}
	if err := c.CheckRecipients([]string{"not-an-email"}, nil, nil); err == nil {
		t.Fatal("无 @ 的地址应被拒")
	}
	if err := c.CheckRecipients(nil, nil, nil); err == nil {
		t.Fatal("收件人为空应被拒")
	}
}

func TestDomainAllowed_SuffixMatch(t *testing.T) {
	cases := []struct {
		domain string
		allow  []string
		want   bool
	}{
		{"example.com", []string{"example.com"}, true},
		{"mail.example.com", []string{".example.com"}, true},
		{"example.com", []string{".example.com"}, true},
		{"notexample.com", []string{".example.com"}, false},
		{"a.com", nil, false},
	}
	for _, c := range cases {
		if got := domainAllowed(c.domain, c.allow); got != c.want {
			t.Fatalf("domainAllowed(%q,%v)=%v，期望 %v", c.domain, c.allow, got, c.want)
		}
	}
}

// ------------------------------------------------------------------ 配置

func TestNormalizeTLS_ByPort(t *testing.T) {
	cases := []struct {
		mode string
		port int
		want string
	}{
		{"", 465, TLSDirect},
		{"", 587, TLSUpgrade},
		{"", 25, TLSUpgrade},
		{"none", 587, TLSNone},
		{"TLS", 25, TLSDirect},
		{"STARTTLS", 25, TLSUpgrade},
	}
	for _, c := range cases {
		if got := normalizeTLS(c.mode, c.port); got != c.want {
			t.Fatalf("normalizeTLS(%q,%d)=%q，期望 %q", c.mode, c.port, got, c.want)
		}
	}
}

func TestSenderLoad_DBOverridesEnv(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		DatabaseURL: "sqlite://" + dir + "/t.db", DataDir: dir,
		SMTPHost: "env.example.com", SMTPPort: 587,
		SMTPUser: "env@example.com", SMTPPass: "envpass",
		EmailAllowedDomains: []string{"env.com"},
	}
	db, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	if err := db.AutoMigrate(models.AllModels()...); err != nil {
		t.Fatal(err)
	}
	s := New(db, cfg)

	base := s.Load()
	if base.Host != "env.example.com" || base.User != "env@example.com" || base.Pass != "envpass" {
		t.Fatalf("env 兜底未生效：%+v", base)
	}
	if err := s.Save(map[string]string{
		KeyHost: "db.example.com", KeyUser: "db@example.com", KeyPass: "dbpass",
		KeyPort: "465", KeyTLS: "tls", KeyFrom: "noreply@example.com",
		KeyAllowedDomains: "db.com, .branch.cn",
	}); err != nil {
		t.Fatal(err)
	}
	got := s.Load()
	if got.Host != "db.example.com" || got.User != "db@example.com" || got.Pass != "dbpass" {
		t.Fatalf("DB 未覆盖 env：%+v", got)
	}
	if got.Port != 465 || got.TLSMode != TLSDirect {
		t.Fatalf("端口/TLS 推导不对：port=%d tls=%s", got.Port, got.TLSMode)
	}
	if got.EffectiveFrom() != "noreply@example.com" {
		t.Fatalf("发件人不对：%q", got.EffectiveFrom())
	}
	if len(got.AllowedDomains) != 2 || got.AllowedDomains[1] != ".branch.cn" {
		t.Fatalf("白名单解析不对：%v", got.AllowedDomains)
	}
	all := s.ReadAll()
	if all[KeyHost] != "db.example.com" || all[KeyPort] != "465" {
		t.Fatalf("ReadAll 回读不对：%v", all)
	}
	// 密码允许被显式清空（改回走 env）
	if err := s.Save(map[string]string{KeyPass: ""}); err != nil {
		t.Fatal(err)
	}
	if s.Load().Pass != "" {
		t.Fatal("密码显式清空后仍读到旧值")
	}
}

func TestConfigEffectiveFrom(t *testing.T) {
	if got := (Config{From: "server@example.com", User: "login@example.com"}).EffectiveFrom(); got != "server@example.com" {
		t.Fatalf("应优先取 smtp_from：%q", got)
	}
	if got := (Config{User: "login@example.com"}).EffectiveFrom(); got != "login@example.com" {
		t.Fatalf("未配 from 时应回退到登录账号：%q", got)
	}
	if (Config{}).Enabled() {
		t.Fatal("主机为空时不应判定为可用")
	}
	if !(Config{Host: "x"}).Enabled() {
		t.Fatal("配了主机就该可用")
	}
}

// ------------------------------------------------------------------ 端到端

func TestDeliver_AuthLoginEndToEnd(t *testing.T) {
	f := newFakeSMTP(t)
	c := f.cfg()
	c.User, c.Pass = "ops@example.com", "secret"

	raw := []byte("Subject: 测试\r\n\r\n正文\r\n")
	if err := deliver(c, "noreply@example.com", []string{"a@example.com", "b@example.com"}, raw); err != nil {
		t.Fatalf("投递失败：%v", err)
	}
	auth, user, pass, from, rcpts, data := f.snapshot()
	if auth != "LOGIN" {
		t.Fatalf("应优先走 AUTH LOGIN（国内邮件服务常见），实得 %q", auth)
	}
	if user != "ops@example.com" || pass != "secret" {
		t.Fatalf("AUTH LOGIN 三步握手未把凭据送达：user=%q pass=%q", user, pass)
	}
	if from != "noreply@example.com" {
		t.Fatalf("发件人不对：%q", from)
	}
	if len(rcpts) != 2 || rcpts[0] != "a@example.com" || rcpts[1] != "b@example.com" {
		t.Fatalf("RCPT 不对：%v", rcpts)
	}
	if !strings.Contains(data, "Subject: 测试") || !strings.Contains(data, "正文") {
		t.Fatalf("信体未完整送达：%q", data)
	}
}

func TestDeliver_AuthFailureSurfaced(t *testing.T) {
	f := newFakeSMTP(t)
	f.rejectPass = true
	c := f.cfg()
	c.User, c.Pass = "ops@example.com", "wrong"
	err := deliver(c, "noreply@example.com", []string{"a@example.com"}, []byte("x\r\n"))
	if err == nil {
		t.Fatal("密码错误应返回失败")
	}
	if !strings.Contains(err.Error(), "AUTH") && !strings.Contains(err.Error(), "密码") {
		t.Fatalf("错误信息没说清是认证失败：%v", err)
	}
}

func TestDeliver_NoAuthExtensionSurfaces(t *testing.T) {
	f := newFakeSMTP(t)
	f.noAuth = true
	c := f.cfg()
	c.User, c.Pass = "ops@example.com", "secret"
	err := deliver(c, "noreply@example.com", []string{"a@example.com"}, []byte("x\r\n"))
	if err == nil || !strings.Contains(err.Error(), "AUTH") {
		t.Fatalf("服务端未声明 AUTH 时应给出明确错误，实得：%v", err)
	}
}

func TestDeliver_EmptyHostFails(t *testing.T) {
	c := Config{TLSMode: TLSNone, Timeout: 2 * time.Second}
	if err := deliver(c, "a@b.com", []string{"c@d.com"}, []byte("x")); err == nil {
		t.Fatal("未配主机时投递应失败")
	}
}

func TestDeliver_DotStuffingRoundTrip(t *testing.T) {
	// 正文里出现行首 "."：客户端要转成 ".."，服务端还原。
	// 不处理的话这一行会被当成结束符，后面内容全丢。
	f := newFakeSMTP(t)
	body := "第一行\r\n.隐藏行\r\n第三行\r\n"
	if err := deliver(f.cfg(), "noreply@example.com", []string{"a@example.com"}, []byte(body)); err != nil {
		t.Fatal(err)
	}
	_, _, _, _, _, data := f.snapshot()
	if !strings.Contains(data, ".隐藏行") {
		t.Fatalf("dot-stuffing 还原失败：%q", data)
	}
	if !strings.Contains(data, "第三行") {
		t.Fatalf("结束符之后的内容丢失：%q", data)
	}
}

func TestDeliver_BCCInEnvelopeNotInHeader(t *testing.T) {
	f := newFakeSMTP(t)
	raw := []byte("From: noreply@example.com\r\nTo: a@example.com\r\nSubject: s\r\n\r\nbody\r\n")
	if err := deliver(f.cfg(), "noreply@example.com",
		[]string{"a@example.com", "bcc@example.com"}, raw); err != nil {
		t.Fatal(err)
	}
	_, _, _, _, rcpts, data := f.snapshot()
	found := false
	for _, r := range rcpts {
		if r == "bcc@example.com" {
			found = true
		}
	}
	if !found {
		t.Fatalf("BCC 未进 SMTP 信封（密送人收不到信）：%v", rcpts)
	}
	if strings.Contains(data, "bcc@example.com") {
		t.Fatal("BCC 不该出现在信体里")
	}
}

func TestDeliver_QuitFailureStillSuccess(t *testing.T) {
	// QUIT 拿不到 221 不算投递失败：信封已经交出去了，服务端稍后可能会拒收，
	// 但那是服务端的事，不该让调用方以为没发出去。
	f := newFakeSMTP(t)
	if err := deliver(f.cfg(), "noreply@example.com", []string{"a@example.com"}, []byte("x\r\n")); err != nil {
		t.Fatal(err)
	}
	if f.sessionCount() == 0 {
		t.Fatal("没有建立任何会话")
	}
}

func TestRetryable(t *testing.T) {
	// 配置类与校验类错误重试没有意义，只会白白占着连接。
	if retryable(ErrNotConfigured) {
		t.Fatal("未配置不该重试")
	}
	if retryable(ErrTooManyRecipients) {
		t.Fatal("收件人超限不该重试")
	}
	if retryable(&ErrDomainNotAllowed{Addr: "a@b.com", Domain: "b.com"}) {
		t.Fatal("域名不合规不该重试")
	}
	if retryable(errors.New("发件人为空")) {
		t.Fatal("配置类错误不该重试")
	}
	if !retryable(errors.New("dial tcp 1.2.3.4:587: connect: connection refused")) {
		t.Fatal("网络错误应该重试")
	}
	if !retryable(errors.New("421 Service not available")) {
		t.Fatal("服务端临时错误应该重试")
	}
}

// ------------------------------------------------------------------ 发送器

func TestSender_SendWritesAuditChain(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		DatabaseURL: "sqlite://" + dir + "/t.db", DataDir: dir,
		EmailAllowedDomains: []string{"example.com"},
	}
	db, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	if err := db.AutoMigrate(models.AllModels()...); err != nil {
		t.Fatal(err)
	}

	f := newFakeSMTP(t)
	for k, v := range map[string]string{
		KeyHost: f.host(), KeyPort: itoa(f.port()), KeyTLS: TLSNone,
		KeyFrom: "noreply@example.com",
	} {
		if err := db.Save(&models.Setting{Key: k, Value: v}).Error; err != nil {
			t.Fatal(err)
		}
	}

	jobID := "job_test_0001"
	db.Save(&models.Job{ID: jobID, Status: "succeeded", Total: 1, Succeeded: 1,
		CreatedAt: time.Now()})

	s := New(db, cfg)
	err = s.Send(Outbox{
		JobID: jobID, Source: "job", ActorUserID: "u1",
		Message: Message{To: []string{"a@example.com"}, CC: []string{"c@example.com"},
			Subject: "对账单", Body: "请查收",
			Attach: []Attachment{{Filename: "对账单.docx", Content: []byte("PK fake")}}},
	})
	if err != nil {
		t.Fatalf("发送失败：%v", err)
	}

	// 投递结果只落一处：哈希链审计。没有独立表，也就不会出现两份对不上的记录。
	var log models.AuditLog
	if err := db.Where("action = ?", ActionDeliver).First(&log).Error; err != nil {
		t.Fatalf("未写投递审计：%v", err)
	}
	if log.ObjectType != ObjectTypeEmail || log.ObjectID != jobID {
		t.Fatalf("审计对象不对：%s/%s（期望 email/%s）", log.ObjectType, log.ObjectID, jobID)
	}
	if log.ActorType != "user" || log.ActorUserID != "u1" {
		t.Fatalf("审计主体不对：%s/%s", log.ActorType, log.ActorUserID)
	}

	var d map[string]interface{}
	if err := json.Unmarshal(log.Detail, &d); err != nil {
		t.Fatalf("detail 不是合法 JSON：%v", err)
	}
	if d["result"] != "sent" {
		t.Fatalf("detail.result 应为 sent，实得 %v（error=%v）", d["result"], d["error"])
	}
	if d["subject"] != "对账单" {
		t.Fatalf("detail.subject 不对：%v", d["subject"])
	}
	if d["source"] != "job" {
		t.Fatalf("detail.source 不对：%v", d["source"])
	}
	if d["attachment_1"] != "对账单.docx" {
		t.Fatalf("附件名未记录：%v", d["attachment_1"])
	}
	if to, ok := d["to"].([]interface{}); !ok || len(to) != 1 || to[0] != "a@example.com" {
		t.Fatalf("detail.to 不对：%v", d["to"])
	}
	if cc, ok := d["cc"].([]interface{}); !ok || len(cc) != 1 || cc[0] != "c@example.com" {
		t.Fatalf("detail.cc 不对：%v", d["cc"])
	}

	// 关键：这条记录必须在链上，篡改它能被校验发现——否则「放进审计」等于白放。
	if ok, checked, _, _, _ := (&auditx.Chain{DB: db}).Verify(); !ok || checked != 1 {
		t.Fatalf("哈希链校验未通过：ok=%v checked=%d", ok, checked)
	}

	// 任务状态仍然回写（调用方要读的状态，不是日志）
	var job models.Job
	db.First(&job, "id = ?", jobID)
	if job.EmailStatus != "sent" {
		t.Fatalf("job.email_status 未回写：%q", job.EmailStatus)
	}
}

func TestSender_FailedDeliveryAudited(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		DatabaseURL: "sqlite://" + dir + "/t.db", DataDir: dir,
		EmailAllowedDomains: []string{"example.com"},
	}
	db, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	if err := db.AutoMigrate(models.AllModels()...); err != nil {
		t.Fatal(err)
	}

	// 指向一个没人听的端口：投递必然失败，失败也必须留痕。
	for k, v := range map[string]string{
		KeyHost: "127.0.0.1", KeyPort: "1", KeyTLS: TLSNone,
		KeyFrom: "noreply@example.com",
	} {
		db.Save(&models.Setting{Key: k, Value: v})
	}
	s := New(db, cfg)
	s.Send(Outbox{
		JobID: "job_fail", Source: "job",
		Message: Message{To: []string{"a@example.com"}, Subject: "发不出去", Body: "x"},
	})

	var log models.AuditLog
	if err := db.Where("action = ?", ActionDeliver).First(&log).Error; err != nil {
		t.Fatalf("失败的投递也必须留痕：%v", err)
	}
	var d map[string]interface{}
	_ = json.Unmarshal(log.Detail, &d)
	if d["result"] != "failed" {
		t.Fatalf("detail.result 应为 failed，实得 %v", d["result"])
	}
	if str, _ := d["error"].(string); str == "" {
		t.Fatal("失败原因未记录，事后没法解释客户为什么没收到")
	}
}

func TestSender_SendRejectsDomainOutsideWhitelist(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		DatabaseURL: "sqlite://" + dir + "/t.db", DataDir: dir,
		EmailAllowedDomains: []string{"example.com"},
	}
	db, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	if err := db.AutoMigrate(models.AllModels()...); err != nil {
		t.Fatal(err)
	}
	f := newFakeSMTP(t)
	for k, v := range map[string]string{
		KeyHost: f.host(), KeyPort: itoa(f.port()), KeyTLS: TLSNone,
		KeyFrom: "noreply@example.com",
	} {
		db.Save(&models.Setting{Key: k, Value: v})
	}
	s := New(db, cfg)
	err = s.Send(Outbox{JobID: "job_x", Source: "job",
		Message: Message{To: []string{"someone@evil.com"}, Subject: "s", Body: "b"}})
	if err == nil {
		t.Fatal("名单外域名应被拒")
	}
	// 但 SendTest 是运维自己验链路，不该被白名单挡
	f2 := newFakeSMTP(t)
	for k, v := range map[string]string{
		KeyHost: f2.host(), KeyPort: itoa(f2.port()), KeyTLS: TLSNone,
		KeyFrom: "noreply@example.com",
	} {
		db.Save(&models.Setting{Key: k, Value: v})
	}
	s2 := New(db, cfg)
	if err := s2.SendTest(Outbox{Source: "admin_test",
		Message: Message{To: []string{"ops@evil.com"}, Subject: "s", Body: "b"}}); err != nil {
		t.Fatalf("测试邮件不该被白名单挡：%v", err)
	}
}

func TestSender_NotConfiguredReturnsTypedError(t *testing.T) {
	s := New(nil, &config.Config{})
	err := s.Send(Outbox{Message: Message{To: []string{"a@example.com"}}})
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("未配置 SMTP 应返回 ErrNotConfigured，实得 %v", err)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("一二三四五", 3); got != "一二三" {
		t.Fatalf("按 rune 截断不对：%q", got)
	}
	if got := truncate("ab", 5); got != "ab" {
		t.Fatalf("短串不该被截断：%q", got)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
