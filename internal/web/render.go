package web

import (
	"embed"
	"html/template"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

var accentRule = regexp.MustCompile(`^(#[0-9a-fA-F]{3,8}|[a-zA-Z]+|(rgb|hsl|oklch)a?\([0-9.,%\s/]+\))$`)

type view struct {
	Title, LogoURL, CSRF string
	Accent               template.CSS
	Session              *Session
	Data                 any
	Error                string
}

type renderer struct {
	pages  map[string]*template.Template
	accent template.CSS
	logo   string
}

func newRenderer(accent, idpURL string) (*renderer, error) {
	r := &renderer{pages: map[string]*template.Template{}, logo: idpURL + "/api/application-images/logo"}
	// Pocket ID's accent_color is "default" or a CSS colour such as
	// "oklch(0.63 0.2 15)". Only a value the rule admits is passed through as
	// trusted CSS; html/template would otherwise defuse the parentheses.
	if !strings.EqualFold(accent, "default") && accentRule.MatchString(accent) {
		r.accent = template.CSS(accent)
	}
	for _, name := range []string{"home.html", "admin.html", "invite.html", "message.html"} {
		t, err := template.ParseFS(templateFS, "templates/layout.html", "templates/"+name)
		if err != nil {
			return nil, err
		}
		r.pages[name] = t
	}
	return r, nil
}

func (r *renderer) page(w http.ResponseWriter, status int, name string, v view) {
	v.Accent, v.LogoURL = r.accent, r.logo
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' "+r.logo+"; style-src 'self' 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'")
	w.WriteHeader(status)
	if err := r.pages[name].ExecuteTemplate(w, "layout", v); err != nil {
		slog.Error("render", "page", name, "error", err)
	}
}
