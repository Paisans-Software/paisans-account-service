package web

import (
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-account-service/internal/flow"
	"github.com/paisans-software/paisans-account-service/internal/idp"
	"github.com/paisans-software/paisans-account-service/internal/store"
)

// sessionGroupsNotice is what a group change cannot do at once, said on the
// page that makes it and the page that confirms it.
const sessionGroupsNotice = "This service sees a change at once. Other apps see it at their next sign-in: an app session they already have keeps the privileges it was given until it expires."

// unconfirmedGroups is a group change sent to Pocket ID whose result could
// not be read back.
const unconfirmedGroups = "The change was sent to Pocket ID but could not be confirmed. The groups below are as Pocket ID has them now."

type groupsData struct {
	Page   flow.GroupsPage
	Notice string
}

func (s *server) groupsPage(w http.ResponseWriter, r *http.Request, sess Session) {
	s.groupsView(w, r, sess, http.StatusOK, "")
}

func (s *server) groupsView(w http.ResponseWriter, r *http.Request, sess Session, status int, errText string) {
	p, err := s.Flow.MemberGroups(r.Context(), sess.member(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Render.page(w, status, "groups.html", view{Title: "Groups of " + p.Member.Username, Session: &sess, CSRF: sess.CSRF, Error: errText,
		Data: groupsData{Page: p, Notice: sessionGroupsNotice}})
}

// without is the IDs in a that are not in b.
func without(a, b []string) []string {
	var out []string
	for _, id := range a {
		if !slices.Contains(b, id) {
			out = append(out, id)
		}
	}
	return out
}

// friendly lists groups by the names people read.
func friendly(gs []idp.Group) string {
	if len(gs) == 0 {
		return "none"
	}
	names := make([]string, 0, len(gs))
	for _, g := range gs {
		names = append(names, g.FriendlyName)
	}
	return strings.Join(names, ", ")
}

// groupsSubmit sends the groups to add and to remove: those checked now and
// not when the page loaded, and the reverse. A group the admin did not touch
// is left as Pocket ID has it at the moment of writing.
func (s *server) groupsSubmit(w http.ResponseWriter, r *http.Request, sess Session) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	checked, was := r.PostForm["group"], r.PostForm["was"]
	ch, err := s.Flow.ChangeGroups(r.Context(), sess.member(), r.PathValue("id"), without(checked, was), without(was, checked))
	if errors.Is(err, idp.ErrUnconfirmed) {
		s.groupsView(w, r, sess, http.StatusServiceUnavailable, unconfirmedGroups)
		return
	}
	if msg := userError(err); msg != "" {
		s.groupsView(w, r, sess, http.StatusUnprocessableEntity, msg)
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	p, err := s.Flow.MemberByID(r.Context(), sess.member(), r.PathValue("id"))
	who := "Their"
	if err == nil {
		who = p.Username + "'s"
	}
	s.Render.page(w, 200, "message.html", view{Title: "Groups changed", Session: &sess, Data: messageData{
		Heading: who + " groups are changed",
		Body:    "Added: " + friendly(ch.Added) + ". Removed: " + friendly(ch.Removed) + ". " + sessionGroupsNotice}})
}
