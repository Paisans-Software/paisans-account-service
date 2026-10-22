package web

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"

	"github.com/paisans-software/paisans-account-service/internal/flow"
	"github.com/paisans-software/paisans-account-service/internal/idp"
	"github.com/paisans-software/paisans-account-service/internal/store"
)

// sessionNotice is what a ban cannot do, said on the page that confirms it.
const sessionNotice = "Pocket ID refuses their next sign-in, but an app that keeps its own session keeps them signed in until that session expires."

// userErrors are the flow's errors a person can act on. Each page shows the
// error's own fixed text; anything else is a 503.
var userErrors = []error{
	flow.ErrOperator, flow.ErrSelf, flow.ErrReasonRequired, flow.ErrMemberBanned, flow.ErrNoSuchMember,
	flow.ErrInvalidEmail, flow.ErrSameAddress, flow.ErrCodeWrong, flow.ErrCodeExpired, flow.ErrAddressRefused,
	flow.ErrInvalidNames, flow.ErrForbidden, flow.ErrOperatorsGroup, flow.ErrNoSuchGroup, flow.ErrNoGroupChange,
}

// userError returns the sentence for err, or "" if it is not one a person
// can act on. The text is the sentinel's own, never a wrapper's.
func userError(err error) string {
	for _, e := range userErrors {
		if errors.Is(err, e) {
			return capitalise(e.Error()) + "."
		}
	}
	switch {
	case errors.Is(err, store.ErrAlreadyBanned):
		return "That member is already banned."
	case errors.Is(err, store.ErrSameAdmin):
		return "You opened this request, so another admin must approve it."
	case errors.Is(err, store.ErrBusy):
		return "That member already has a recovery request waiting, or another admin is approving it; refresh in a moment."
	case errors.Is(err, store.ErrNotFound), errors.Is(err, idp.ErrNotFound):
		return "That no longer exists, or is no longer waiting."
	}
	return ""
}

// done is the notice a redirect after a write names by a fixed code, so a
// crafted link cannot put words on the page.
var done = map[string]string{
	"lifted":    "The ban is lifted and the member can sign in again.",
	"opened":    "The request is open. Another admin must approve it within 48 hours.",
	"approved":  "The new address is set and Pocket ID has emailed a sign-in link to it.",
	"recovered": "The account is recovered: the new address is set and Pocket ID has emailed a sign-in link to it.",
	"cancelled": "The request is cancelled.",
	"email":     "Your email address is changed.",
	"name":      "Your name is changed.",
}

func (s *server) id(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil
}

// --- members and bans ---

// membersData is one page of the members list, with the links that sort and
// page it built from the query as applied, so a link never carries a value
// the flow refused.
type membersData struct{ flow.MembersPage }

// link is the members list with q, sorted by column, desc and at page, each
// left out when it is the default.
func (d membersData) link(column string, desc bool, page int) string {
	v := url.Values{}
	if d.Q != "" {
		v.Set("q", d.Q)
	}
	if column != flow.SortName {
		v.Set("sort", column)
	}
	if desc {
		v.Set("dir", "desc")
	}
	if page > 1 {
		v.Set("page", strconv.Itoa(page))
	}
	if len(v) == 0 {
		return "/admin/members"
	}
	return "/admin/members?" + v.Encode()
}

// SortLink sorts by column from the first page: ascending, or reversed if it
// is the column already sorted ascending.
func (d membersData) SortLink(column string) string {
	return d.link(column, column == d.Sort && !d.Desc, 1)
}

// AriaSort is column's aria-sort value.
func (d membersData) AriaSort(column string) string {
	switch {
	case column != d.Sort:
		return "none"
	case d.Desc:
		return "descending"
	}
	return "ascending"
}

// Arrow marks the sorted column's header with its direction.
func (d membersData) Arrow(column string) string {
	switch {
	case column != d.Sort:
		return ""
	case d.Desc:
		return " ↓"
	}
	return " ↑"
}

// PrevLink and NextLink are the neighbouring pages, or "" at either end.
func (d membersData) PrevLink() string {
	if d.Page <= 1 {
		return ""
	}
	return d.link(d.Sort, d.Desc, d.Page-1)
}

func (d membersData) NextLink() string {
	if d.Page >= d.Pages {
		return ""
	}
	return d.link(d.Sort, d.Desc, d.Page+1)
}

// Count says how many members the list holds, or how many match a search.
func (d membersData) Count() string {
	n := strconv.Itoa(d.Total)
	switch {
	case d.Q == "" && d.Total == 1:
		return "1 member"
	case d.Q == "":
		return n + " members"
	case d.Total == 1:
		return "1 member matches"
	}
	return n + " members match"
}

