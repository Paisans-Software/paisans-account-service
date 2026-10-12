package web

import (
	"crypto/subtle"
	"errors"
	"html/template"
	"net/http"
	"strings"

	"github.com/paisans-software/paisans-invite/internal/flow"
	"github.com/paisans-software/paisans-invite/internal/store"
)

type inviteData struct {
	Inviter                      string
	CoC                          template.HTML
	Username, DisplayName, Email string
}

func (s *server) unusable(w http.ResponseWriter) {
	s.Render.page(w, http.StatusNotFound, "message.html", view{Title: "Invite", Data: map[string]string{
		"Heading": "This invite can't be used",
		"Body":    "It may have been used already, expired or been withdrawn. Ask the person who invited you for a new one.",
	}})
}

func (s *server) invitePage(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	inv, err := s.Flow.OpenInvite(r.Context(), tok)
	if err != nil {
		s.unusable(w)
		return
	}
	th, _ := flow.TokenHash(tok)
	s.Render.page(w, 200, "invite.html", view{Title: "You have been invited", CSRF: inviteeCSRF(s.SessionKey, th),
		Data: inviteData{Inviter: inv.InviterUsername, CoC: s.Flow.CoC().HTML}})
}

func (s *server) inviteSubmit(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	th, ok := flow.TokenHash(tok)
	if !ok {
		s.unusable(w)
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.PostFormValue("csrf")), []byte(inviteeCSRF(s.SessionKey, th))) != 1 {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	a := flow.Applicant{
		Username: r.PostFormValue("username"), Email: r.PostFormValue("email"),
		DisplayName: r.PostFormValue("display_name"), Agreed: r.PostFormValue("agree") == "yes",
	}
	now, err := s.Flow.Apply(r.Context(), tok, a)
	switch {
	case errors.Is(err, store.ErrNotUsable):
		s.unusable(w)
	case errors.Is(err, flow.ErrInvalidUsername), errors.Is(err, flow.ErrInvalidEmail), errors.Is(err, flow.ErrInvalidName),
		errors.Is(err, flow.ErrUsernameTaken), errors.Is(err, flow.ErrEmailTaken), errors.Is(err, flow.ErrNotAgreed):
		inv, oerr := s.Flow.OpenInvite(r.Context(), tok)
		if oerr != nil {
			s.unusable(w)
			return
		}
		s.Render.page(w, http.StatusUnprocessableEntity, "invite.html", view{Title: "You have been invited",
			CSRF: inviteeCSRF(s.SessionKey, th), Error: capitalise(validationSentence(err)),
			Data: inviteData{Inviter: inv.InviterUsername, CoC: s.Flow.CoC().HTML, Username: a.Username, DisplayName: a.DisplayName, Email: a.Email}})
	case err != nil:
		s.fail(w, err)
	case now:
		s.Render.page(w, 200, "message.html", view{Title: "Welcome", Data: map[string]string{
			"Heading": "Welcome", "Body": "Check your email — your sign-in link is on its way."}})
	default:
		s.Render.page(w, 200, "message.html", view{Title: "Thank you", Data: map[string]string{
			"Heading": "Thank you", "Body": "An admin will review your application, and you will get an email."}})
	}
}

// capitalise starts a flow error sentence with a capital; they are ASCII.
func capitalise(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

var validationErrors = []error{flow.ErrInvalidUsername, flow.ErrInvalidEmail, flow.ErrInvalidName,
	flow.ErrUsernameTaken, flow.ErrEmailTaken, flow.ErrNotAgreed}

// validationSentence returns the flow sentinel's own text, never a wrapper's.
func validationSentence(err error) string {
	for _, e := range validationErrors {
		if errors.Is(err, e) {
			return e.Error()
		}
	}
	return ""
}
