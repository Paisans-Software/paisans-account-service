// Package idptest is an in-memory identity provider for tests.
package idptest

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/paisans-software/paisans-account-service/internal/idp"
)

var _ idp.Provider = (*Fake)(nil)

type Fake struct {
	mu    sync.Mutex
	Users []idp.User
	// Groups maps a group name to the usernames of its members.
	Groups                   map[string][]string
	FailCreate, FailSendLink error
	// FailLookup makes every read fail, as an unreachable provider would.
	FailLookup error
	// FailUpdate makes every update fail.
	FailUpdate error
	// LinksTo lists the user IDs each sign-in link was sent to, in order.
	LinksTo            []string
	Created, LinksSent int
	// OnCreate, if set, runs at the start of CreateUser, outside the lock, so
	// a test can act while an approval is in progress.
	OnCreate func()
	// AfterCreate, if set, runs once CreateUser has made the account, outside
	// the lock, so a test can act between the account and its recording.
	AfterCreate func()
	// OnSendLink, if set, runs at the start of SendLoginLink, outside the
	// lock, so a test can act while a link is being sent.
	OnSendLink func()
	// AfterSendLink, if set, runs once SendLoginLink has sent, outside the lock.
	AfterSendLink func()
	// FriendlyNames maps a group name to its display name; a group without
	// one is shown by its name. Every key of Groups is a group, ID "g-"+name.
	FriendlyNames map[string]string
	// OnChangeGroups, if set, runs once ChangeGroups has made its fresh read
	// and before it writes, outside the lock.
	OnChangeGroups func()
	// FailGroupReadBack makes ChangeGroups write and then fail to read back.
	FailGroupReadBack error
	// GroupWrites counts the group writes ChangeGroups made.
	GroupWrites int
}

func (f *Fake) UsernameTaken(_ context.Context, n string) (bool, error) {
	_, ok, err := f.FindUser(context.Background(), n)
	return ok, err
}

func (f *Fake) EmailTaken(_ context.Context, e string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FailLookup != nil {
		return false, f.FailLookup
	}
	for _, u := range f.Users {
		if strings.EqualFold(u.Email, strings.TrimSpace(e)) {
			return true, nil
		}
	}
	return false, nil
}

func (f *Fake) FindUser(_ context.Context, n string) (idp.User, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FailLookup != nil {
		return idp.User{}, false, f.FailLookup
	}
	for _, u := range f.Users {
		if strings.EqualFold(u.Username, n) {
			return u, true, nil
		}
	}
	return idp.User{}, false, nil
}

func (f *Fake) AdminGroupEmails(_ context.Context, group string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FailLookup != nil {
		return nil, f.FailLookup
	}
	var out []string
	for _, name := range f.Groups[group] {
		for _, u := range f.Users {
			if u.Username == name && u.Email != "" {
				out = append(out, u.Email)
			}
		}
	}
	return out, nil
}

