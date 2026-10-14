// Package pocketid is the only package that reaches the identity provider.
// It is the service's adapter to the shared Pocket ID client,
// github.com/paisans-software/pocketid-go, through which every request goes
// except the group listing, which that client does not offer. Its write
// surface is pinned by writesurface_test.go.
package pocketid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/paisans-software/paisans-account-service/internal/idp"
	pid "github.com/paisans-software/pocketid-go"
)

type Client struct {
	api *pid.Client
	// base, key and hc are for listGroups alone: the one request the shared
	// client does not make. They are api's own values.
	base, key string
	hc        *http.Client
	// linkTTL is the sign-in link's lifetime, a whole number of hours. Pocket
	// ID accepts at most 744h; config.Load holds it to 1 to 31 days.
	linkTTL time.Duration
}

var _ idp.Provider = (*Client)(nil)

// maxPages bounds the group listing so a misbehaving server cannot hold a
// request open.
const maxPages = 100

// maxLinkTTL is the longest sign-in link Pocket ID accepts.
const maxLinkTTL = 744 * time.Hour

// New refuses a base URL whose host is not private, as pocketid-go judges it
// with networks, so the API key is never sent anywhere else; config.Load
// refuses the same earlier, naming the variable. It copies hc so that a
// redirect is an error: the key must never follow a 3xx to another place.
// linkTTL is the lifetime of every sign-in link the client sends: a whole
// number of hours from 1h to 744h, or New refuses.
func New(baseURL, apiKey string, hc *http.Client, linkTTL time.Duration, networks ...netip.Prefix) (*Client, error) {
	if linkTTL < time.Hour || linkTTL > maxLinkTTL || linkTTL%time.Hour != 0 {
		return nil, errors.New("identity provider: the sign-in link lifetime must be a whole number of hours from 1h to 744h")
	}
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("identity provider: the API's address must be an absolute http or https URL")
	}
	if !pid.Private(u.Hostname(), networks...) {
		return nil, fmt.Errorf("identity provider: %s is not a private address, and the API key is sent only to one", u.Hostname())
	}
	var cp http.Client
	if hc != nil {
		cp = *hc
	}
	cp.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	base := strings.TrimRight(baseURL, "/")
	return &Client{
		api:  &pid.Client{HTTP: &cp, BaseURL: base, APIKey: apiKey, Networks: networks},
		base: base, key: apiKey, hc: &cp, linkTTL: linkTTL,
	}, nil
}

// wrap names the identity provider in an error, makes a 404 idp.ErrNotFound,
// and drops a *url.Error's URL, which carries the query.
func wrap(err error) error {
	if err == nil {
		return nil
	}
	var ae *pid.APIError
	if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
		return fmt.Errorf("identity provider %s %s: %w", ae.Method, ae.Path, idp.ErrNotFound)
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		msg := err.Error()
		if i := strings.Index(msg, ue.Error()); i >= 0 {
			return fmt.Errorf("identity provider: %s%w", msg[:i], ue.Err)
		}
		return fmt.Errorf("identity provider: %w", ue.Err)
	}
	return fmt.Errorf("identity provider: %w", err)
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func toUser(u pid.User) idp.User {
	return idp.User{ID: u.ID, Username: u.Username, Email: deref(u.Email), DisplayName: u.DisplayName,
		FirstName: u.FirstName, LastName: deref(u.LastName), Disabled: u.Disabled}
}

func toGroup(g pid.Group) idp.Group {
	return idp.Group{ID: g.ID, Name: g.Name, FriendlyName: g.FriendlyName}
}

