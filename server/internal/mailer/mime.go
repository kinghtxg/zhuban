package mailer

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"path/filepath"
	"strings"
	"time"
)

// Attachment 一个附件。Content 为空时不带附件。
type Attachment struct {
	Filename string
	Content  []byte
}

// Message 一封待发的信。
type Message struct {
	To      []string
	CC      []string
	BCC     []string
	Subject string
	Body    string
	Attach  []Attachment
	// From 为空时取配置的 EffectiveFrom()。
	From string
}

// TotalBytes 附件总字节数（限额判定用）。
func (m Message) TotalBytes() int64 {
	var n int64
	for _, a := range m.Attach {
		n += int64(len(a.Content))
	}
	return n
}

// AllRecipients 信封收件人 = To + CC + BCC（BCC 不写进信头，但要进 SMTP 会话）。
func (m Message) AllRecipients() []string {
	out := make([]string, 0, len(m.To)+len(m.CC)+len(m.BCC))
	out = append(out, m.To...)
	out = append(out, m.CC...)
	out = append(out, m.BCC...)
	return out
}

var wordEnc = mime.BEncoding

// encodeHeader 把可能含中文的头字段编成 RFC 2047 的 =?UTF-8?B?..?=。
// 中文主题不编码的话，Outlook 与部分网关会把整条标题显示成乱码。
func encodeHeader(s string) string {
	if s == "" || isASCII(s) {
		return s
	}
	return wordEnc.Encode("utf-8", s)
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > 0x7f {
			return false
		}
	}
	return true
}

// lineBreaker 按 RFC 2045 把 base64 折成每行 76 字符。
// 不折行的话，超长行会被中间网关截断或判为畸形。
type lineBreaker struct {
	w   io.Writer
	col int
}

func (l *lineBreaker) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		if l.col >= 76 {
			if _, err := io.WriteString(l.w, "\r\n"); err != nil {
				return written, err
			}
			l.col = 0
		}
		n := 76 - l.col
		if n > len(p) {
			n = len(p)
		}
		nn, err := l.w.Write(p[:n])
		written += nn
		l.col += nn
		if err != nil {
			return written, err
		}
		p = p[n:]
	}
	return written, nil
}

// buildMIME 组装整封信（multipart/mixed：正文 + 若干附件）。
func buildMIME(from string, m Message, now time.Time, msgID string) ([]byte, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)

	// ---- 正文段 ----
	th := map[string][]string{
		"Content-Type":              {"text/plain; charset=UTF-8"},
		"Content-Transfer-Encoding": {"base64"},
	}
	pw, err := w.CreatePart(th)
	if err != nil {
		return nil, err
	}
	if err := writeBase64(pw, []byte(m.Body)); err != nil {
		return nil, err
	}

	// ---- 附件段 ----
	for _, a := range m.Attach {
		if a.Filename == "" || len(a.Content) == 0 {
			continue
		}
		ah := map[string][]string{
			// 中文文件名：name 用 RFC 2047（老客户端认），filename* 用 RFC 2231（新客户端认）。
			// 只给一种，总有一半客户端看到乱码文件名。
			"Content-Type": {fmt.Sprintf("%s;\r\n name=%q", mimeOf(a.Filename), encodeHeader(a.Filename))},
			"Content-Disposition": {fmt.Sprintf("attachment;\r\n filename=%q;\r\n filename*=UTF-8''%s",
				encodeHeader(a.Filename), percentEncode(a.Filename))},
			"Content-Transfer-Encoding": {"base64"},
		}
		aw, err := w.CreatePart(ah)
		if err != nil {
			return nil, err
		}
		if err := writeBase64(aw, a.Content); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}

	// ---- 信头 ----
	var head bytes.Buffer
	writeHeader(&head, "Date", now.Format(time.RFC1123Z))
	writeHeader(&head, "From", from)
	if len(m.To) > 0 {
		writeHeader(&head, "To", strings.Join(m.To, ", "))
	}
	if len(m.CC) > 0 {
		writeHeader(&head, "Cc", strings.Join(m.CC, ", "))
	}
	// BCC 刻意不写进信头——写了等于把密送名单公开给所有收件人。
	writeHeader(&head, "Subject", encodeHeader(m.Subject))
	writeHeader(&head, "Message-ID", msgID)
	writeHeader(&head, "MIME-Version", "1.0")
	writeHeader(&head, "Content-Type", fmt.Sprintf("multipart/mixed;\r\n boundary=%q", w.Boundary()))
	head.WriteString("\r\n")

	out := make([]byte, 0, head.Len()+body.Len())
	out = append(out, head.Bytes()...)
	out = append(out, body.Bytes()...)
	return out, nil
}

func writeBase64(w io.Writer, raw []byte) error {
	lb := &lineBreaker{w: w}
	enc := base64.NewEncoder(base64.StdEncoding, lb)
	if _, err := enc.Write(raw); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\r\n")
	return err
}

func writeHeader(w *bytes.Buffer, k, v string) {
	w.WriteString(k)
	w.WriteString(": ")
	w.WriteString(v)
	w.WriteString("\r\n")
}

// percentEncode RFC 2231 的 filename* 编码：非安全字符转成 %XX。
func percentEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '.' || c == '_' {
			b.WriteByte(c)
			continue
		}
		b.WriteString(fmt.Sprintf("%%%02X", c))
	}
	return b.String()
}

func mimeOf(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".pdf":
		return "application/pdf"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".doc":
		return "application/msword"
	case ".zip":
		return "application/zip"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	}
	return "application/octet-stream"
}

// newMessageID 生成 Message-ID。用随机而不是计数器：同一秒里连发几十封时，
// Message-ID 撞车会被部分网关判成重复投递直接丢弃。
func newMessageID(from string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	host := "zhuban.local"
	if addr, err := mail.ParseAddress(from); err == nil && strings.Contains(addr.Address, "@") {
		host = addr.Address[strings.LastIndex(addr.Address, "@")+1:]
	}
	return fmt.Sprintf("<%d.%s@%s>", time.Now().UnixNano(), hex.EncodeToString(b), host)
}
