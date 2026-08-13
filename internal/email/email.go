// ai-form-backend - AGPL-3.0
// SMTP 发送,移植自 new-api common/email.go 的连接模式(SSL 直连 / STARTTLS),做了精简。
package email

import (
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net/smtp"
	"time"

	"github.com/HPZS/ai-form-backend/config"
)

type Mailer struct {
	cfg config.SMTPConfig
}

func New(cfg config.SMTPConfig) *Mailer { return &Mailer{cfg: cfg} }

func (m *Mailer) Configured() bool { return m.cfg.Server != "" && m.cfg.Account != "" }

func (m *Mailer) Send(to, subject, htmlBody string) error {
	if !m.Configured() {
		return fmt.Errorf("SMTP 未配置")
	}
	from := m.cfg.From
	if from == "" {
		from = m.cfg.Account
	}
	encodedSubject := fmt.Sprintf("=?UTF-8?B?%s?=", base64.StdEncoding.EncodeToString([]byte(subject)))
	msg := []byte(fmt.Sprintf("To: %s\r\nFrom: %s <%s>\r\nSubject: %s\r\nDate: %s\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n%s\r\n",
		to, m.cfg.SystemName, from, encodedSubject, time.Now().Format(time.RFC1123Z), htmlBody))

	addr := fmt.Sprintf("%s:%d", m.cfg.Server, m.cfg.Port)
	client, err := m.connect(addr)
	if err != nil {
		return fmt.Errorf("连接 SMTP 失败: %w", err)
	}
	defer client.Close()

	if err := client.Auth(smtp.PlainAuth("", m.cfg.Account, m.cfg.Token, m.cfg.Server)); err != nil {
		return fmt.Errorf("SMTP 认证失败: %w", err)
	}
	if err := client.Mail(from); err != nil {
		return err
	}
	if err := client.Rcpt(to); err != nil {
		return err
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return client.Quit()
}

func (m *Mailer) connect(addr string) (*smtp.Client, error) {
	tlsCfg := &tls.Config{ServerName: m.cfg.Server}
	if m.cfg.SSL || m.cfg.Port == 465 {
		conn, err := tls.Dial("tcp", addr, tlsCfg)
		if err != nil {
			return nil, err
		}
		c, err := smtp.NewClient(conn, m.cfg.Server)
		if err != nil {
			conn.Close()
			return nil, err
		}
		return c, nil
	}
	c, err := smtp.Dial(addr)
	if err != nil {
		return nil, err
	}
	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(tlsCfg); err != nil {
			c.Close()
			return nil, err
		}
	}
	return c, nil
}

func (m *Mailer) SendVerifyCode(to, code string) error {
	body := fmt.Sprintf(`<div style="font-family:sans-serif;max-width:480px;margin:0 auto">
<h2>%s</h2>
<p>你的登录验证码是:</p>
<p style="font-size:28px;font-weight:bold;letter-spacing:6px">%s</p>
<p style="color:#888">10 分钟内有效。如果这不是你的操作,请忽略本邮件。</p>
</div>`, m.cfg.SystemName, code)
	return m.Send(to, fmt.Sprintf("【%s】登录验证码", m.cfg.SystemName), body)
}
