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

type NewUser struct{ Username, Email, DisplayName, FirstName, LastName string }

// OperatorsGroup is the group the deployment's configuration owns: the
// service shows it and never changes who is in it.
const OperatorsGroup = "operators"

var (
	// ErrOperatorsGroup is a group change that would add a user to, or take
	// them out of, OperatorsGroup.
	ErrOperatorsGroup = errors.New("identity provider: operators membership would change")
	// ErrNoSuchGroup is a group change naming a group the provider does not have.
	ErrNoSuchGroup = errors.New("identity provider: no such group")
	// ErrUnconfirmed is a group change that was sent but could not be read
	// back; the GroupChange returned with it is what was sent.
	ErrUnconfirmed = errors.New("identity provider: the group change was sent but could not be read back")
)

// Group is one of the provider's groups: Name is the value in the groups
// claim, FriendlyName the one people read.
type Group struct{ ID, Name, FriendlyName string }

// GroupChange is what one group change did to a user.
type GroupChange struct{ Added, Removed []Group }

// Client is one of the provider's OIDC clients, an app a member signs in to.
// LaunchURL is "" when the client has none. AllowedGroupIDs are the groups a
// Restricted client admits, read only when it has a launch URL; an
// unrestricted client admits everyone. HasLogo and HasDarkLogo say which of
// its light and dark logos Pocket ID has.
type Client struct {
	ID, Name, LaunchURL              string
	HasLogo, HasDarkLogo, Restricted bool
	AllowedGroupIDs                  []string
}

type Provider interface {
	UsernameTaken(ctx context.Context, username string) (bool, error)
	EmailTaken(ctx context.Context, email string) (bool, error)
	FindUser(ctx context.Context, username string) (User, bool, error)
	// AdminGroupEmails returns the email addresses of the members of the
	// named group: the service's admins are that group, not the provider's.
	AdminGroupEmails(ctx context.Context, group string) ([]string, error)
	// GroupMembers returns every member of the named group, disabled ones
	// and those without an address included. A group the provider does not
	// have is an error.
	GroupMembers(ctx context.Context, group string) ([]User, error)
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
	// ListGroups returns every group.
	ListGroups(ctx context.Context) ([]Group, error)
	// UserGroups returns the groups a user is in, or ErrNotFound.
	UserGroups(ctx context.Context, userID string) ([]Group, error)
	// ChangeGroups reads the user's groups afresh, adds the groups whose IDs
	// are in add and takes out those in remove, keeping every other group as
	// read. It refuses with ErrOperatorsGroup a change to OperatorsGroup
	// membership and with ErrNoSuchGroup an unknown group, writing nothing.
	// It returns what actually changed, read back after the write.
	ChangeGroups(ctx context.Context, userID string, add, remove []string) (GroupChange, error)
	// ListClients returns every OIDC client.
	ListClients(ctx context.Context) ([]Client, error)
}
