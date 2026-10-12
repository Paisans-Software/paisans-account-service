// Package mail sends the service's own mail. The login link is the identity
// provider's mail, not this package's.
package mail

import (
	"context"
	"errors"
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

// Send attempts every recipient, so one bad address does not stop the rest.
// Errors are fixed text: go-mail's own errors carry recipient addresses and
// the server's reply, which must not reach a log.
func (s *smtpSender) Send(ctx context.Context, to []string, subject, body string) error {
	client, err := gomail.NewClient(s.c.Host, s.opts...)
	if err != nil {
		return errors.New("mail client could not be set up")
	}
	var errs []error
	for _, rcpt := range to {
		m := gomail.NewMsg()
		if err := m.From(s.c.From); err != nil {
			return errors.New("sender address rejected")
		}
		if err := m.To(rcpt); err != nil {
			errs = append(errs, errors.New("recipient address rejected"))
			continue
		}
		m.Subject(subject)
		m.SetBodyString(gomail.TypeTextPlain, body)
		if err := client.DialAndSendWithContext(ctx, m); err != nil {
			errs = append(errs, sanitize(err))
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	slog.Info("mail sent", "subject", subject, "recipients", len(to))
	return nil
}

// sanitize reduces a send error to a reason and a numeric code.
func sanitize(err error) error {
	var se *gomail.SendError
	if errors.As(err, &se) {
		return fmt.Errorf("send mail: %s (code %d)", se.Reason, se.ErrorCode())
	}
	return errors.New("send mail: delivery failed")
}
