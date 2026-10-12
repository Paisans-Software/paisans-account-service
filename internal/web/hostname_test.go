package web

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// idpHostname matches a hostname that starts with an identity-provider label
// such as "id." or "pid.", followed by a domain.
var idpHostname = regexp.MustCompile(`(^|[^A-Za-z0-9_-])(p?id|auth|login|sso)\.[a-z0-9-]+\.[a-z]{2,}`)

// The identity provider's URL comes only from INVITE_IDP_URL: no source or
// template under internal/ may name one.
func TestNoHardCodedIDPHostname(t *testing.T) {
	err := filepath.WalkDir("..", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasSuffix(p, "_test.go") {
			return err
		}
		if !strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, ".html") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			if m := idpHostname.FindString(line); m != "" {
				t.Errorf("%s:%d: hard-coded identity provider hostname %q", p, i+1, strings.TrimLeft(m, " \"'/`(=:"))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestIDPHostnamePattern(t *testing.T) {
	for s, want := range map[string]bool{
		`"https://id.example.org"`:             true,
		`"pid.paisans.community"`:              true,
		`url := "https://login.two.example/x"`: true,
		`u.Host`:                               false,
		`idp.User{ID: id}`:                     false,
		`"/api/users/" + id`:                   false,
	} {
		if got := idpHostname.MatchString(s); got != want {
			t.Errorf("%s: got %v, want %v", s, got, want)
		}
	}
}
