# Your apps Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The account home page shows a Your apps card of tiles mirroring Pocket ID's dashboard, and the account app itself carries no launch URL.

**Architecture:** pocketid-go gains a client list call and a release; the account service reads clients and the user's groups each home-page load and filters with Pocket ID's own dashboard rule; the Paisans CLI marks the `account` blueprint `no_launch` and clears a launch URL it set on a `no_launch` client.

**Tech Stack:** Go, `html/template`, Postgres-backed tests (`ACCOUNT_TEST_DATABASE_URL`), pocketid-go, Pocket ID v2 API.

**Spec:** `docs/plans/2026-10-23-apps-directory-design.md` (this branch).

## Global Constraints

- Pocket ID's rule (`backend/internal/service/oidc_service.go:844-857` at v2.18.0): a client is listed when `is_group_restricted = false` or one of its allowed groups is one of the user's groups, and its `launch_url` is neither NULL nor `''`.
- Only reads are added at Pocket ID. `TestWriteSurface` / `TestWriteSurfaceOnTheWire` gain the new methods as reads; no write count changes.
- An app whose launch URL is not an absolute `https://` URL with a host is left off and logged at warn with the client's ID and name.
- Tiles sorted by name, case-insensitively, then ID.
- Logo URL is `IDPPublicURL() + "/api/oidc/clients/" + url.PathEscape(id) + "/logo"` (public route, `oidc_controller.go:43`), only when `hasLogo`; else a letter tile (first rune of the name, upper-cased).
- Copy, exact: card heading `Your apps`; empty `You don't have any apps yet.`; failure `Your apps can't be listed right now.`
- CHANGELOG bullets ≤ 35 words; docs describe current state only; say "blueprint", never "kind".
- Never `git stash`. Each repo's worktree lives in that repo's `.worktrees/`.

## Review Focus

- A client whose `launchURL` is `""` (not null) must be treated as no launch URL, as Pocket ID does. → Task 2 test.
- A restricted client with an empty allowed-group list is visible to nobody. → Task 2 test.
- A hostile client name (`<script>`) and a `javascript:` launch URL never reach the page as markup or a link. → Task 3 test.
- The home page still renders invites when Pocket ID's client read fails. → Task 3 test.
- The CSP `img-src` admits client logos and nothing wider than Pocket ID's `/api/oidc/clients/` path. → Task 3 test.

---

### Task 1: pocketid-go lists OIDC clients (repo `src/pocketid-go`, branch `feat/list-oidc-clients` off `develop`)

**Files:** Modify `client.go` (near `FindOIDCClient`), `client_test.go`, `CHANGELOG.md`.

**Interfaces — Produces:**
- `OIDCClient` gains `HasLogo bool \`json:"hasLogo"\``.
- `func (c *Client) OIDCClients(ctx context.Context) ([]OIDCClient, error)` — every client, all pages, via `list[OIDCClient](ctx, c, "/api/oidc/clients", "")`. Doc comment cites `controller/oidc_controller.go:31` and notes `allowedUserGroups` may be absent from list entries on older Pocket ID (v2.14.0 gave a count), so callers needing groups read by ID.

- [ ] Step 1: Test `TestOIDCClientsReadsEveryPage` with the existing fake HTTP server pattern: two pages, asserts all clients returned, `HasLogo` and `LaunchURL` decoded, `GET` only.
- [ ] Step 2: Run `go test ./... -run TestOIDCClients` — FAIL (undefined).
- [ ] Step 3: Implement.
- [ ] Step 4: `go test ./... && go vet ./... && gofmt -l .` — clean.
- [ ] Step 5: CHANGELOG `### Added` bullet; commit; push; PR to `develop`; merge when checks pass.
- [ ] Step 6: Release 0.3.0 the way 0.2.0 went: PR `kacl-skip: chore: release 0.3.0` to `develop` moving Unreleased to `## [0.3.0] - <date>`; merge; PR `develop` → `main` titled `Release 0.3.0`; merge; tag `v0.3.0` on that merge commit; push the tag.

### Task 2: account service reads apps (repo `src/paisans-account-service`, branch `feat/apps-directory`)

**Files:** `go.mod`/`go.sum` (pocketid-go v0.3.0), `internal/idp/idp.go`, `internal/idp/pocketid/client.go`, `internal/idp/pocketid/client_test.go`, `internal/idp/pocketid/writesurface_test.go`, `internal/idp/idptest/fake.go`, new `internal/flow/apps.go`, new `internal/flow/apps_test.go`.

**Interfaces — Produces:**
- `idp.Client struct { ID, Name, LaunchURL string; HasLogo, Restricted bool; AllowedGroupIDs []string }`.
- `idp.Provider` gains `Clients(ctx context.Context) ([]Client, error)`: pocketid lists with `OIDCClients`, then for each client with a non-empty launch URL that is restricted, reads `OIDCClientByID` for its allowed groups (skip a client that vanished between the two reads). Fake: settable `Clients []idp.Client` and `FailClients bool`.
- `flow.App struct { ID, Name, URL, Host, LogoURL, Letter string }`.
- `func (s *Service) Apps(ctx context.Context, m Member) ([]App, error)` — `UserGroups(m.Sub)` + `Clients`, Pocket ID's rule, the https rule, sort. Needs Pocket ID's public URL: add a field on the flow settings filled from `cfg.IDPPublicURL()` where the service is built.

