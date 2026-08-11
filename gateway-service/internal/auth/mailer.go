package auth

import (
	"fmt"
	"net/smtp"
	"os"
	"strings"
)

// Mailer sends transactional auth email (password reset). It is intentionally
// tiny — net/smtp against operator-supplied SMTP_* env — because the only
// message it needs to send is a reset link. When SMTP is not configured it
// reports Configured()==false so callers can degrade gracefully (in a dev stack
// the reset link is logged instead of emailed) without ever leaking whether an
// account exists.
type Mailer struct {
	host string
	port string
	user string
	pass string
	from string
}

// MailerFromEnv builds a Mailer from SMTP_HOST/SMTP_PORT/SMTP_USERNAME/
// SMTP_PASSWORD/SMTP_FROM. A missing SMTP_HOST yields an unconfigured mailer.
func MailerFromEnv() *Mailer {
	m := &Mailer{
		host: strings.TrimSpace(os.Getenv("SMTP_HOST")),
		port: strings.TrimSpace(os.Getenv("SMTP_PORT")),
		user: os.Getenv("SMTP_USERNAME"),
		pass: os.Getenv("SMTP_PASSWORD"),
		from: strings.TrimSpace(os.Getenv("SMTP_FROM")),
	}
	if m.port == "" {
		m.port = "587"
	}
	if m.from == "" {
		m.from = "no-reply@pulsetrace.local"
	}
	return m
}

// Configured reports whether an SMTP host is set.
func (m *Mailer) Configured() bool { return m != nil && m.host != "" }

// Send delivers a plain-text message. Uses SMTP AUTH when credentials are set,
// which is the common hosted-relay case; an unauthenticated relay (creds empty)
// still works for internal MTAs.
func (m *Mailer) Send(to, subject, body string) error {
	if !m.Configured() {
		return fmt.Errorf("SMTP is not configured")
	}
	addr := m.host + ":" + m.port
	msg := buildMessage(m.from, to, subject, body)

	var auth smtp.Auth
	if m.user != "" {
		auth = smtp.PlainAuth("", m.user, m.pass, m.host)
	}
	return smtp.SendMail(addr, auth, m.from, []string{to}, msg)
}

// buildMessage assembles RFC 5322 headers + body. Kept separate so it is unit-
// testable without a live SMTP server.
func buildMessage(from, to, subject, body string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(body)
	return []byte(b.String())
}
