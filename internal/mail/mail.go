// Package mail sends the service's own mail. The login link is the identity
// provider's mail, not this package's.
package mail

import (
	"context"
	"fmt"
	"log/slog"

	gomail "github.com/wneessen/go-mail"

	"github.com/paisans-software/paisans-invite/internal/config"
)

type Sender interface {
	Send(ctx context.Context, to []string, subject, body string) error
}

type smtpSender struct {
	c    config.SMTP
	opts []gomail.Option
}

func NewSMTP(c config.SMTP) (Sender, error) {
	opts := []gomail.Option{gomail.WithPort(c.Port)}
	switch c.TLS {
	case "none":
		opts = append(opts, gomail.WithTLSPolicy(gomail.NoTLS))
	case "starttls":
		opts = append(opts, gomail.WithTLSPolicy(gomail.TLSMandatory))
	case "tls":
		opts = append(opts, gomail.WithSSL())
	}
	if c.User != "" {
		opts = append(opts, gomail.WithSMTPAuth(gomail.SMTPAuthPlain), gomail.WithUsername(c.User), gomail.WithPassword(c.Password))
	}
	return &smtpSender{c: c, opts: opts}, nil
}

func (s *smtpSender) Send(ctx context.Context, to []string, subject, body string) error {
	client, err := gomail.NewClient(s.c.Host, s.opts...)
	if err != nil {
		return err
	}
	for _, rcpt := range to {
		m := gomail.NewMsg()
		if err := m.From(s.c.From); err != nil {
			return err
		}
		if err := m.To(rcpt); err != nil {
			return fmt.Errorf("recipient address rejected")
		}
		m.Subject(subject)
		m.SetBodyString(gomail.TypeTextPlain, body)
		if err := client.DialAndSendWithContext(ctx, m); err != nil {
			return fmt.Errorf("send mail: %w", err)
		}
	}
	slog.Info("mail sent", "subject", subject, "recipients", len(to))
	return nil
}