- [ ] Step 1: Tests in `apps_test.go`: `TestAppsFollowPocketIDsRule` (unrestricted shown; restricted shown only with a matching group ID; restricted with no groups hidden; null and `""` launch URL hidden), `TestAppsDropBadLaunchURLs` (`http://`, `javascript:alert(1)`, `/relative`, `https://` with no host — all dropped, a warning logged), `TestAppsSortedByName`, `TestAppsLogoAndLetter` (`HasLogo` gives the exact logo URL; otherwise `LogoURL==""`, `Letter` is the upper-cased first rune, e.g. `"émile"` → `"É"`), `TestAppsHostIsLaunchHost`, `TestAppsFailWhenProviderFails`.
- [ ] Step 2: pocketid client test `TestClientsReadsGroupsByID` against the fake API; add `Clients` to both write-surface tables as a read of `clients` and `client by id`.
- [ ] Step 3: Run `go test ./internal/flow ./internal/idp/...` — FAIL.
- [ ] Step 4: Implement.
- [ ] Step 5: Same packages PASS; commit.

### Task 3: the Your apps card

**Files:** `internal/web/member.go`, `internal/web/templates/home.html`, `internal/web/static/style.css`, `internal/web/render.go` (CSP), `internal/web/server_test.go`, `internal/web/render_test.go`, `README.md`, `CHANGELOG.md`.

**Interfaces — Consumes:** `flow.Service.Apps`, `flow.App`.
**Produces:** `homeData` gains `Apps []flow.App` and `AppsUnavailable bool`. `homeView` fills them for every signed-in user before the signup-mode check; an `Apps` error logs a warning and sets `AppsUnavailable`, never failing the page.

- [ ] Step 1: Tests: `TestHomeListsApps` (card first on the page, tile per app, `href` exact, logo `<img>` or letter), `TestHomeAppsEmpty` (exact empty copy), `TestHomeAppsUnavailable` (exact failure copy and invites still render), `TestHomeAppsCSP` (`img-src` contains `<IDPPublicURL>/api/oidc/clients/` and the application logo, nothing else new). Extend `TestHostileValuesEscapedOnEveryPage` with a client named `<script>x</script>` and the `render_test` fixture with every branch of the card.
- [ ] Step 2: Run `go test ./internal/web` — FAIL.
- [ ] Step 3: Template: `<div class="card"><h1>Your apps</h1>` then a `.apps` grid of `<a class="app-tile" href="{{.URL}}">` with `<img src="{{.LogoURL}}" alt="">` or `<span class="app-letter">{{.Letter}}</span>`, `<b>{{.Name}}</b>`, `<span class="muted">{{.Host}}</span>`. CSS: `.apps { display:grid; grid-template-columns: repeat(auto-fill, minmax(9rem, 1fr)); gap:.75rem }`, tile and letter styled from existing tokens (`--radius-3xl`, `--muted`, `--border`), no underline, hover as `.sidebar a:hover`.
- [ ] Step 4: Full suite with `ACCOUNT_TEST_DATABASE_URL`, `go vet ./...`, `gofmt -l .` — clean.
- [ ] Step 5: README bullet for the home page's apps; CHANGELOG `### Added` (≤ 35 words); commit; push; PR to `develop` (`Closes #27`); merge when checks pass.

### Task 4: Paisans CLI — the account app has no launch URL (repo `src/paisans-stack`, branch `feat/account-no-launch` off `develop`)

**Files:** `internal/blueprints/reserved/account/blueprint.yaml`, `internal/oidcclient/oidcclient.go` (plan at `:253`), `internal/oidcclient/oidcclient_test.go`, the apply step that executes `SetLaunchURL`, `README.md` (launch URL section near line 8592), `docs/commands.md` if it lists plan lines, `CHANGELOG.md`.

**Interfaces — Produces:** `oidcclient.Desired` gains `NoLaunch bool` (set in `cmd/paisans/clients.go` from the blueprint's spec); new step kind `ClearLaunchURL`, title `clear launch URL for <app>`, detail `clear launch URL for client <app>: PUT /api/oidc/clients/<id> with launchURL "" and every other field sent back as it is now`.

- [ ] Step 1: Tests: `NoLaunch` + current launch URL in `ToolkitLaunchURLs` → `ClearLaunchURL` step; `NoLaunch` + operator-set value → no step, a `Present` line saying it is left as it is; `NoLaunch` + none → nothing; existing `SetLaunchURL` tests unchanged. A blueprint test that `account` has no launch URL.
- [ ] Step 2: Run `go test ./internal/oidcclient ./internal/blueprints ./cmd/paisans` — FAIL.
- [ ] Step 3: Implement; the executor reuses the `SetLaunchURL` PUT with an empty value (check pocketid-go's `SetLaunchURL` sends `""` rather than omitting it; if it omits, send it explicitly).
- [ ] Step 4: `go test ./... && go vet ./... && gofmt -l .` — clean (CI runs only the changelog check).
- [ ] Step 5: README and changelog; commit; push; PR to `develop`; merge when the changelog check passes.

## Order

Task 1 → Task 2 → Task 3 in sequence (2 needs the release). Task 4 is independent and runs in parallel.
