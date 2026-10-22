package flow

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/paisans-software/paisans-account-service/internal/emailhash"
	"github.com/paisans-software/paisans-account-service/internal/idp"
	"github.com/paisans-software/paisans-account-service/internal/store"
)

var (
	// ErrOperator is an action the service refuses for an operator, who is
	// managed in the deployment's configuration instead.
	ErrOperator = errors.New("operators can only be managed in the Paisan Stack deployment configuration")
	// ErrSelf is an admin acting on their own account where another must.
	ErrSelf = errors.New("you can't do that to your own account")
	// ErrReasonRequired is a ban or recovery without its reason.
	ErrReasonRequired = errors.New("a reason of up to 1000 characters is required")
	// ErrMemberBanned is a recovery for a member who is banned.
	ErrMemberBanned = errors.New("that member is banned; lift the ban first")
)

// maxReason bounds the free text an admin gives for a ban or a recovery.
const maxReason = 1000

// IsOperator reports whether username is one of the deployment's operators.
func (s *Service) IsOperator(username string) bool {
	return slices.ContainsFunc(s.s.Operators, func(o string) bool { return strings.EqualFold(o, username) })
}

func reason(r string) (string, error) {
	r = strings.TrimSpace(r)
	if r == "" || utf8.RuneCountInString(r) > maxReason {
		return "", ErrReasonRequired
	}
	return r, nil
}

// MemberRow is one user as the admins' pages show them.
type MemberRow struct {
	idp.User
	// Operator and Self are users the service will not ban.
	Operator, Self bool
	// Ban is the user's current ban, or nil.
	Ban *store.Ban
}

// Bannable reports whether the row may be offered a ban.
func (r MemberRow) Bannable() bool { return !r.Operator && !r.Self && r.Ban == nil }

func (s *Service) row(u idp.User, admin Member, bans map[string]store.Ban) MemberRow {
	r := MemberRow{User: u, Operator: s.IsOperator(u.Username), Self: u.ID == admin.Sub}
	if b, ok := bans[u.ID]; ok {
		r.Ban = &b
	}
	return r
}

// Status is the row's state: banned, disabled at the identity provider, or
// active. A ban outranks the provider's flag.
func (r MemberRow) Status() string {
	switch {
	case r.Ban != nil:
		return "banned"
	case r.Disabled:
		return "disabled"
	}
	return "active"
}

// The columns the members list sorts by.
const (
	SortName     = "name"
	SortUsername = "username"
	SortEmail    = "email"
	SortStatus   = "status"
)

// MembersPerPage is how many rows one page of the members list holds.
const MembersPerPage = 50

// maxQuery bounds a member search: the longest an email address can be.
const maxQuery = 254

// MembersQuery is one page of the members list as asked for.
type MembersQuery struct {
	// Q keeps the users whose username, display name or email contains it,
	// ignoring case; blank keeps everyone.
	Q string
	// Sort is one of the Sort columns; any other value is SortName. Desc
	// reverses the order.
	Sort string
	Desc bool
	// Page counts from 1; a page outside the list is its nearest one.
	Page int
}

// MembersPage is one page of the members list: the query as applied, its
// rows, and how many pages and rows the whole list has.
type MembersPage struct {
	MembersQuery
	Rows         []MemberRow
	Pages, Total int
}

// statusRank orders Status values: active, then disabled, then banned.
var statusRank = map[string]int{"active": 0, "disabled": 1, "banned": 2}

// memberOrder compares two rows by column, then by name, username and ID,
// so that every order is total and a page never shifts between requests.
func memberOrder(column string) func(a, b MemberRow) int {
	name := func(r MemberRow) string {
		if strings.TrimSpace(r.DisplayName) == "" {
			return strings.ToLower(r.Username)
		}
		return strings.ToLower(r.DisplayName)
	}
	var first func(a, b MemberRow) int
	switch column {
	case SortUsername:
		first = func(a, b MemberRow) int {
			return strings.Compare(strings.ToLower(a.Username), strings.ToLower(b.Username))
		}
	case SortEmail:
		first = func(a, b MemberRow) int { return strings.Compare(strings.ToLower(a.Email), strings.ToLower(b.Email)) }
	case SortStatus:
		first = func(a, b MemberRow) int { return cmp.Compare(statusRank[a.Status()], statusRank[b.Status()]) }
	default:
		first = func(MemberRow, MemberRow) int { return 0 }
	}
	return func(a, b MemberRow) int {
		return cmp.Or(first(a, b), strings.Compare(name(a), name(b)),
			strings.Compare(strings.ToLower(a.Username), strings.ToLower(b.Username)), strings.Compare(a.ID, b.ID))
	}
}

