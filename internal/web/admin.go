package web

import (
	"net/http"
	"net/url"
	"strconv"
)

func (s *server) queue(w http.ResponseWriter, r *http.Request, sess Session) {
	p, err := s.Flow.Pending(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	v := view{Title: "Applications", Session: &sess, CSRF: sess.CSRF, Data: p, Error: r.URL.Query().Get("error")}
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
		// The message is the flow's own sentence, never the provider's body;
		// pass it through a query string the template escapes.
		http.Redirect(w, r, "/admin?error="+urlQuery(err.Error()), http.StatusSeeOther)
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

func urlQuery(s string) string { return url.QueryEscape(s) }
