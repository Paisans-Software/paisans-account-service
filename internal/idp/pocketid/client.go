// Package pocketid is the only package that sends HTTP to the identity
// provider. Its write surface is pinned by writesurface_test.go.
package pocketid

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/paisans-software/paisans-account-service/internal/idp"
)

type Client struct {
	base, key string
	hc        *http.Client
	// linkTTL is the sign-in link's lifetime, sent in whole hours. Pocket ID
	// accepts at most 744h; config.Load holds it to 1 to 31 days.
	linkTTL time.Duration
}

var _ idp.Provider = (*Client)(nil)

// maxPages bounds a listing so a misbehaving server cannot hold a request open.
const maxPages = 100

// maxLinkTTL is the longest sign-in link Pocket ID accepts.
const maxLinkTTL = 744 * time.Hour

// New copies hc so that a redirect is an error: the API key header must never
// follow a 3xx to another place. linkTTL is the lifetime of every sign-in link
// the client sends: a whole number of hours from 1h to 744h, or New refuses.
func New(baseURL, apiKey string, hc *http.Client, linkTTL time.Duration) (*Client, error) {
	if linkTTL < time.Hour || linkTTL > maxLinkTTL || linkTTL%time.Hour != 0 {
		return nil, errors.New("identity provider: the sign-in link lifetime must be a whole number of hours from 1h to 744h")
	}
	var cp http.Client
	if hc != nil {
		cp = *hc
	}
	cp.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{base: strings.TrimRight(baseURL, "/"), key: apiKey, hc: &cp, linkTTL: linkTTL}, nil
}

// withoutURL drops the *url.Error wrapper, whose message carries the whole
// request URL and with it any query.
func withoutURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

type userDTO struct {
	ID          string  `json:"id"`
	Username    string  `json:"username"`
	Email       *string `json:"email"`
	DisplayName string  `json:"displayName"`
	FirstName   string  `json:"firstName"`
	LastName    *string `json:"lastName"`
	Disabled    bool    `json:"disabled"`
}