func (f *Fake) CreateUser(_ context.Context, n idp.NewUser) (idp.User, error) {
	if f.OnCreate != nil {
		f.OnCreate()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FailCreate != nil {
		return idp.User{}, f.FailCreate
	}
	f.Created++
	u := idp.User{ID: fmt.Sprintf("id-%d", f.Created), Username: n.Username, Email: strings.TrimSpace(n.Email), DisplayName: n.DisplayName}
	f.Users = append(f.Users, u)
	if after := f.AfterCreate; after != nil {
		f.mu.Unlock()
		after()
		f.mu.Lock()
	}
	return u, nil
}

func (f *Fake) SendLoginLink(_ context.Context, id string) error {
	if f.OnSendLink != nil {
		f.OnSendLink()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FailSendLink != nil {
		return f.FailSendLink
	}
	f.LinksSent++
	f.LinksTo = append(f.LinksTo, id)
	if after := f.AfterSendLink; after != nil {
		f.mu.Unlock()
		after()
		f.mu.Lock()
	}
	return nil
}

func (f *Fake) ListUsers(context.Context) ([]idp.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FailLookup != nil {
		return nil, f.FailLookup
	}
	return append([]idp.User(nil), f.Users...), nil
}

func (f *Fake) GetUser(_ context.Context, id string) (idp.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FailLookup != nil {
		return idp.User{}, f.FailLookup
	}
	for _, u := range f.Users {
		if u.ID == id {
			return u, nil
		}
	}
	return idp.User{}, idp.ErrNotFound
}

// User returns the user with id as the fake holds it now.
func (f *Fake) User(id string) idp.User {
	u, _ := f.GetUser(context.Background(), id)
	return u
}

func (f *Fake) update(id string, change func(*idp.User)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FailUpdate != nil {
		return f.FailUpdate
	}
	for i := range f.Users {
		if f.Users[i].ID == id {
			change(&f.Users[i])
			return nil
		}
	}
	return idp.ErrNotFound
}

func (f *Fake) SetDisabled(_ context.Context, id string, disabled bool) error {
	return f.update(id, func(u *idp.User) { u.Disabled = disabled })
}

func (f *Fake) SetEmail(_ context.Context, id, email string) error {
	return f.update(id, func(u *idp.User) { u.Email = strings.TrimSpace(email) })
}

func (f *Fake) SetName(_ context.Context, id string, n idp.Name) error {
	return f.update(id, func(u *idp.User) {
		u.DisplayName, u.FirstName, u.LastName = n.DisplayName, n.FirstName, n.LastName
	})
}

// GroupID is the ID the fake gives the group named name.
func GroupID(name string) string { return "g-" + name }

func (f *Fake) group(name string) idp.Group {
	friendly := f.FriendlyNames[name]
	if friendly == "" {
		friendly = name
	}
	return idp.Group{ID: GroupID(name), Name: name, FriendlyName: friendly}
}

func (f *Fake) ListGroups(context.Context) ([]idp.Group, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FailLookup != nil {
		return nil, f.FailLookup
	}
	names := make([]string, 0, len(f.Groups))
	for n := range f.Groups {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]idp.Group, 0, len(names))
	for _, n := range names {
		out = append(out, f.group(n))
	}
	return out, nil
}

// userGroups is the groups of the user with id, sorted by name; f.mu is held.
func (f *Fake) userGroups(id string) ([]idp.Group, string, error) {
	username := ""
	for _, u := range f.Users {
		if u.ID == id {
			username = u.Username
		}
	}
	if username == "" {
		return nil, "", idp.ErrNotFound
	}
	var names []string
	for n, members := range f.Groups {
		if slices.Contains(members, username) {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	out := []idp.Group{}
	for _, n := range names {
		out = append(out, f.group(n))
	}
	return out, username, nil
}

func (f *Fake) UserGroups(_ context.Context, id string) ([]idp.Group, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FailLookup != nil {
		return nil, f.FailLookup
	}
	gs, _, err := f.userGroups(id)
	return gs, err
}

// In reports whether the user named username is in the group named group.
func (f *Fake) In(username, group string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.Groups[group], username)
}

// SetIn puts the user named username in the group named group, or takes them
// out, as another admin or the operator reconciler would.
func (f *Fake) SetIn(username, group string, in bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Groups[group] = slices.DeleteFunc(f.Groups[group], func(m string) bool { return m == username })
	if in {
		f.Groups[group] = append(f.Groups[group], username)
	}
}

// ChangeGroups behaves as the Pocket ID client's: a fresh read, the read
// list plus add minus remove, the same refusals, then the write.
func (f *Fake) ChangeGroups(_ context.Context, id string, add, remove []string) (idp.GroupChange, error) {
	f.mu.Lock()
	if f.FailLookup != nil {
		f.mu.Unlock()
		return idp.GroupChange{}, f.FailLookup
	}
	if _, _, err := f.userGroups(id); err != nil {
		f.mu.Unlock()
		return idp.GroupChange{}, err
	}
	f.mu.Unlock()
	if f.OnChangeGroups != nil {
		f.OnChangeGroups()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	before, username, err := f.userGroups(id)
	if err != nil {
		return idp.GroupChange{}, err
	}
	name := func(gid string) (string, bool) {
		n, ok := strings.CutPrefix(gid, "g-")
		if _, exists := f.Groups[n]; !ok || !exists {
			return "", false
		}
		return n, true
	}
	in := map[string]bool{}
	for _, g := range before {
		in[g.Name] = true
	}
	next := map[string]bool{}
	for n := range in {
		next[n] = true
	}
	for _, gid := range add {
		n, ok := name(gid)
		if !ok {
			return idp.GroupChange{}, idp.ErrNoSuchGroup
		}
		if !slices.Contains(remove, gid) {
			next[n] = true
		}
	}
	for _, gid := range remove {
		if n, ok := name(gid); ok {
			delete(next, n)
		}
	}
	if next[idp.OperatorsGroup] != in[idp.OperatorsGroup] {
		return idp.GroupChange{}, idp.ErrOperatorsGroup
	}
	if f.FailUpdate != nil {
		return idp.GroupChange{}, f.FailUpdate
	}
	var ch idp.GroupChange
	for n := range f.Groups {
		switch {
		case next[n] && !in[n]:
			f.Groups[n] = append(f.Groups[n], username)
			ch.Added = append(ch.Added, f.group(n))
		case !next[n] && in[n]:
			f.Groups[n] = slices.DeleteFunc(f.Groups[n], func(m string) bool { return m == username })
			ch.Removed = append(ch.Removed, f.group(n))
		}
	}
	byName := func(a, b idp.Group) int { return strings.Compare(a.Name, b.Name) }
	slices.SortFunc(ch.Added, byName)
	slices.SortFunc(ch.Removed, byName)
	f.GroupWrites++
	if f.FailGroupReadBack != nil {
		return ch, fmt.Errorf("%w: %w", idp.ErrUnconfirmed, f.FailGroupReadBack)
	}
	return ch, nil
}
