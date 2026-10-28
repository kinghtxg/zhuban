package mailer

import (
	"fmt"
	"log"
	"strings"
	"time"

	"docforge/server/internal/auditx"
	"docforge/server/internal/models"
)

// maxConcurrent 同时在发信的协程上限。
//
// SMTP 服务器（尤其企业邮）对并发连接很敏感。批量 500 份时若一股脑开 500 个连接，
// 结果不是更快，而是集体被限流/断连，然后全部进重试。宁可排队。
const maxConcurrent = 3

var sem = make(chan struct{}, maxConcurrent)

// Outbox 一封待发邮件 + 它的来源上下文（写审计用）。
type Outbox struct {
	JobID      string
	DocumentID string
	Source     string // job|admin_test
	Message    Message

	ActorUserID string
	// ActorName 调用人显示名。审计里「谁发的」比 ID 有用得多，
	// 而投递发生在 worker 里、拿不到 Principal，只能由调用方带进来。
	ActorName string
	APIKeyID  string
}

// Send 同步发一封，并把结果写进哈希链审计（成功失败都写）。
//
// 校验顺序刻意是「配置 → 收件人 → 正文/附件」：
// 没配 SMTP 就别去解析收件人（省一次白干活），收件人不合法也别去读附件文件。
func (s *Sender) Send(ob Outbox) error {
	err := s.once(ob, false)
	s.record(ob, err, 1)
	return err
}

// SendTest 发一封运维测试邮件：跳过收件人域名白名单，其余校验照旧。
func (s *Sender) SendTest(ob Outbox) error {
	err := s.once(ob, true)
	s.record(ob, err, 1)
	return err
}

func (s *Sender) once(ob Outbox, skipDomain bool) error {
	c := s.Load()
	if !c.Enabled() {
		return ErrNotConfigured
	}
	from := c.EffectiveFrom()
	if from == "" {
		return fmt.Errorf("发件人为空：请配置 smtp_from 或 smtp_user")
	}
	if err := c.checkRecipients(ob.Message.To, ob.Message.CC, ob.Message.BCC, skipDomain); err != nil {
		return err
	}
	if c.MaxBodyChars > 0 && len([]rune(ob.Message.Body)) > c.MaxBodyChars {
		return fmt.Errorf("正文过长（%d 字，上限 %d）", len([]rune(ob.Message.Body)), c.MaxBodyChars)
	}
	if c.MaxAttachBytes > 0 && ob.Message.TotalBytes() > c.MaxAttachBytes {
		return fmt.Errorf("附件过大（%.1f MB，上限 %d MB）",
			float64(ob.Message.TotalBytes())/(1<<20), c.MaxAttachBytes>>20)
	}
	m := ob.Message
	// 收件人看到的 From 始终是服务端配置的发件人，调用方改不了——
	// 否则一封声称来自别人的信会由我们的服务器发出去。
	m.From = from
	raw, err := buildMIME(from, m, time.Now(), newMessageID(from))
	if err != nil {
		return fmt.Errorf("组装邮件失败：%w", err)
	}
	return deliver(c, from, m.AllRecipients(), raw)
}

// SendAsync 异步发送，退避重试 3 次（1s/4s/16s），更新任务上的邮件状态。
// 不阻塞 worker：发信再慢也不能把任务卡在 running。
func (s *Sender) SendAsync(ob Outbox) {
	go func() {
		sem <- struct{}{}
		defer func() { <-sem }()

		delays := []time.Duration{1 * time.Second, 4 * time.Second, 16 * time.Second}
		var lastErr error
		attempt := 1
		for ; attempt <= 3; attempt++ {
			lastErr = s.once(ob, false)
			if lastErr == nil {
				break
			}
			// 配置类/校验类错误重试没有意义，直接收尾。
			if !retryable(lastErr) {
				break
			}
			if attempt < 3 {
				time.Sleep(delays[attempt-1])
			}
		}
		if lastErr != nil {
			log.Printf("[mailer] 发送失败 job=%s doc=%s to=%v: %v",
				ob.JobID, ob.DocumentID, ob.Message.To, lastErr)
		}
		s.record(ob, lastErr, attempt)
	}()
}