// listUsers returns every user. Pocket ID's search is a case-sensitive LIKE on
// Postgres, so callers filter the full list themselves.
func (c *Client) listUsers(ctx context.Context) ([]idp.User, error) {
	us, err := c.api.Users(ctx)
	if err != nil {
		return nil, wrap(err)
	}
	out := make([]idp.User, 0, len(us))
	for _, u := range us {
		out = append(out, toUser(u))
	}
	return out, nil
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

// AdminGroupEmails returns the email addresses of the enabled members of the
// group whose name (the value in the groups claim) is exactly group:
// FindGroup compares exactly, and every user is listed with their groups.
func (c *Client) AdminGroupEmails(ctx context.Context, group string) ([]string, error) {
	g, err := c.api.FindGroup(ctx, group)
	if err != nil {
		return nil, wrap(err)
	}
	if g == nil {
		return nil, fmt.Errorf("identity provider: no group named %q", group)
	}
	us, err := c.api.Users(ctx)
	if err != nil {
		return nil, wrap(err)
	}
	var out []string
	for _, u := range us {
		if u.InGroup(g.ID) && deref(u.Email) != "" && !u.Disabled {
			out = append(out, *u.Email)
		}
	}
	return out, nil
}

// CreateUser creates a user with an unverified address, no first or last
// name, and not an administrator.
func (c *Client) CreateUser(ctx context.Context, u idp.NewUser) (idp.User, error) {
	email := strings.TrimSpace(u.Email)
	out, err := c.api.CreateUser(ctx, pid.NewUser{Username: u.Username, Email: &email, DisplayName: u.DisplayName})
	if err != nil {
		return idp.User{}, wrap(err)
	}
	slog.Info("identity provider write", "action", "user.create", "user_id", out.ID)
	return toUser(out), nil
}

// SendLoginLink has Pocket ID email the user a sign-in link that lasts
// linkTTL. Pocket ID answers once the link is stored and sends the email
// afterwards, so a failed send is not reported here.
func (c *Client) SendLoginLink(ctx context.Context, userID string) error {
	if err := c.api.EmailLoginLink(ctx, userID, c.linkTTL); err != nil {
		return wrap(err)
	}
	slog.Info("identity provider write", "action", "user.login_link", "user_id", userID)
	return nil
}

// ListUsers returns every user, for the admins' member search.
func (c *Client) ListUsers(ctx context.Context) ([]idp.User, error) { return c.listUsers(ctx) }

func (c *Client) GetUser(ctx context.Context, id string) (idp.User, error) {
	u, err := c.api.User(ctx, id)
	if err != nil {
		return idp.User{}, wrap(err)
	}
	return toUser(u), nil
}

// update reads the user and writes it back with change applied. Pocket ID's
// update takes every field and sets each, so every field the caller does not
// change goes back exactly as it was read. The shared client's two ways of
// sending a user back as given are SetAdmin and Demote, which differ only in
// the admin flag they set; the one matching the flag as read sends it back
// unchanged.
func (c *Client) update(ctx context.Context, id string, change func(*pid.User)) error {
	cur, err := c.api.User(ctx, id)
	if err != nil {
		return wrap(err)
	}
	next := cur
	change(&next)
	if cur.IsAdmin {
		err = c.api.SetAdmin(ctx, next)
	} else {
		err = c.api.Demote(ctx, next)
	}
	return wrap(err)
}

func (c *Client) SetDisabled(ctx context.Context, id string, disabled bool) error {
	if err := c.update(ctx, id, func(u *pid.User) { u.Disabled = disabled }); err != nil {
		return err
	}
	slog.Info("identity provider write", "action", "user.disabled", "user_id", id, "disabled", disabled)
	return nil
}

func (c *Client) SetEmail(ctx context.Context, id, email string) error {
	email = strings.TrimSpace(email)
	if err := c.update(ctx, id, func(u *pid.User) { u.Email = &email; u.EmailVerified = true }); err != nil {
		return err
	}
	slog.Info("identity provider write", "action", "user.email", "user_id", id)
	return nil
}

func (c *Client) SetName(ctx context.Context, id string, n idp.Name) error {
	last := n.LastName
	if err := c.update(ctx, id, func(u *pid.User) {
		u.DisplayName = n.DisplayName
		u.FirstName = n.FirstName
		u.LastName = &last
	}); err != nil {
		return err
	}
	slog.Info("identity provider write", "action", "user.name", "user_id", id)
	return nil
}

// listGroups returns every group, GET /api/user-groups page by page. It is
// the one request this package makes itself, to the same private base URL
// with the same key and client as api: the shared client finds a group only
// by name, and the groups page and ChangeGroups need every group, a group
// without members included.
func (c *Client) listGroups(ctx context.Context) ([]idp.Group, error) {
	var out []idp.Group
	for page := 1; ; page++ {
		if page > maxPages {
			return nil, fmt.Errorf("identity provider GET /api/user-groups: more than %d pages", maxPages)
		}
		q := url.Values{"pagination[page]": {fmt.Sprint(page)}, "pagination[limit]": {"100"}}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/user-groups?"+q.Encode(), nil)
		if err != nil {
			return nil, wrap(err)
		}
		req.Header.Set("X-API-Key", c.key)
		req.Header.Set("Accept", "application/json")
		resp, err := c.hc.Do(req)
		if err != nil {
			return nil, wrap(fmt.Errorf("GET /api/user-groups: Pocket ID did not answer: %w", err))
		}
		var res struct {
			Data       []pid.Group `json:"data"`
			Pagination struct {
				TotalPages int `json:"totalPages"`
			} `json:"pagination"`
		}
		err = func() error {
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				io.Copy(io.Discard, resp.Body)
				return fmt.Errorf("identity provider GET /api/user-groups: status %d", resp.StatusCode)
			}
			if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
				return fmt.Errorf("identity provider GET /api/user-groups: the response is not the JSON expected: %v", err)
			}
			return nil
		}()
		if err != nil {
			return nil, err
		}
		for _, g := range res.Data {
			out = append(out, toGroup(g))
		}
		if page >= res.Pagination.TotalPages || len(res.Data) == 0 {
			return out, nil
		}
	}
}

