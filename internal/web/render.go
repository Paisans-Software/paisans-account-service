package web

import (
	"embed"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
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
	idp    idpLink
}

// idpLink is the identity provider as a page shows it: the configured
// INVITE_IDP_URL, and its host as the link text.
type idpLink struct{ URL, Host string }

// messageData is message.html's data. Recovery, when set, adds the line that
// sends someone whose link expired to the identity provider.
type messageData struct {
	Heading, Body string
	Recovery      *idpLink
}

func newRenderer(accent, idpURL string) (*renderer, error) {
	u, err := url.Parse(idpURL)
	if err != nil || u.Host == "" {
		return nil, errors.New("the identity provider URL must be an absolute URL")
	}
	r := &renderer{pages: map[string]*template.Template{}, logo: idpURL + "/api/application-images/logo",
		idp: idpLink{URL: idpURL, Host: u.Host}}
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
