package coc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	p := filepath.Join(t.TempDir(), "coc.md")
	if err := os.WriteFile(p, []byte("# Be kind\n\n<script>x</script>"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Version) != 64 || !strings.Contains(string(c.HTML), "<h1>Be kind</h1>") || strings.Contains(string(c.HTML), "<script>") {
		t.Fatalf("%+v", c)
	}
	if err := os.WriteFile(p, []byte("# Be kinder"), 0o600); err != nil {
		t.Fatal(err)
	}
	c2, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c2.Version == c.Version {
		t.Fatal("an edit is a new version")
	}
}
