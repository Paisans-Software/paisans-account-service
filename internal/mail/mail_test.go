package mail

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	gomail "github.com/wneessen/go-mail"

	"github.com/paisans-software/paisans-account-service/internal/config"
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

// rejectingSMTP is an SMTP server that refuses every recipient with a reply
// naming the address, as real servers do.
func rejectingSMTP(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				r := bufio.NewReader(c)
				fmt.Fprint(c, "220 test ESMTP\r\n")
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					cmd := strings.ToUpper(strings.TrimSpace(line))
					switch {
					case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
						fmt.Fprint(c, "250-test\r\n250 OK\r\n")
					case strings.HasPrefix(cmd, "RCPT TO:"):
						addr := strings.TrimSpace(line[len("RCPT TO:"):])
						fmt.Fprintf(c, "550 5.1.1 %s: no such user here\r\n", addr)
					case strings.HasPrefix(cmd, "QUIT"):
						fmt.Fprint(c, "221 bye\r\n")
						return
					default:
						fmt.Fprint(c, "250 OK\r\n")
					}
				}
			}(c)
		}
	}()
	return l.Addr().(*net.TCPAddr).Port
}

// A real go-mail SendError carries the recipient and the server's reply,
// which names the address; the error Send returns carries neither.
func TestSendErrorWithAddressesIsSanitised(t *testing.T) {
	port := rejectingSMTP(t)

	// go-mail's own error names the address: this is what must not escape.
	client, err := gomail.NewClient("127.0.0.1", gomail.WithPort(port), gomail.WithTLSPolicy(gomail.NoTLS))
	if err != nil {
		t.Fatal(err)
	}
	m := gomail.NewMsg()
	m.From("from@example.org")
	m.To("bob@example.org")
	m.SetBodyString(gomail.TypeTextPlain, "b")
	raw := client.DialAndSendWithContext(t.Context(), m)
	var se *gomail.SendError
	if !errors.As(raw, &se) || !strings.Contains(raw.Error(), "bob@example.org") {
		t.Fatalf("want a SendError naming the address, got %v", raw)
	}
	for _, in := range []error{raw, fmt.Errorf("wrapped: %w", raw)} {
		if got := sanitize(in).Error(); strings.Contains(got, "@") || strings.Contains(got, "example") {
			t.Fatalf("sanitize leaked: %q", got)
		}
	}

	c := configForTest()
	c.Port = port
	s, _ := NewSMTP(c)
	err = s.Send(t.Context(), []string{"bob@example.org", "carol@example.org"}, "s", "b")
	if err == nil || strings.Contains(err.Error(), "@") || strings.Contains(err.Error(), "example") {
		t.Fatalf("Send leaked: %v", err)
	}
}