// ListGroups returns every group, for the admins' groups page.
func (c *Client) ListGroups(ctx context.Context) ([]idp.Group, error) { return c.listGroups(ctx) }

// userGroups reads the user, GET /api/users/:id, for the groups it is in.
func (c *Client) userGroups(ctx context.Context, id string) ([]idp.Group, error) {
	u, err := c.api.User(ctx, id)
	if err != nil {
		return nil, wrap(err)
	}
	out := make([]idp.Group, 0, len(u.UserGroups))
	for _, g := range u.UserGroups {
		out = append(out, toGroup(g))
	}
	return out, nil
}

func (c *Client) UserGroups(ctx context.Context, id string) ([]idp.Group, error) {
	return c.userGroups(ctx, id)
}

// ChangeGroups reads the user's groups afresh and writes them back with only
// the requested change, so that a change another admin made in between to
// any other group is kept. Pocket ID replaces the user's groups with exactly
// the list sent. TestWriteSurface pins its shape.
func (c *Client) ChangeGroups(ctx context.Context, id string, add, remove []string) (idp.GroupChange, error) {
	all, err := c.listGroups(ctx)
	if err != nil {
		return idp.GroupChange{}, err
	}
	before, err := c.userGroups(ctx, id)
	if err != nil {
		return idp.GroupChange{}, err
	}
	next := groupList(groupIDs(before), add, remove)
	if err := checkGroups(before, next, all); err != nil {
		return idp.GroupChange{}, err
	}
	if err := c.api.SetUserGroups(ctx, id, next); err != nil {
		return idp.GroupChange{}, wrap(err)
	}
	slog.Info("identity provider write", "action", "user.groups", "user_id", id)
	after, err := c.userGroups(ctx, id)
	if err != nil {
		return groupChange(before, named(next, before, all)), fmt.Errorf("%w: %w", idp.ErrUnconfirmed, err)
	}
	return groupChange(before, after), nil
}

func groupIDs(gs []idp.Group) []string {
	out := make([]string, 0, len(gs))
	for _, g := range gs {
		out = append(out, g.ID)
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// groupList is the list the group write sends: cur in its own order without
// the IDs in remove, then each ID in add not already there. A group neither
// added nor removed stays exactly as cur has it.
func groupList(cur, add, remove []string) []string {
	out := []string{}
	for _, id := range cur {
		if !contains(remove, id) && !contains(out, id) {
			out = append(out, id)
		}
	}
	for _, id := range add {
		if !contains(remove, id) && !contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// named resolves each ID to a group the user is in or the provider has, in
// the order of ids; an ID neither knows is left out.
func named(ids []string, known ...[]idp.Group) []idp.Group {
	var out []idp.Group
	for _, id := range ids {
	find:
		for _, list := range known {
			for _, g := range list {
				if g.ID == id {
					out = append(out, g)
					break find
				}
			}
		}
	}
	return out
}

func inOperators(gs []idp.Group) bool {
	for _, g := range gs {
		if g.Name == idp.OperatorsGroup {
			return true
		}
	}
	return false
}

// checkGroups refuses next if it names a group that neither the user's own
// list nor the provider's has, or if it would put the user in OperatorsGroup
// or take them out of it.
func checkGroups(before []idp.Group, next []string, all []idp.Group) error {
	resolved := named(next, before, all)
	if len(resolved) != len(next) {
		return idp.ErrNoSuchGroup
	}
	if inOperators(before) != inOperators(resolved) {
		return idp.ErrOperatorsGroup
	}
	return nil
}

// groupChange is the difference between two lists of a user's groups.
func groupChange(before, after []idp.Group) idp.GroupChange {
	var ch idp.GroupChange
	for _, g := range after {
		if !contains(groupIDs(before), g.ID) {
			ch.Added = append(ch.Added, g)
		}
	}
	for _, g := range before {
		if !contains(groupIDs(after), g.ID) {
			ch.Removed = append(ch.Removed, g)
		}
	}
	return ch
}
