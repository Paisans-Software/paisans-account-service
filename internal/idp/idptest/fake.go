// Package idptest is an in-memory identity provider for tests.
package idptest

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/paisans-software/paisans-invite/internal/idp"
)

var _ idp.Provider = (*Fake)(nil)

type Fake struct {
	mu    sync.Mutex
	Users []idp.User
	// Groups maps a group name to the usernames of its members.
	Groups                   map[string][]string
	FailCreate, FailSendLink error
	// FailLookup makes every read fail, as an unreachable provider would.
	FailLookup         error
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
	u := idp.User{ID: fmt.Sprintf("id-%d", f.Created), Username: n.Username, Email: strings.TrimSpace(n.Email)}
	f.Users = append(f.Users, u)
	if after := f.AfterCreate; after != nil {
		f.mu.Unlock()
		after()
		f.mu.Lock()
	}
	return u, nil
}

func (f *Fake) SendLoginLink(context.Context, string) error {
	if f.OnSendLink != nil {
		f.OnSendLink()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FailSendLink != nil {
		return f.FailSendLink
	}
	f.LinksSent++
	return nil
}