// Members lists the users, filtered by q.Q, sorted by q.Sort and cut to page
// q.Page of MembersPerPage rows. Unknown or out-of-range values in q are
// replaced by the nearest valid ones, and the page returned says which were
// applied.
func (s *Service) Members(ctx context.Context, admin Member, q MembersQuery) (MembersPage, error) {
	if !admin.Admin {
		return MembersPage{}, ErrForbidden
	}
	q.Q = strings.TrimSpace(q.Q)
	if utf8.RuneCountInString(q.Q) > maxQuery {
		q.Q = string([]rune(q.Q)[:maxQuery])
	}
	switch q.Sort {
	case SortName, SortUsername, SortEmail, SortStatus:
	default:
		q.Sort = SortName
	}
	users, err := s.p.ListUsers(ctx)
	if err != nil {
		return MembersPage{}, err
	}
	bans, err := s.st.CurrentBans(ctx)
	if err != nil {
		return MembersPage{}, err
	}
	needle := strings.ToLower(q.Q)
	var all []MemberRow
	for _, u := range users {
		if strings.Contains(strings.ToLower(u.Username), needle) || strings.Contains(strings.ToLower(u.DisplayName), needle) ||
			strings.Contains(strings.ToLower(u.Email), needle) {
			all = append(all, s.row(u, admin, bans))
		}
	}
	order := memberOrder(q.Sort)
	if q.Desc {
		slices.SortFunc(all, func(a, b MemberRow) int { return order(b, a) })
	} else {
		slices.SortFunc(all, order)
	}
	p := MembersPage{Total: len(all), Pages: max(1, (len(all)+MembersPerPage-1)/MembersPerPage)}
	q.Page = min(max(q.Page, 1), p.Pages)
	p.MembersQuery = q
	from := (q.Page - 1) * MembersPerPage
	p.Rows = all[from:min(from+MembersPerPage, len(all))]
	return p, nil
}

// MemberByID is one user, for the ban confirmation page.
func (s *Service) MemberByID(ctx context.Context, admin Member, id string) (MemberRow, error) {
	if !admin.Admin {
		return MemberRow{}, ErrForbidden
	}
	u, err := s.p.GetUser(ctx, id)
	if errors.Is(err, idp.ErrNotFound) {
		return MemberRow{}, store.ErrNotFound
	}
	if err != nil {
		return MemberRow{}, err
	}
	bans, err := s.st.CurrentBans(ctx)
	if err != nil {
		return MemberRow{}, err
	}
	return s.row(u, admin, bans), nil
}

// Ban disables the user at the identity provider and records the ban, with
// the keyed hash of their address so that it cannot sign up again. Operators
// and the admin themselves are refused.
func (s *Service) Ban(ctx context.Context, admin Member, userID, why string) (store.Ban, error) {
	if !admin.Admin {
		return store.Ban{}, ErrForbidden
	}
	why, err := reason(why)
	if err != nil {
		return store.Ban{}, err
	}
	if userID == admin.Sub {
		return store.Ban{}, ErrSelf
	}
	row, err := s.MemberByID(ctx, admin, userID)
	if err != nil {
		return store.Ban{}, err
	}
	switch {
	case row.Operator:
		return store.Ban{}, ErrOperator
	case row.Ban != nil:
		return store.Ban{}, store.ErrAlreadyBanned
	}
	if err := s.p.SetDisabled(ctx, userID, true); err != nil {
		return store.Ban{}, err
	}
	// The user is disabled: what follows records it, and must not be cut
	// short by the request going away.
	ctx = context.WithoutCancel(ctx)
	var hmac []byte
	if strings.TrimSpace(row.Email) != "" {
		hmac = emailhash.Sum(s.s.EmailHashKey, row.Email)
	}
	b, err := s.st.RecordBan(ctx, userID, row.Username, hmac, why, admin.Sub, admin.Username)
	if err != nil {
		if errors.Is(err, store.ErrAlreadyBanned) {
			// Another admin banned them at the same moment; theirs stands.
			return store.Ban{}, err
		}
		// An unrecorded ban could never be lifted here, so it is undone.
		slog.Error("ban could not be recorded; re-enabling the user", "user_id", userID, "error", err)
		if uerr := s.p.SetDisabled(ctx, userID, false); uerr != nil {
			slog.Error("could not re-enable a user whose ban was not recorded; re-enable them at the identity provider", "user_id", userID, "error", uerr)
		}
		return store.Ban{}, err
	}
	slog.Info("write", "action", "ban.create", "ban_id", b.ID, "user_id", userID)
	return b, nil
}

// Lift re-enables a banned user and marks their ban lifted. Their groups were
// never touched, so everything they had returns.
func (s *Service) Lift(ctx context.Context, admin Member, banID int64) (store.Ban, error) {
	if !admin.Admin {
		return store.Ban{}, ErrForbidden
	}
	b, err := s.st.Ban(ctx, banID)
	if err != nil {
		return store.Ban{}, err
	}
	if !b.Current() {
		return store.Ban{}, store.ErrNotFound
	}
	if err := s.p.SetDisabled(ctx, b.UserID, false); err != nil {
		return store.Ban{}, err
	}
	b, err = s.st.LiftBan(context.WithoutCancel(ctx), banID, admin.Sub, admin.Username)
	if err != nil {
		return store.Ban{}, fmt.Errorf("the user is re-enabled but the ban is still recorded; lift it again: %w", err)
	}
	slog.Info("write", "action", "ban.lift", "ban_id", b.ID, "user_id", b.UserID)
	return b, nil
}

// Bans lists every ban, current and lifted.
func (s *Service) Bans(ctx context.Context, admin Member) ([]store.Ban, error) {
	if !admin.Admin {
		return nil, ErrForbidden
	}
	return s.st.Bans(ctx)
}
