package flow

import (
	"cmp"
	"context"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// App is one tile of a member's Your apps card. LogoURL is Pocket ID's public
// light logo of the client, which with ?light=false is its dark one; Pocket ID
// serves either variant when the client has only the other. It is "" when the
// client has neither, and Letter stands in for it.
type App struct {
	ID, Name, URL, Host, LogoURL, Letter string
}

// Apps returns the apps Pocket ID's own dashboard shows the member, sorted by
// name ignoring case, then by ID. Its rule (service/oidc_service.go:844-857
// at v2.18.0): a client is listed when it is not group-restricted or allows
// one of the user's groups, and its launch URL is neither NULL nor empty. An
// app whose launch URL is not an absolute https URL with a host is left off
// too, and logged, so the page never links anywhere else.
func (s *Service) Apps(ctx context.Context, m Member) ([]App, error) {
	gs, err := s.p.UserGroups(ctx, m.Sub)
	if err != nil {
		return nil, err
	}
	cs, err := s.p.ListClients(ctx)
	if err != nil {
		return nil, err
	}
	mine := map[string]bool{}
	for _, g := range gs {
		mine[g.ID] = true
	}
	out := []App{}
	for _, c := range cs {
		if c.LaunchURL == "" || (c.Restricted && !slices.ContainsFunc(c.AllowedGroupIDs, func(id string) bool { return mine[id] })) {
			continue
		}
		u, err := url.Parse(c.LaunchURL)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" {
			slog.Warn("app left off the list: its launch URL is not an absolute https URL", "client_id", c.ID, "client_name", c.Name)
			continue
		}
		a := App{ID: c.ID, Name: c.Name, URL: c.LaunchURL, Host: u.Hostname()}
		if c.HasLogo || c.HasDarkLogo {
			a.LogoURL = s.s.IDPURL + "/api/oidc/clients/" + url.PathEscape(c.ID) + "/logo"
		} else if r, _ := utf8.DecodeRuneInString(c.Name); r != utf8.RuneError {
			a.Letter = string(unicode.ToUpper(r))
		}
		out = append(out, a)
	}
	slices.SortFunc(out, func(a, b App) int {
		return cmp.Or(cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), cmp.Compare(a.ID, b.ID))
	})
	return out, nil
}
