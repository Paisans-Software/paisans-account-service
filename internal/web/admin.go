package web

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/paisans-software/paisans-invite/internal/flow"
	"github.com/paisans-software/paisans-invite/internal/store"
)

// Error text on the admin page is chosen by the server from a fixed code, so a
// crafted link cannot put words in the banner.
var decisionErrors = map[string]string{
	"username_taken": "That username is now taken. Decline the application, or ask the person to apply again with another.",
	"email_taken":    "An account already uses that email address.",
	"not_found":      "That application no longer exists.",
	"busy":           "Someone else is deciding this application; refresh in a moment.",
	"forbidden":      "Only admins can do that.",
	"failed":         "That did not work. The identity provider or database may be unavailable; try again.",
}

func decisionCode(err error) string {
	switch {
	case errors.Is(err, flow.ErrUsernameTaken):
		return "username_taken"
	case errors.Is(err, flow.ErrEmailTaken):
		return "email_taken"
	case errors.Is(err, store.ErrNotFound):
		return "not_found"
	case errors.Is(err, store.ErrBusy):
		return "busy"
	case errors.Is(err, flow.ErrForbidden):
		return "forbidden"
	}
	return "failed"
}

func (s *server) queue(w http.ResponseWriter, r *http.Request, sess Session) {
	p, err := s.Flow.Pending(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	v := view{Title: "Applications", Session: &sess, CSRF: sess.CSRF, Data: p, Error: decisionErrors[r.URL.Query().Get("error")]}
	s.Render.page(w, 200, "admin.html", v)
}

func (s *server) decide(w http.ResponseWriter, r *http.Request, sess Session, approve bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if approve {
		err = s.Flow.Approve(r.Context(), sess.member(), id)
	} else {
		err = s.Flow.Decline(r.Context(), sess.member(), id)
	}
	if err != nil {
		slog.Warn("decision failed", "application_id", id, "error", err)
		http.Redirect(w, r, "/admin?error="+decisionCode(err), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (s *server) approve(w http.ResponseWriter, r *http.Request, sess Session) {
	s.decide(w, r, sess, true)
}
func (s *server) decline(w http.ResponseWriter, r *http.Request, sess Session) {
	s.decide(w, r, sess, false)
}
