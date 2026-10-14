package web

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/paisans-software/paisans-account-service/internal/flow"
	"github.com/paisans-software/paisans-account-service/internal/store"
)

type homeData struct {
	// InvitesOn shows the invite cards; with invites off the page shows
	// only who is signed in.
	InvitesOn        bool
	Invites          []store.Invite
	NewLink, Expires string
	Remaining        string
	// Nonce is the create form's one-time value, fresh on every render.
	Nonce string
}

const alreadyMade = "That invite was already made. It's in your list below. If you didn't copy the link, revoke it and make a new one."

func (s *server) homeView(r *http.Request, sess Session, extra homeData) (homeData, error) {
	if !s.Invites {
		return homeData{}, nil
	}
	extra.InvitesOn = true
	invites, err := s.Flow.Invites(r.Context(), sess.member())
	if err != nil {
		return homeData{}, err
	}
	extra.Invites = invites
	extra.Nonce = flow.NewNonce()
	if sess.Admin {
		extra.Remaining = "As an admin you can make as many invites as you need."
	} else {
		live := 0
		for _, i := range invites {
			if i.State == "open" || i.State == "applied" {
				live++
			}
		}
		extra.Remaining = fmt.Sprintf("You have %d invite(s) in use of your %d.", live, s.Flow.Quota())
	}
	return extra, nil
}

func (s *server) home(w http.ResponseWriter, r *http.Request, sess Session) {
	d, err := s.homeView(r, sess, homeData{})
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Render.page(w, 200, "home.html", view{Title: "Account", Session: &sess, CSRF: sess.CSRF, Data: d})
}

func (s *server) createInvite(w http.ResponseWriter, r *http.Request, sess Session) {
	link, inv, err := s.Flow.CreateInvite(r.Context(), sess.member(), r.PostFormValue("nonce"))
	v := view{Title: "Account", Session: &sess, CSRF: sess.CSRF}
	var extra homeData
	switch {
	case errors.Is(err, flow.ErrBadNonce):
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	case errors.Is(err, store.ErrDuplicateCreate):
		v.Error = alreadyMade
	case errors.Is(err, store.ErrQuota):
		v.Error = "You have reached your invite quota. An invite frees up when it is used, expires or is revoked."
	case err != nil:
		s.fail(w, err)
		return
	default:
		extra = homeData{NewLink: link, Expires: inv.ExpiresAt.Format("2 Jan 2006")}
	}
	d, err := s.homeView(r, sess, extra)
	if err != nil {
		s.fail(w, err)
		return
	}
	v.Data = d
	s.Render.page(w, 200, "home.html", v)
}

func (s *server) revoke(w http.ResponseWriter, r *http.Request, sess Session) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	if err := s.Flow.Revoke(r.Context(), sess.member(), id); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// fail answers a request the service could not complete, almost always because
// the database is unreachable: 503, and a page asking the person to retry.
func (s *server) fail(w http.ResponseWriter, err error) {
	slog.Error("request failed", "error", err)
	s.Render.page(w, http.StatusServiceUnavailable, "message.html", view{Title: "Something went wrong", Data: map[string]string{
		"Heading": "Something went wrong", "Body": "Please try again in a minute."}})
}