// retryable 判断值不值得重试：网络/临时性错误值得，配置与收件人错误不值得。
func retryable(err error) bool {
	if err == nil {
		return false
	}
	if err == ErrNotConfigured || err == ErrTooManyRecipients {
		return false
	}
	if _, ok := err.(*ErrDomainNotAllowed); ok {
		return false
	}
	msg := err.Error()
	for _, s := range []string{"收件人", "发件人为空", "正文过长", "附件过大", "组装邮件失败"} {
		if strings.Contains(msg, s) {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// 投递结果落审计
//
// 为什么不单独建一张「邮件投递记录」表：
//  1. 同一个事实存两处，迟早对不上，而对不上时你不知道该信哪一份；
//  2. 审计链是防篡改的、可校验可导出，投递记录本来就属于「谁在什么时候
//     把什么东西发给了谁」——这恰恰是审计该回答的问题，另开一张表
//     等于把最有合规价值的一类记录放到链外；
//  3. 查询入口也因此只剩一个，运维不用记「邮件看邮件页、操作看审计页」。
// ---------------------------------------------------------------------------

const (
	// ActionDeliver 一次邮件投递（结果在 detail.result）。
	ActionDeliver = "mail.deliver"
	// ObjectTypeEmail 审计对象类型：邮件。
	ObjectTypeEmail = "email"
)

// record 把一次投递写进哈希链审计，并把结果回写到所属任务/产物。
func (s *Sender) record(ob Outbox, err error, attempts int) {
	status := "sent"
	errMsg := ""
	if err != nil {
		status = "failed"
		errMsg = truncate(err.Error(), 480)
	}
	s.markOwner(ob, status, attempts, errMsg)
	if s.Audit == nil {
		return
	}

	detail := map[string]interface{}{
		"result":      status,
		"to":          ob.Message.To,
		"subject":     truncate(ob.Message.Subject, 500),
		"attachments": len(ob.Message.Attach),
		"bytes":       ob.Message.TotalBytes(),
		"attempts":    attempts,
		"source":      ob.Source,
	}
	if len(ob.Message.CC) > 0 {
		detail["cc"] = ob.Message.CC
	}
	// BCC 要记下来（否则密送无从追溯），但字段本身就是"密"的，
	// 审计页与 CSV 导出面向管理员，这是合规上必须留的那一半。
	if len(ob.Message.BCC) > 0 {
		detail["bcc"] = ob.Message.BCC
	}
	if ob.DocumentID != "" {
		detail["document_id"] = ob.DocumentID
	}
	for i, a := range ob.Message.Attach {
		if i >= 10 {
			break
		}
		detail[fmt.Sprintf("attachment_%d", i+1)] = a.Filename
	}
	if errMsg != "" {
		detail["error"] = errMsg
	}
	if ob.APIKeyID != "" {
		detail["api_key_id"] = ob.APIKeyID
	}

	// object_id 优先用任务 ID：同一批投递挂在同一个对象下，按任务一查就是全部。
	objectID := ob.JobID
	if objectID == "" && len(ob.Message.To) > 0 {
		objectID = ob.Message.To[0]
	}

	e := &auditx.Entry{
		Action:     ActionDeliver,
		ObjectType: ObjectTypeEmail,
		ObjectID:   objectID,
		Detail:     detail,
	}
	switch {
	case ob.APIKeyID != "":
		e.ActorType = "api_key"
		e.ActorName = ob.ActorName
	case ob.ActorUserID != "":
		e.ActorType = "user"
		e.ActorUserID = ob.ActorUserID
		e.ActorName = ob.ActorName
	default:
		e.ActorType = "system"
	}
	if _, err := s.Audit.Write(e); err != nil {
		log.Printf("[mailer] 审计写入失败 job=%s: %v", ob.JobID, err)
	}
}

// markOwner 把结果回写到所属 job / document，让任务详情能直接看到投递状态。
//
// 注意这里回写的是「任务状态机」而不是日志：日志只此一份在审计里，
// 而任务需不需要重发、有没有发出去过，是调用方要读的状态，两者不重复。
func (s *Sender) markOwner(ob Outbox, status string, attempts int, errMsg string) {
	if ob.JobID == "" || s.DB == nil {
		return
	}
	upd := map[string]interface{}{"email_status": status, "email_attempts": attempts, "email_error": errMsg}
	if ob.DocumentID != "" {
		s.DB.Model(&models.Document{}).Where("id = ?", ob.DocumentID).Updates(upd)
		return
	}
	s.DB.Model(&models.Job{}).Where("id = ?", ob.JobID).Updates(upd)
}

func truncate(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n])
}

// Save 把管理端提交的 SMTP 配置写进 setting 表（密码允许为空串=清空）。
func (s *Sender) Save(in map[string]string) error {
	if s.DB == nil {
		return fmt.Errorf("数据库不可用")
	}
	for k, v := range in {
		row := models.Setting{Key: k, Value: v, UpdatedAt: time.Now()}
		if err := s.DB.Save(&row).Error; err != nil {
			return err
		}
	}
	return nil
}

// ReadAll 读出全部邮件相关 setting（供管理端回填）。
func (s *Sender) ReadAll() map[string]string {
	out := map[string]string{}
	if s.DB == nil {
		return out
	}
	for _, k := range []string{KeyHost, KeyPort, KeyUser, KeyPass, KeyFrom, KeyTLS, KeyAllowedDomains} {
		var row models.Setting
		if err := s.DB.Where("key = ?", k).First(&row).Error; err == nil {
			out[k] = row.Value
		} else {
			out[k] = ""
		}
	}
	return out
}
