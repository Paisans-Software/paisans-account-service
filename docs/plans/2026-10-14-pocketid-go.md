# The account service on pocketid-go Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** The service calls Pocket ID through the shared client `github.com/paisans-software/pocketid-go` v0.2.0, behind its unchanged `idp.Provider` interface, with the write surface pinned as strictly as before and the API key sent only to a private address.

**Architecture:** `internal/idp/pocketid` stays the one package that reaches Pocket ID, as an adapter around a `*pocketid.Client` (imported as `pid`) on its `HTTP` path. Every request goes through that one `pid` client, built once in `New`; the package builds and sends no request itself. `pid` answers a redirect with an error rather than following it. `config.Load` refuses a public `ACCOUNT_IDP_URL` at startup, naming it; `ACCOUNT_IDP_PRIVATE_NETWORKS` passes further networks to `pid.Client.Networks`. Member-facing links to Pocket ID come from `ACCOUNT_OIDC_ISSUER`, since `ACCOUNT_IDP_URL` is now a mesh address no browser can reach.

**Tech Stack:** Go 1.25, `github.com/paisans-software/pocketid-go` v0.2.0, `go/types` for the static write-surface test, Pocket ID v2.18.0 and Mailpit for the contract test.

**Spec:** `docs/specs/2026-10-14-repository-split.md` (step 5 of *Order*) and `docs/specs/2026-10-14-pocket-id-api-private.md`, both in `Paisans-Software/paisans-cli`.

## Global Constraints

- `idp.Provider`, `idptest.Fake` and the flows stay as they are.
- Every `pid` method gets the caller's `ctx`, which is the request's.
- The approved writes stay exactly four: create a user (`pid.CreateUser`, from CreateUser), send a sign-in link (`pid.EmailLoginLink`, from SendLoginLink), update a user (`pid.UpdateUser`, from SetDisabled, SetEmail and SetName, each with a `UserChange` holding only its own fields), and set a user's groups (`pid.SetUserGroups`, from ChangeGroups: fresh read, plus additions, minus removals, after `checkGroups`).
- A failure of `writesurface_test.go` is a question for the founder, not a test to fix.
- `ACCOUNT_IDP_URL` must be private, by `pid.Private`: loopback, RFC 1918, RFC 6598, IPv6 unique local, a network in `ACCOUNT_IDP_PRIVATE_NETWORKS`, or a hostname without a dot. Anything else fails `config.Load`, naming the variable.
- CHANGELOG `[Unreleased]` bullets are at most 35 words. No tag, no release.

## The update and the group listing

- **Update.** `pid.UpdateUser(ctx, id, change)` reads the user and sends every field back as read except those `change` sets; the username, the admin flag and the locale are not in a `UserChange`, it refuses a user an LDAP sync manages, and an empty change sends nothing. SetDisabled sends `{Disabled}` (ban and lift), SetEmail `{Email, EmailVerified: true}` (address change and recovery), SetName `{DisplayName, FirstName, LastName}`. Pinned statically and on the wire.
- **Group listing.** `ListGroups`, and `ChangeGroups`'s check that a group exists, need every group, including one with no members: `pid.Groups`, a GET of `/api/user-groups` page by page.

## Review Focus

- A public `ACCOUNT_IDP_URL` (a dotted hostname that resolves privately included) must stop the service in `config.Load`, before the database is opened, with the variable's name in the error.
- An admin's update (disable, address, name) must not demote them, and a non-admin's must not promote them; no update changes the username or the groups.
- A 3xx from Pocket ID must be an error at every method and must not be followed.
- A 404 from Pocket ID through `pid` must still be `idp.ErrNotFound`, so a missing user is "not found", not a server error.
- An error from a dropped connection must not carry the request URL's query or the key.
- A member's mail and pages must link to Pocket ID's public URL, never the mesh address.

---

### Task 1: Configuration: a private ACCOUNT_IDP_URL, its networks, the public URL

**Files:** Modify `internal/config/config.go`, `internal/config/config_test.go`, `cmd/paisans-account-service/main.go`, `cmd/paisans-account-service/main_test.go`, `internal/web/server.go`, `internal/web/hostname_test.go`.

**Produces:** `Config.IDPNetworks []netip.Prefix`; `func (c Config) IDPPublicURL() string` (the issuer without a trailing slash).

- [x] Tests: `TestIDPURLMustBePrivate` accepts `http://10.44.0.1:1413`, `http://100.70.0.1:1413`, `http://[fd00::1]:1413`, `http://localhost:1411`, `http://pocket-id:1411`, and `http://203.0.113.5:1413` with `ACCOUNT_IDP_PRIVATE_NETWORKS=203.0.113.0/24`; refuses `https://id.example.org`, `http://203.0.113.5:1413`, `http://pocket-id.internal:1411` and `not a url`, each error naming `ACCOUNT_IDP_URL`. A malformed CIDR is refused naming `ACCOUNT_IDP_PRIVATE_NETWORKS`. `TestIDPPublicURL`: `https://id.example.org/` gives `https://id.example.org`. `TestRunRefusesPublicIDPURL` in `main_test.go` sets a complete environment with a public `ACCOUNT_IDP_URL` and an unreachable database, and `run()` fails naming the variable.
- [x] Run `go test ./internal/config/ ./cmd/...` — fails.
- [x] Implement in `Load`, using `pid.Private`; wire `IDPPublicURL()` into `web.New`'s renderer and `flow.Settings.IDPURL`; pass `IDPNetworks` to `pocketid.New`.
- [x] Run — passes. Commit.

