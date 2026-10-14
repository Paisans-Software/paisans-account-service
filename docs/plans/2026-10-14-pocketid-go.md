# The account service on pocketid-go Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** The service calls Pocket ID through the shared client `github.com/paisans-software/pocketid-go` v0.1.0, behind its unchanged `idp.Provider` interface, with the write surface pinned as strictly as before and the API key sent only to a private address.

**Architecture:** `internal/idp/pocketid` stays the one package that reaches Pocket ID, as an adapter around a `*pocketid.Client` (imported as `pid`) on its `HTTP` path. Every request goes through `pid` except one read, the group listing, which v0.1.0 does not offer (see *Gaps in v0.1.0*). `config.Load` refuses a public `ACCOUNT_IDP_URL` at startup, naming it; `ACCOUNT_IDP_PRIVATE_NETWORKS` passes further networks to `pid.Client.Networks`. Member-facing links to Pocket ID come from `ACCOUNT_OIDC_ISSUER`, since `ACCOUNT_IDP_URL` is now a mesh address no browser can reach.

**Tech Stack:** Go 1.25, `github.com/paisans-software/pocketid-go` v0.1.0, `go/types` for the static write-surface test, Pocket ID v2.18.0 and Mailpit for the contract test.

**Spec:** `docs/specs/2026-10-14-repository-split.md` (step 5 of *Order*) and `docs/specs/2026-10-14-pocket-id-api-private.md`, both in `Paisans-Software/paisans-cli`.

## Global Constraints

- `idp.Provider`, `idptest.Fake` and the flows stay as they are.
- Every `pid` method gets the caller's `ctx`, which is the request's.
- The approved writes stay exactly four: create a user, send a sign-in link, update a user (every field back as read; only SetDisabled, SetEmail and SetName change a field, each its own), and set a user's groups (ChangeGroups: fresh read, plus additions, minus removals, after `checkGroups`).
- A failure of `writesurface_test.go` is a question for the founder, not a test to fix.
- `ACCOUNT_IDP_URL` must be private, by `pid.Private`: loopback, RFC 1918, RFC 6598, IPv6 unique local, a network in `ACCOUNT_IDP_PRIVATE_NETWORKS`, or a hostname without a dot. Anything else fails `config.Load`, naming the variable.
- CHANGELOG `[Unreleased]` bullets are at most 35 words. No tag, no release.

## Gaps in v0.1.0

`pid` v0.1.0 has no general user update and no group listing. The smallest changes that keep the service's behaviour:

- **Update.** `pid.SetAdmin(u)` and `pid.Demote(u)` each send `u` back as given with only `IsAdmin` forced. `update` reads the user, applies the caller's change and calls `SetAdmin` when the user as read is an admin and `Demote` otherwise, so `IsAdmin` goes back as read and the body is the same nine fields as before. Pinned statically and on the wire.
- **Group listing.** `ListGroups`, and `ChangeGroups`'s check that a group exists, need every group, including one with no members. `pid` lists groups only by exact name. The adapter keeps one GET of `/api/user-groups`, the package's only request outside `pid`, to the same private base URL with the same redirect-refusing client, pinned as GET-only. A `pid` release with `Groups` and `UpdateUser` removes both; that is a founder decision.

## Review Focus

- A public `ACCOUNT_IDP_URL` (a dotted hostname that resolves privately included) must stop the service in `config.Load`, before the database is opened, with the variable's name in the error.
- An admin's update (disable, address, name) must not demote them, and a non-admin's must not promote them.
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
	base    string       // for listGroups only
	hc      *http.Client // the copy given to api, redirects refused
	key     string       // for listGroups only
	linkTTL time.Duration
}
func (c *Client) update(ctx context.Context, id string, change func(*pid.User)) error {
	cur, err := c.api.User(ctx, id)
	if err != nil { return wrap(err) }
	next := cur
	change(&next)
	if cur.IsAdmin {
		err = c.api.SetAdmin(ctx, next)
	} else {
		err = c.api.Demote(ctx, next)
	}
	return wrap(err)
}
```

`CreateUser` sends `pid.NewUser{Username: u.Username, Email: &email, DisplayName: u.DisplayName}` with `email := strings.TrimSpace(u.Email)`; `SendLoginLink` calls `c.api.EmailLoginLink(ctx, userID, c.linkTTL)`; `ChangeGroups` keeps its six steps with the write `c.api.SetUserGroups(ctx, id, next)`. `wrap` maps a `*pid.APIError` with status 404 to `idp.ErrNotFound` and strips a `*url.Error`'s URL.

- [x] Tests first. `TestWriteSurface` (static, type-checked) pins: the import allowlist; every use of the `pid` package against an allowlist of types, `Private`, and the methods `Users`, `User`, `FindGroup`, `InGroup` (reads, anywhere) and `CreateUser`, `EmailLoginLink`, `SetAdmin`, `Demote`, `SetUserGroups` (writes, each called exactly once, from `CreateUser`, `SendLoginLink`, `update`, `update`, `ChangeGroups` respectively); a `pid` method used other than by calling it; exactly one `pid.Client` literal, in `New`, with keys `HTTP`, `BaseURL`, `APIKey`, `Networks`, and no assignment to its fields; no `pid.User` literal and no assignment to a `pid.User` field except Disabled in SetDisabled, Email and EmailVerified (only `true`) in SetEmail, DisplayName, FirstName and LastName in SetName; exactly one `pid.NewUser` literal, in CreateUser, with exactly those three keys from the argument; `update`'s shape above, including `if cur.IsAdmin { SetAdmin } else { Demote }` on `next`; `SendLoginLink`'s one call with `c.linkTTL`; `ChangeGroups`'s steps; the only `net/http` request built (`NewRequestWithContext`, `http.MethodGet` in place) and sent (`(*http.Client).Do`) in `listGroups`, no other `http.Method*` constant than GET anywhere, no write method spelled as a string, no request shortcut or `RoundTrip`; the exported surface. `TestWriteSurfaceOnTheWire` drives every exported method against a recording server and fails on any request outside the seven approved method and route pairs, and on any request without the key header. `TestUpdateKeepsAdmin` checks an admin's and a non-admin's update sends `isAdmin` as read. The existing HTTP-level tests stay, with the body changes `pid` brings: the create body is `username, email, firstName "", lastName "", displayName, isAdmin false`, and the link TTL is `"168h0m0s"`.
- [x] Run `go test ./internal/idp/...` — fails to build or fails.
- [x] Implement `client.go`; `go get github.com/paisans-software/pocketid-go@v0.1.0 && go mod tidy`.
- [x] Run `go vet ./... && go vet -tags contract ./... && go test -race ./...` — passes. Run the contract test against a fresh Pocket ID and Mailpit. Commit.

### Task 3: Documentation

**Files:** `README.md`, `CHANGELOG.md`.

- [x] README: `ACCOUNT_IDP_URL` is Pocket ID's API at a private address, such as `http://<site mesh address>:1413`, refused at startup otherwise; `ACCOUNT_IDP_PRIVATE_NETWORKS`; `ACCOUNT_OIDC_ISSUER` is also the URL members are sent to.
- [x] CHANGELOG `[Unreleased]`: Changed (pocketid-go; ACCOUNT_IDP_URL must be private; links use the issuer), Added (ACCOUNT_IDP_PRIVATE_NETWORKS). Each bullet at most 35 words.
- [x] Commit.
