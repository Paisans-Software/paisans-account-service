// Package idp is the service's only view of the identity provider.
package idp

import (
	"context"
	"errors"
	"regexp"
)

// ErrNotFound is a user the provider does not have.
var ErrNotFound = errors.New("identity provider: no such user")

var UsernameRule = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9_.@-]*[a-zA-Z0-9])?$`)

const (
	MaxUsername    = 50
	MaxDisplayName = 100
)

type User struct {
	ID, Username, Email              string
	DisplayName, FirstName, LastName string
	Disabled                         bool
}

// Name is the part of a user a member may change: never the username.
type Name struct{ DisplayName, FirstName, LastName string }

const MaxFirstName, MaxLastName = 50, 50

type NewUser struct{ Username, Email, DisplayName string }

type Provider interface {
	UsernameTaken(ctx context.Context, username string) (bool, error)
	EmailTaken(ctx context.Context, email string) (bool, error)
	FindUser(ctx context.Context, username string) (User, bool, error)
	// AdminGroupEmails returns the email addresses of the members of the
	// named group: the service's admins are that group, not the provider's.
	AdminGroupEmails(ctx context.Context, group string) ([]string, error)
	CreateUser(ctx context.Context, u NewUser) (User, error)
	SendLoginLink(ctx context.Context, userID string) error
	// ListUsers returns every user.
	ListUsers(ctx context.Context) ([]User, error)
	// GetUser returns one user by ID, or ErrNotFound.
	GetUser(ctx context.Context, id string) (User, error)
	// SetDisabled disables or re-enables a user, changing nothing else.
	SetDisabled(ctx context.Context, id string, disabled bool) error
	// SetEmail sets a user's address, marked verified, changing nothing else.
	SetEmail(ctx context.Context, id, email string) error
	// SetName sets a user's display, first and last name, changing nothing else.
	SetName(ctx context.Context, id string, n Name) error
}
