package mail

import (
	"errors"
	"strings"
	"testing"

	gomail "github.com/wneessen/go-mail"

	"github.com/paisans-software/paisans-invite/internal/config"
)

func TestSanitizeDropsAddresses(t *testing.T) {
	for _, in := range []error{
		errors.New("550 <bob@example.org> no such user"),
		&gomail.SendError{Reason: gomail.ErrSMTPRcptTo},
	} {
		if got := sanitize(in).Error(); strings.Contains(got, "@") || strings.Contains(got, "example") {
			t.Fatalf("leaked: %q", got)
		}
	}
}

func TestSendRejectsBadRecipientWithoutEchoing(t *testing.T) {
	s, _ := NewSMTP(configForTest())
	err := s.Send(t.Context(), []string{"not an address@@"}, "s", "b")
	if err == nil || strings.Contains(err.Error(), "address@@") {
		t.Fatalf("%v", err)
	}
}

func configForTest() config.SMTP {
	return config.SMTP{Host: "127.0.0.1", Port: 1, From: "from@example.org", TLS: "none"}
}
