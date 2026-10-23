package web

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strings"

	"github.com/paisans-software/paisans-account-service/internal/flow"
	"github.com/paisans-software/paisans-account-service/internal/store"
)

// inviteData is invite.html's data, for an invite or, with Open, for open
// registration.
type inviteData struct {
	Open                         bool
	Inviter                      string
	CoC                          template.HTML
	Username, DisplayName, Email string
	FirstName, LastName          string
}

func (s *server) unusable(w http.ResponseWriter) {
	s.Render.page(w, http.StatusNotFound, "message.html", view{Title: "Invite", Data: map[string]string{
		"Heading": "This invite can't be used",
		"Body":    "It may have been used already, expired or been withdrawn. Ask the person who invited you for a new one.",
	}})
}

// openFailed answers a failed OpenInvite: the neutral page only when the invite
// is not usable, and a 503 for anything else (the database, say).
func (s *server) openFailed(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotUsable) {
		s.unusable(w)
		return
	}
	s.fail(w, err)
}

func (s *server) invitePage(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	inv, err := s.Flow.OpenInvite(r.Context(), tok)
	if err != nil {
		s.openFailed(w, err)
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
		DisplayName: r.PostFormValue("display_name"),
		FirstName:   r.PostFormValue("first_name"), LastName: r.PostFormValue("last_name"), Agreed: r.PostFormValue("agree") == "yes",
	}
	now, err := s.Flow.Apply(r.Context(), tok, a)
	switch {
	case errors.Is(err, store.ErrNotUsable):
		s.unusable(w)
	case validationSentence(err) != "":
		inv, oerr := s.Flow.OpenInvite(r.Context(), tok)
		if oerr != nil {
			s.openFailed(w, oerr)
			return
		}
		s.Render.page(w, http.StatusUnprocessableEntity, "invite.html", view{Title: "You have been invited",
			CSRF: inviteeCSRF(s.SessionKey, th), Error: capitalise(validationSentence(err)),
			Data: inviteData{Inviter: inv.InviterUsername, CoC: s.Flow.CoC().HTML, Username: a.Username, DisplayName: a.DisplayName, Email: a.Email,
				FirstName: a.FirstName, LastName: a.LastName}})
	case err != nil:
		s.fail(w, err)
	default:
		s.submitted(w, now)
	}
}

// submitted answers an application the flow accepted, or refused for its
// address, alike: now is whether the account was created at once.
func (s *server) submitted(w http.ResponseWriter, now bool) {
	if now {
		idp := s.Render.idp
		s.Render.page(w, 200, "message.html", view{Title: "Check your email", Data: messageData{
			Heading: "Check your email", Body: "We've sent you a sign-in link. " + expiresIn(s.Flow.LoginLinkDays()), Recovery: &idp}})
		return
	}
	s.Render.page(w, 200, "message.html", view{Title: "Thanks for asking to join", Data: messageData{
		Heading: "Thanks for asking to join", Body: "An admin will review your request. Once you're approved, we'll email you a sign-in link."}})
}

// expiresIn states the sign-in link's lifetime as a sentence.
func expiresIn(days int) string {
	if days == 1 {
		return "It expires in 1 day."
	}
	return fmt.Sprintf("It expires in %d days.", days)
}

// capitalise starts a flow error sentence with a capital; they are ASCII.
func capitalise(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

var validationErrors = []error{flow.ErrInvalidUsername, flow.ErrInvalidEmail, flow.ErrInvalidName,
	flow.ErrInvalidFirst, flow.ErrInvalidLast, flow.ErrUsernameTaken, flow.ErrNotAgreed}

// validationSentence returns the flow sentinel's own text, never a wrapper's.
func validationSentence(err error) string {
	for _, e := range validationErrors {
		if errors.Is(err, e) {
			return e.Error()
		}
	}
	return ""
}
