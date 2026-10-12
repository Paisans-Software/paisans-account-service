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

	"github.com/paisans-software/paisans-invite/internal/idp"
)

type Client struct {
	base, key string
	hc        *http.Client
}

var _ idp.Provider = (*Client)(nil)

// maxPages bounds a listing so a misbehaving server cannot hold a request open.
const maxPages = 100

// New copies hc so that a redirect is an error: the API key header must never
// follow a 3xx to another place.
func New(baseURL, apiKey string, hc *http.Client) *Client {
	var cp http.Client
	if hc != nil {
		cp = *hc
	}
	cp.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{base: strings.TrimRight(baseURL, "/"), key: apiKey, hc: &cp}
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
	ID       string  `json:"id"`
	Username string  `json:"username"`
	Email    *string `json:"email"`
	IsAdmin  bool    `json:"isAdmin"`
}

func (u userDTO) user() idp.User {
	e := ""
	if u.Email != nil {
		e = *u.Email
	}
	return idp.User{ID: u.ID, Username: u.Username, Email: e, IsAdmin: u.IsAdmin}
}

// createUserRequest is the whole body of the one user-creating write. Its
// fields are pinned by TestWriteSurface.
type createUserRequest struct {
	Username      string `json:"username"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"emailVerified"`
	DisplayName   string `json:"displayName"`
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

func (c *Client) AdminEmails(ctx context.Context) ([]string, error) {
	users, err := c.listUsers(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, u := range users {
		if u.IsAdmin && u.Email != "" {
			out = append(out, u.Email)
		}
	}
	return out, nil
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
	if err := c.do(ctx, http.MethodPost, "/api/users/"+url.PathEscape(userID)+"/one-time-access-email", struct{}{}, nil); err != nil {
		return err
	}
	slog.Info("identity provider write", "action", "user.login_link", "user_id", userID)
	return nil
}