### Task 2: The adapter on pid, test-first

**Files:** Rewrite `internal/idp/pocketid/client.go`, `writesurface_test.go`; modify `client_test.go`, `update_test.go`, `contract_test.go`; `go.mod`, `go.sum`.

**Interfaces:** `func New(baseURL, apiKey string, hc *http.Client, linkTTL time.Duration, networks ...netip.Prefix) (*Client, error)`, refusing a lifetime outside 1h to 744h in whole hours and a base URL that is not absolute or not `pid.Private`. `*Client` implements `idp.Provider`.

The adapter:

```go
type Client struct {
	api     *pid.Client
	linkTTL time.Duration
}
func (c *Client) SetDisabled(ctx context.Context, id string, disabled bool) error {
	if _, err := c.api.UpdateUser(ctx, id, pid.UserChange{Disabled: &disabled}); err != nil {
		return wrap(err)
	}
	...
}
```

SetEmail trims the address and sends `pid.UserChange{Email: &email, EmailVerified: &verified}` with `verified := true`; SetName sends `pid.UserChange{DisplayName: &n.DisplayName, FirstName: &n.FirstName, LastName: &n.LastName}`; `listGroups` is `c.api.Groups(ctx)`. `CreateUser` sends `pid.NewUser{Username: u.Username, Email: &email, DisplayName: u.DisplayName}` with `email := strings.TrimSpace(u.Email)`; `SendLoginLink` calls `c.api.EmailLoginLink(ctx, userID, c.linkTTL)`; `ChangeGroups` keeps its six steps with the write `c.api.SetUserGroups(ctx, id, next)`. `wrap` maps a `*pid.APIError` with status 404 to `idp.ErrNotFound` and strips a `*url.Error`'s URL.

- [x] Tests first. `TestWriteSurface` (static, type-checked) pins: the import allowlist; every use of the `pid` package against an allowlist of types (`Client`, `User`, `NewUser`, `UserChange`, `Group`, `APIError`), `Private`, and the methods `Users`, `User`, `FindGroup`, `Groups`, `InGroup` (reads, anywhere) and `UpdateUser`, `CreateUser`, `EmailLoginLink`, `SetUserGroups` (writes, each called exactly once from each function that owns it: SetDisabled, SetEmail and SetName; CreateUser; SendLoginLink; ChangeGroups); a `pid` method or `c.api` used other than by calling it; exactly one `Client` and one `pid.Client` literal, both in `New`, with exact keys, and no assignment to their fields; no `pid.User` literal and no assignment to any `pid` type's field; each `UserChange` written in its `UpdateUser` call with exactly its owner's fields from their approved sources (Disabled `&disabled` only in SetDisabled; Email `&email` only with EmailVerified `&verified`, `verified := true` set once, only in SetEmail; the names from `&n.DisplayName`, `&n.FirstName`, `&n.LastName` only in SetName), those variables assigned only as approved and their addresses taken only in the literal; exactly one `pid.NewUser` literal, in CreateUser, with exactly three keys from the argument; `SendLoginLink`'s one call with `c.linkTTL`; `listGroups` as one `c.api.Groups(ctx)`; `ChangeGroups`'s steps; no `net/http` request built or sent, no `http.Method*` constant other than GET, no write method spelled as a string, no request shortcut or `RoundTrip`; the exported surface. Breaking each rule deliberately (37 violations, at least one per allowlisted method and per `UserChange` field) fails it every time. `TestWriteSurfaceOnTheWire` drives every exported method against a recording server and fails unless each makes exactly its listed sequence of the seven approved method and route pairs, each with the key header. `TestRedirectIsAnErrorAndNotFollowed` answers every exported method with 301, 302, 303, 307 and 308 and requires a `*pid.APIError` with that status and no request to the target. `TestUpdateKeepsAdmin`, `TestUpdateRefusesLDAPUser` and the update body tests check the PUT body field by field.
- [x] Run `go test ./internal/idp/...` — fails to build or fails.
- [x] Implement `client.go`; `go get github.com/paisans-software/pocketid-go@v0.2.0 && go mod tidy`.
- [x] Run `go vet ./... && go vet -tags contract ./... && go test -race ./...` — passes. Run the contract test against a fresh Pocket ID and Mailpit; `TestContractUpdate` checks the admin flag, username and groups after every update, for an admin and a non-admin, and `TestContractGroups` that `ListGroups` includes a group nobody is in. Commit.

### Task 3: Documentation

**Files:** `README.md`, `CHANGELOG.md`.

- [x] README: every call goes through pocketid-go, a redirect is an error, and what each update may change; `ACCOUNT_IDP_URL` is Pocket ID's API at a private address, such as `http://<site mesh address>:1413`, refused at startup otherwise; `ACCOUNT_IDP_PRIVATE_NETWORKS`; `ACCOUNT_OIDC_ISSUER` is also the URL members are sent to.
- [x] CHANGELOG `[Unreleased]`: Changed (pocketid-go; ACCOUNT_IDP_URL must be private; links use the issuer), Added (ACCOUNT_IDP_PRIVATE_NETWORKS). Each bullet at most 35 words.
- [x] Commit.
