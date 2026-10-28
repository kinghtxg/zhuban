package mailer

import (
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

// 自己实现一个最小 SMTP 客户端，而不是用 net/smtp。
//
// 两个理由：
//  1. net/smtp 自 Go 1.25 起被标记弃用（不再演进），新代码不该再往上靠；
//  2. 它只提供 PLAIN 认证，而国内常见的邮件服务（163、QQ 企业邮等）历史上
//     只认 AUTH LOGIN。自己写可以两种都支持，握手过程也完全可见、可测。

// smtpSession 一次投递会话。
//
// 所有命令都走 Cmd + StartResponse/EndResponse：Cmd 写进 bufio.Writer，
// 只有 StartResponse 才会 flush。绕过这套机制直接读响应会等到超时。
type smtpSession struct {
	conn     net.Conn
	tp       *textproto.Conn
	exts     map[string]bool
	authLine string
}

func (s *smtpSession) close() {
	if s.conn != nil {
		_ = s.conn.Close()
	}
}

// cmd 发一条命令并期待指定响应码。
func (s *smtpSession) cmd(expect int, format string, args ...interface{}) (string, error) {
	id, err := s.tp.Cmd(format, args...)
	if err != nil {
		return "", err
	}
	s.tp.StartResponse(id)
	defer s.tp.EndResponse(id)
	_, msg, err := s.tp.ReadResponse(expect)
	return msg, err
}

// deliver 连上服务器把一封信交出去。调用方已保证 rcpts 非空且校验过。
func deliver(c Config, from string, rcpts []string, raw []byte) error {
	addr := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	deadline := time.Now().Add(timeout)

	var sess *smtpSession
	if c.TLSMode == TLSDirect {
		d := &net.Dialer{Timeout: timeout}
		conn, err := tls.DialWithDialer(d, "tcp", addr,
			&tls.Config{ServerName: c.Host, MinVersion: tls.VersionTLS12})
		if err != nil {
			return fmt.Errorf("连接邮件服务器失败（TLS）：%w", err)
		}
		sess = newSession(conn)
	} else {
		conn, err := net.DialTimeout("tcp", addr, timeout)
		if err != nil {
			return fmt.Errorf("连接邮件服务器失败：%w", err)
		}
		sess = newSession(conn)
	}
	defer sess.close()
	_ = sess.conn.SetDeadline(deadline)

	// 服务就绪问候：此时没有待发命令，直接读。
	if _, _, err := sess.tp.ReadResponse(220); err != nil {
		return fmt.Errorf("邮件服务器未就绪：%w", err)
	}
	if err := sess.greet(); err != nil {
		return err
	}
	if c.TLSMode == TLSUpgrade {
		if !sess.exts["STARTTLS"] {
			return fmt.Errorf("邮件服务器不支持 STARTTLS（端口 %d），请改用 tls 或 none", c.Port)
		}
		if _, err := sess.cmd(220, "STARTTLS"); err != nil {
			return fmt.Errorf("STARTTLS 失败：%w", err)
		}
		tconn := tls.Client(sess.conn, &tls.Config{ServerName: c.Host, MinVersion: tls.VersionTLS12})
		if err := tconn.Handshake(); err != nil {
			return fmt.Errorf("TLS 握手失败：%w", err)
		}
		// 换连接对象后必须重建 textproto，并重新 EHLO——升格后服务端状态已重置，
		// 能力列表也可能不一样（升格前不给的 AUTH 升格后才给，这是常见做法）。
		sess.conn = tconn
		sess.tp = textproto.NewConn(tconn)
		sess.exts = map[string]bool{}
		_ = tconn.SetDeadline(deadline)
		if err := sess.greet(); err != nil {
			return err
		}
	}
	if strings.TrimSpace(c.User) != "" {
		if err := sess.auth(c.User, c.Pass); err != nil {
			return err
		}
	}
	if _, err := sess.cmd(250, "MAIL FROM:<%s>", from); err != nil {
		return fmt.Errorf("服务器拒绝发件人 %s：%w", from, err)
	}
	// 25 匹配 250/251：收件人可以是「转发到别处」的 251。
	for _, r := range rcpts {
		if _, err := sess.cmd(25, "RCPT TO:<%s>", r); err != nil {
			return fmt.Errorf("服务器拒绝收件人 %s：%w", r, err)
		}
	}
	if err := sess.data(raw); err != nil {
		return err
	}
	// QUIT 失败不算投递失败：信封已经交出去了。
	_, _ = sess.cmd(221, "QUIT")
	return nil
}

// data 走完整的 DATA 流程：354 → 写内容（含 dot-stuffing）→ 250。
func (s *smtpSession) data(raw []byte) error {
	id, err := s.tp.Cmd("DATA")
	if err != nil {
		return err
	}
	s.tp.StartResponse(id)
	defer s.tp.EndResponse(id)
	if _, _, err := s.tp.ReadResponse(354); err != nil {
		return fmt.Errorf("服务器未接受 DATA：%w", err)
	}
	w := s.tp.DotWriter()
	if _, err := w.Write(raw); err != nil {
		return fmt.Errorf("写入邮件内容失败：%w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("结束邮件内容失败：%w", err)
	}
	if _, _, err := s.tp.ReadResponse(250); err != nil {
		return fmt.Errorf("服务器拒收邮件：%w", err)
	}
	return nil
}

func newSession(conn net.Conn) *smtpSession {
	return &smtpSession{conn: conn, tp: textproto.NewConn(conn), exts: map[string]bool{}}
}

// greet 发 EHLO；服务器不认 EHLO 时退到 HELO（老中继）。
func (s *smtpSession) greet() error {
	msg, err := s.cmd(250, "EHLO %s", "localhost")
	if err != nil {
		if _, err2 := s.cmd(250, "HELO %s", "localhost"); err2 != nil {
			return fmt.Errorf("EHLO/HELO 均被拒绝：%w", err)
		}
		return nil
	}
	for _, line := range strings.Split(msg, "\n") {
		up := strings.ToUpper(strings.TrimSpace(line))
		if up == "" {
			continue
		}
		if strings.HasPrefix(up, "AUTH") {
			s.authLine = up
			s.exts["AUTH"] = true
			for _, m := range strings.Fields(up)[1:] {
				s.exts[strings.ToUpper(m)] = true
			}
			continue
		}
		s.exts[strings.Fields(up)[0]] = true
	}
	return nil
}

// auth 优先用服务器声明支持的方式：LOGIN（国内服务常见）> PLAIN。
func (s *smtpSession) auth(user, pass string) error {
	if !s.exts["AUTH"] {
		return fmt.Errorf("邮件服务器未在 EHLO 中声明 AUTH，无法登录")
	}
	enc := base64.StdEncoding.EncodeToString
	switch {
	case s.exts["LOGIN"]:
		// LOGIN 是三步握手，每一步都是独立的一问一答：
		//   C: AUTH LOGIN      → S: 334（要用户名）
		//   C: <base64 user>   → S: 334（要密码）
		//   C: <base64 pass>   → S: 235
		// 首条命令不能省：少了它，服务端还在等命令，收到裸 base64 只会回 500。
		if _, err := s.cmd(334, "AUTH LOGIN"); err != nil {
			return fmt.Errorf("AUTH LOGIN 未被接受：%w", err)
		}
		if _, err := s.cmd(334, "%s", enc([]byte(user))); err != nil {
			return fmt.Errorf("AUTH LOGIN 用户名被拒：%w", err)
		}
		if _, err := s.cmd(235, "%s", enc([]byte(pass))); err != nil {
			return fmt.Errorf("AUTH LOGIN 密码被拒：%w", err)
		}
		return nil
	case s.exts["PLAIN"]:
		if _, err := s.cmd(235, "AUTH PLAIN %s", enc([]byte("\x00"+user+"\x00"+pass))); err != nil {
			return fmt.Errorf("AUTH PLAIN 被拒：%w", err)
		}
		return nil
	}
	return fmt.Errorf("邮件服务器不支持可用的认证方式（AUTH: %s）", s.authLine)
}
