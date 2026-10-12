// Package idp is the service's only view of the identity provider.
package idp

import (
	"context"
	"regexp"
)

var UsernameRule = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9_.@-]*[a-zA-Z0-9])?$`)

const (
	MaxUsername    = 50
	MaxDisplayName = 100
)

type User struct {
	ID, Username, Email string
	IsAdmin             bool
}

type NewUser struct{ Username, Email, DisplayName string }

type Provider interface {
	UsernameTaken(ctx context.Context, username string) (bool, error)
	EmailTaken(ctx context.Context, email string) (bool, error)
	FindUser(ctx context.Context, username string) (User, bool, error)
	AdminEmails(ctx context.Context) ([]string, error)
	CreateUser(ctx context.Context, u NewUser) (User, error)
	SendLoginLink(ctx context.Context, userID string) error
}