// members lists every user, or those matching q, sorted by sort and dir and
// cut to page. An unknown sort or a page that is not a number is the
// default; the flow clamps the rest.
func (s *server) members(w http.ResponseWriter, r *http.Request, sess Session) {
	v := r.URL.Query()
	page, err := strconv.Atoi(v.Get("page"))
	if err != nil {
		page = 1
	}
	p, err := s.Flow.Members(r.Context(), sess.member(), flow.MembersQuery{
		Q: v.Get("q"), Sort: v.Get("sort"), Desc: v.Get("dir") == "desc", Page: page})
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Render.page(w, 200, "members.html", view{Title: "Members", Session: &sess, CSRF: sess.CSRF, Data: membersData{p}})
}

type banData struct {
	Member flow.MemberRow
	Reason string
	Notice string
}

func (s *server) banPage(w http.ResponseWriter, r *http.Request, sess Session) {
	s.banForm(w, r, sess, http.StatusOK, "", "")
}

func (s *server) banForm(w http.ResponseWriter, r *http.Request, sess Session, status int, errText, reason string) {
	row, err := s.Flow.MemberByID(r.Context(), sess.member(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Render.page(w, status, "ban.html", view{Title: "Ban " + row.Username, Session: &sess, CSRF: sess.CSRF, Error: errText,
		Data: banData{Member: row, Reason: reason, Notice: sessionNotice}})
}

func (s *server) banSubmit(w http.ResponseWriter, r *http.Request, sess Session) {
	why := r.PostFormValue("reason")
	b, err := s.Flow.Ban(r.Context(), sess.member(), r.PathValue("id"), why)
	if msg := userError(err); msg != "" {
		s.banForm(w, r, sess, http.StatusUnprocessableEntity, msg, why)
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Render.page(w, 200, "message.html", view{Title: "Banned", Session: &sess, Data: messageData{
		Heading: b.Username + " is banned", Body: "Their account is disabled and their address cannot sign up again. " + sessionNotice}})
}

type bansData struct{ Bans []store.Ban }

func (s *server) bans(w http.ResponseWriter, r *http.Request, sess Session) {
	list, err := s.Flow.Bans(r.Context(), sess.member())
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Render.page(w, 200, "bans.html", view{Title: "Bans", Session: &sess, CSRF: sess.CSRF,
		Notice: done[r.URL.Query().Get("done")], Error: errorCodes[r.URL.Query().Get("error")], Data: bansData{Bans: list}})
}

// errorCodes are the errors a redirect may name, by fixed code.
var errorCodes = map[string]string{
	"not_found": "That no longer exists, or is no longer waiting.",
	"same":      "You opened this request, so another admin must approve it.",
	"self":      "You can't do that to your own account.",
	"operator":  "Operators can only be managed in the Paisan Stack deployment configuration.",
	"refused":   "That address is now used by another account or matches a ban; cancel this request and open a new one.",
	"banned":    "That member is banned; lift the ban first.",
	"busy":      "Another admin is approving this request; refresh in a moment.",
	"gone":      "That member no longer exists.",
}

func errorCode(err error) string {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return "not_found"
	case errors.Is(err, store.ErrSameAdmin):
		return "same"
	case errors.Is(err, flow.ErrSelf):
		return "self"
	case errors.Is(err, flow.ErrOperator):
		return "operator"
	case errors.Is(err, flow.ErrAddressRefused):
		return "refused"
	case errors.Is(err, flow.ErrMemberBanned):
		return "banned"
	case errors.Is(err, store.ErrBusy):
		return "busy"
	case errors.Is(err, flow.ErrNoSuchMember):
		return "gone"
	}
	return ""
}

// after redirects to path, naming the outcome of a write by a fixed code. An
// error with no code is the service failing, a 503.
func (s *server) after(w http.ResponseWriter, r *http.Request, path, ok string, err error) {
	if err == nil {
		http.Redirect(w, r, path+"?done="+ok, http.StatusSeeOther)
		return
	}
	code := errorCode(err)
	if code == "" {
		s.fail(w, err)
		return
	}
	slog.Info("request refused", "path", r.URL.Path, "reason", code)
	http.Redirect(w, r, path+"?error="+code, http.StatusSeeOther)
}

func (s *server) lift(w http.ResponseWriter, r *http.Request, sess Session) {
	id, ok := s.id(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	_, err := s.Flow.Lift(r.Context(), sess.member(), id)
	s.after(w, r, "/admin/bans", "lifted", err)
}

// --- recovery ---

type recoveryData struct {
	Rows                    []flow.RecoveryRow
	Username, Email, Reason string
	Sub                     string
	// Immediate is a viewer who is the sole operator-admin: their recovery
	// happens on submit, with no second admin.
	Immediate bool
}

func (s *server) recoveryView(w http.ResponseWriter, r *http.Request, sess Session, status int, errText string, d recoveryData) {
	rows, err := s.Flow.Recoveries(r.Context(), sess.member())
	if err != nil {
		s.fail(w, err)
		return
	}
	d.Rows, d.Sub = rows, sess.Sub
	// A failed read shows the two-admin wording, which is what opening a
	// request then does.
	d.Immediate, _ = s.Flow.SoleOperatorAdmin(r.Context(), sess.member())
	if errText == "" {
		errText = errorCodes[r.URL.Query().Get("error")]
	}
	s.Render.page(w, status, "recovery.html", view{Title: "Recovery", Session: &sess, CSRF: sess.CSRF,
		Notice: done[r.URL.Query().Get("done")], Error: errText, Data: d})
}

func (s *server) recoveryPage(w http.ResponseWriter, r *http.Request, sess Session) {
	s.recoveryView(w, r, sess, http.StatusOK, "", recoveryData{})
}

func (s *server) recoveryOpen(w http.ResponseWriter, r *http.Request, sess Session) {
	d := recoveryData{Username: r.PostFormValue("username"), Email: r.PostFormValue("email"), Reason: r.PostFormValue("reason")}
	rq, err := s.Flow.OpenRecovery(r.Context(), sess.member(), d.Username, d.Email, d.Reason)
	if msg := userError(err); msg != "" {
		s.recoveryView(w, r, sess, http.StatusUnprocessableEntity, msg, d)
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	if rq.State == "approved" {
		http.Redirect(w, r, "/admin/recovery?done=recovered", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/recovery?done=opened", http.StatusSeeOther)
}

func (s *server) recoveryApprove(w http.ResponseWriter, r *http.Request, sess Session) {
	id, ok := s.id(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.after(w, r, "/admin/recovery", "approved", s.Flow.ApproveRecovery(r.Context(), sess.member(), id))
}

func (s *server) recoveryCancel(w http.ResponseWriter, r *http.Request, sess Session) {
	id, ok := s.id(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.after(w, r, "/admin/recovery", "cancelled", s.Flow.CancelRecovery(r.Context(), sess.member(), id))
}

// --- the member's own address and name ---

type emailData struct {
	Current, Pending, New string
	Operator              bool
}

func (s *server) emailView(w http.ResponseWriter, r *http.Request, sess Session, status int, errText, newAddr string) {
	u, op, err := s.Flow.Profile(r.Context(), sess.member())
	if err != nil {
		s.fail(w, err)
		return
	}
	d := emailData{Current: u.Email, New: newAddr, Operator: op}
	if !op {
		if d.Pending, _, err = s.Flow.PendingEmailChange(r.Context(), sess.member()); err != nil {
			s.fail(w, err)
			return
		}
	}
	s.Render.page(w, status, "email.html", view{Title: "Email address", Session: &sess, CSRF: sess.CSRF,
		Notice: done[r.URL.Query().Get("done")], Error: errText, Data: d})
}

func (s *server) emailPage(w http.ResponseWriter, r *http.Request, sess Session) {
	s.emailView(w, r, sess, http.StatusOK, "", "")
}

func (s *server) emailRequest(w http.ResponseWriter, r *http.Request, sess Session) {
	addr := r.PostFormValue("email")
	err := s.Flow.RequestEmailChange(r.Context(), sess.member(), addr)
	if msg := userError(err); msg != "" {
		s.emailView(w, r, sess, http.StatusUnprocessableEntity, msg, addr)
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/account/email", http.StatusSeeOther)
}

func (s *server) emailConfirm(w http.ResponseWriter, r *http.Request, sess Session) {
	err := s.Flow.ConfirmEmailChange(r.Context(), sess.member(), r.PostFormValue("code"))
	if msg := userError(err); msg != "" {
		s.emailView(w, r, sess, http.StatusUnprocessableEntity, msg, "")
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/account/email?done=email", http.StatusSeeOther)
}

type nameData struct {
	Name     idp.Name
	Username string
	Operator bool
}

func (s *server) nameView(w http.ResponseWriter, r *http.Request, sess Session, status int, errText string, n *idp.Name) {
	u, op, err := s.Flow.Profile(r.Context(), sess.member())
	if err != nil {
		s.fail(w, err)
		return
	}
	d := nameData{Name: idp.Name{DisplayName: u.DisplayName, FirstName: u.FirstName, LastName: u.LastName}, Username: u.Username, Operator: op}
	if n != nil {
		d.Name = *n
	}
	s.Render.page(w, status, "name.html", view{Title: "Name", Session: &sess, CSRF: sess.CSRF,
		Notice: done[r.URL.Query().Get("done")], Error: errText, Data: d})
}

func (s *server) namePage(w http.ResponseWriter, r *http.Request, sess Session) {
	s.nameView(w, r, sess, http.StatusOK, "", nil)
}

func (s *server) nameSubmit(w http.ResponseWriter, r *http.Request, sess Session) {
	n := idp.Name{DisplayName: r.PostFormValue("display_name"), FirstName: r.PostFormValue("first_name"), LastName: r.PostFormValue("last_name")}
	err := s.Flow.SetName(r.Context(), sess.member(), n)
	if msg := userError(err); msg != "" {
		s.nameView(w, r, sess, http.StatusUnprocessableEntity, msg, &n)
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/account/name?done=name", http.StatusSeeOther)
}
