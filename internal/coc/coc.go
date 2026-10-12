// Package coc loads the code of conduct an invitee agrees to.
package coc

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"html/template"
	"os"

	"github.com/yuin/goldmark"
)

type CoC struct {
	Version string
	HTML    template.HTML
}

func Load(path string) (CoC, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return CoC{}, err
	}
	var buf bytes.Buffer
	// goldmark's default renderer omits raw HTML, so the file cannot inject
	// script into the page.
	if err := goldmark.Convert(src, &buf); err != nil {
		return CoC{}, err
	}
	sum := sha256.Sum256(src)
	return CoC{Version: hex.EncodeToString(sum[:]), HTML: template.HTML(buf.String())}, nil
}