func (u userDTO) user() idp.User {
	return idp.User{ID: u.ID, Username: u.Username, Email: deref(u.Email), DisplayName: u.DisplayName,
		FirstName: u.FirstName, LastName: deref(u.LastName), Disabled: u.Disabled}
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// fullUserDTO is GET /api/users/:id: every field an update must send back.
type fullUserDTO struct {
	Username      string  `json:"username"`
	Email         *string `json:"email"`
	EmailVerified bool    `json:"emailVerified"`
	FirstName     string  `json:"firstName"`
	LastName      *string `json:"lastName"`
	DisplayName   string  `json:"displayName"`
	IsAdmin       bool    `json:"isAdmin"`
	Locale        *string `json:"locale"`
	Disabled      bool    `json:"disabled"`
}

// userUpdateRequest is the whole body of the one user-updating write,
// PUT /api/users/:id. Pocket ID sets every field from it, so update fills each
// from the user as read and the caller changes only its own. Its fields, and
// which function may change which, are pinned by TestWriteSurface.
type userUpdateRequest struct {
	Username      string  `json:"username"`
	Email         *string `json:"email"`
	EmailVerified bool    `json:"emailVerified"`
	FirstName     string  `json:"firstName"`
	LastName      string  `json:"lastName"`
	DisplayName   string  `json:"displayName"`
	IsAdmin       bool    `json:"isAdmin"`
	Locale        *string `json:"locale"`
	Disabled      bool    `json:"disabled"`
}

// createUserRequest is the whole body of the one user-creating write. Its
// fields are pinned by TestWriteSurface.
type createUserRequest struct {
	Username      string `json:"username"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"emailVerified"`
	DisplayName   string `json:"displayName"`
}

// loginLinkRequest is the whole body of the login-link write. Pocket ID's
// default lifetime is 15 minutes; the link is sent for the configured number of
// days in hours, which its email reads back as days ("168h" is "7 days"). Its
// one field, and the expression that fills it, are pinned by TestWriteSurface.
type loginLinkRequest struct {
	TTL string `json:"ttl"`
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return fmt.Errorf("identity provider %s %s: %w", method, pathOnly(path), withoutURL(err))
	}
	req.Header.Set("X-API-Key", c.key)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("identity provider %s %s: %w", method, pathOnly(path), withoutURL(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		io.Copy(io.Discard, resp.Body)
		return fmt.Errorf("identity provider %s %s: %w", method, pathOnly(path), idp.ErrNotFound)
	}
	if resp.StatusCode/100 != 2 {
		io.Copy(io.Discard, resp.Body)
		return fmt.Errorf("identity provider %s %s: status %d", method, pathOnly(path), resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func pathOnly(p string) string { return strings.SplitN(p, "?", 2)[0] }

// listUsers returns every user. Pocket ID's search is a case-sensitive LIKE on
// Postgres, so callers filter the full list themselves.
func (c *Client) listUsers(ctx context.Context) ([]idp.User, error) {
	var users []idp.User
	for page := 1; ; page++ {
		if page > maxPages {
			return nil, fmt.Errorf("identity provider GET /api/users: more than %d pages", maxPages)
		}
		q := url.Values{"pagination[page]": {fmt.Sprint(page)}, "pagination[limit]": {"100"}}
		var res struct {
			Data       []userDTO `json:"data"`
			Pagination struct {
				TotalPages int `json:"totalPages"`
			} `json:"pagination"`
		}
		if err := c.do(ctx, http.MethodGet, "/api/users?"+q.Encode(), nil, &res); err != nil {
			return nil, err
		}
		for _, u := range res.Data {
			users = append(users, u.user())
		}
		if page >= res.Pagination.TotalPages {
			return users, nil
		}
	}
}

func (c *Client) FindUser(ctx context.Context, username string) (idp.User, bool, error) {
	users, err := c.listUsers(ctx)
	if err != nil {
		return idp.User{}, false, err
	}
	for _, u := range users {
		if strings.EqualFold(u.Username, username) {
			return u, true, nil
		}
	}
	return idp.User{}, false, nil
}

func (c *Client) UsernameTaken(ctx context.Context, username string) (bool, error) {
	_, found, err := c.FindUser(ctx, username)
	return found, err
}

func (c *Client) EmailTaken(ctx context.Context, email string) (bool, error) {
	email = strings.TrimSpace(email)
	users, err := c.listUsers(ctx)
	if err != nil {
		return false, err
	}
	for _, u := range users {
		if strings.EqualFold(u.Email, email) {
			return true, nil
		}
	}
	return false, nil
}

// AdminGroupEmails returns the email addresses of the members of the group
// whose name (the value in the groups claim) is exactly group. Pocket ID's
// group search is a substring match, so the listing is filtered here.
func (c *Client) AdminGroupEmails(ctx context.Context, group string) ([]string, error) {
	id, err := c.groupID(ctx, group)
	if err != nil {
		return nil, err
	}
	var g struct {
		Users []userDTO `json:"users"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/user-groups/"+url.PathEscape(id), nil, &g); err != nil {
		return nil, err
	}
	var out []string
	for _, u := range g.Users {
		if u.Email != nil && *u.Email != "" && !u.Disabled {
			out = append(out, *u.Email)
		}
	}
	return out, nil
}

func (c *Client) groupID(ctx context.Context, name string) (string, error) {
	for page := 1; ; page++ {
		if page > maxPages {
			return "", fmt.Errorf("identity provider GET /api/user-groups: more than %d pages", maxPages)
		}
		q := url.Values{"search": {name}, "pagination[page]": {fmt.Sprint(page)}, "pagination[limit]": {"100"}}
		var res struct {
			Data []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"data"`
			Pagination struct {
				TotalPages int `json:"totalPages"`
			} `json:"pagination"`
		}
		if err := c.do(ctx, http.MethodGet, "/api/user-groups?"+q.Encode(), nil, &res); err != nil {
			return "", err
		}
		for _, g := range res.Data {
			if g.Name == name {
				return g.ID, nil
			}
		}
		if page >= res.Pagination.TotalPages {
			return "", fmt.Errorf("identity provider: no group named %q", name)
		}
	}
}

func (c *Client) CreateUser(ctx context.Context, u idp.NewUser) (idp.User, error) {
	var out userDTO
	body := createUserRequest{Username: u.Username, Email: strings.TrimSpace(u.Email), EmailVerified: false, DisplayName: u.DisplayName}
	if err := c.do(ctx, http.MethodPost, "/api/users", body, &out); err != nil {
		return idp.User{}, err
	}
	slog.Info("identity provider write", "action", "user.create", "user_id", out.ID)
	return out.user(), nil
}

func (c *Client) SendLoginLink(ctx context.Context, userID string) error {
	if err := c.do(ctx, http.MethodPost, "/api/users/"+url.PathEscape(userID)+"/one-time-access-email", loginLinkRequest{TTL: fmt.Sprintf("%dh", c.linkTTL/time.Hour)}, nil); err != nil {
		return err
	}
	slog.Info("identity provider write", "action", "user.login_link", "user_id", userID)
	return nil
}

// ListUsers returns every user, for the admins' member search.
func (c *Client) ListUsers(ctx context.Context) ([]idp.User, error) { return c.listUsers(ctx) }

func (c *Client) GetUser(ctx context.Context, id string) (idp.User, error) {
	var u userDTO
	if err := c.do(ctx, http.MethodGet, "/api/users/"+url.PathEscape(id), nil, &u); err != nil {
		return idp.User{}, err
	}
	return u.user(), nil
}

// update reads the user and writes it back with change applied. Pocket ID's
// update takes every field and sets each, so every field the caller does not
// change goes back exactly as it was read.
func (c *Client) update(ctx context.Context, id string, change func(*userUpdateRequest)) error {
	var cur fullUserDTO
	if err := c.do(ctx, http.MethodGet, "/api/users/"+url.PathEscape(id), nil, &cur); err != nil {
		return err
	}
	body := userUpdateRequest{Username: cur.Username, Email: cur.Email, EmailVerified: cur.EmailVerified,
		FirstName: cur.FirstName, LastName: deref(cur.LastName), DisplayName: cur.DisplayName,
		IsAdmin: cur.IsAdmin, Locale: cur.Locale, Disabled: cur.Disabled}
	change(&body)
	return c.do(ctx, http.MethodPut, "/api/users/"+url.PathEscape(id), body, nil)
}

func (c *Client) SetDisabled(ctx context.Context, id string, disabled bool) error {
	if err := c.update(ctx, id, func(r *userUpdateRequest) { r.Disabled = disabled }); err != nil {
		return err
	}
	slog.Info("identity provider write", "action", "user.disabled", "user_id", id, "disabled", disabled)
	return nil
}

func (c *Client) SetEmail(ctx context.Context, id, email string) error {
	email = strings.TrimSpace(email)
	if err := c.update(ctx, id, func(r *userUpdateRequest) { r.Email = &email; r.EmailVerified = true }); err != nil {
		return err
	}
	slog.Info("identity provider write", "action", "user.email", "user_id", id)
	return nil
}

func (c *Client) SetName(ctx context.Context, id string, n idp.Name) error {
	if err := c.update(ctx, id, func(r *userUpdateRequest) {
		r.DisplayName = n.DisplayName
		r.FirstName = n.FirstName
		r.LastName = n.LastName
	}); err != nil {
		return err
	}
	slog.Info("identity provider write", "action", "user.name", "user_id", id)
	return nil
}
