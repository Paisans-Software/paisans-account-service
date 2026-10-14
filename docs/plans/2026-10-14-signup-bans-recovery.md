# Signup, bans and recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The account service takes signups by invite or open registration on `join.`, lets admins ban and unban members, lets members change their own email and name, and lets two admins together recover a member's account.

**Architecture:** `internal/config` gains `ACCOUNT_SIGNUP`, `ACCOUNT_JOIN_PUBLIC_URL` and `ACCOUNT_OPERATORS`. `internal/idp` gains reads of single users and one update write, `PUT /api/users/:id`, which always sends back every field as read and changes only what the caller names; `writesurface_test.go` pins it. `internal/store` gains migration `0002` (bans, email changes, recovery requests, nullable `applications.invite_id`). `internal/flow` holds every rule; `internal/web` registers routes per host and per signup mode.

**Tech Stack:** Go 1.x standard library `net/http` ServeMux host patterns, pgx v5, Pocket ID v2.18 admin API, `html/template`.

**Spec:** `docs/specs/2026-10-14-account-signup-bans-recovery.md` in `Paisans-Software/paisans-stack` (with `2026-10-14-account-service.md` and `2026-10-14-operators-and-management.md`).

## Global Constraints

- `ACCOUNT_SIGNUP` is `invite` or `open`, required; `ACCOUNT_INVITES` and `ACCOUNT_INVITE_PUBLIC_URL` are gone, with no alias.
- `ACCOUNT_JOIN_PUBLIC_URL` is always required, no trailing `/`, a different host from `ACCOUNT_PUBLIC_URL`.
- `ACCOUNT_OPERATORS` is usernames, comma separated, and may be empty.
- `join.` serves `GET`/`POST /i/{token}` (invite) or `GET`/`POST /` (open), and `GET /static/`; everything else is `account.`'s. `POST /invites` and revoke exist only with `ACCOUNT_SIGNUP=invite`.
- A ban sends `PUT /api/users/:id` with every other field as read and `disabled: true`; the reason is required; the record and its audit entry share one transaction.
- Email change code: six digits, 15 minutes, stored only as a keyed hash, attempts limited, compared in constant time.
- Recovery: a different admin approves within 48 hours; approval sets the address verified and calls `POST /api/users/:id/one-time-access-email`.
- Operators are refused for ban, email change, name change and recovery. An admin cannot ban themselves.
- Every POST carries the CSRF value; no response reveals whether an address belongs to an account or a ban; no log line carries a code or an address.

## Review Focus

- An address typed in another case or with spaces (`Bob@X.org `) must match a ban and an account exactly as the lowercase one does.
- A signup whose address is banned or already has an account must render the same page as an accepted one; the difference reaches only the address's own inbox.
- A code entered after five wrong tries must be refused even when it is right.
- A recovery request opened and then approved by the same admin, or approved after 48 hours, must change nothing at Pocket ID.
- A ban whose database write fails after Pocket ID disabled the user must re-enable the user rather than leave an unrecorded ban.

---

### Task 1: Configuration

**Files:** Modify `internal/config/config.go`, `internal/config/config_test.go`.

**Produces:** `Config.Signup string` (`"invite"`/`"open"`), `Config.JoinURL string`, `Config.Operators []string`, `Config.JoinHost() string`; `AccountHost()` unchanged. `Invites`, `InviteURL`, `InviteHost()` removed.

- [ ] Tests: base env uses `ACCOUNT_SIGNUP=invite`, `ACCOUNT_JOIN_PUBLIC_URL=https://join.example.org`. Table: signup unset / `yes` refused naming `ACCOUNT_SIGNUP`; join URL unset refused in both modes; trailing slash refused; same host refused; `ACCOUNT_OPERATORS` `" alice, bob ,"` parses to `[alice bob]`; empty gives none; `bad name!` refused naming the variable.
- [ ] Run `go test ./internal/config/` — fails.
- [ ] Implement; operator names validated with `idp.UsernameRule`.
- [ ] Run — passes. Commit.

### Task 2: Identity provider reads and the update write

**Files:** Modify `internal/idp/idp.go`, `internal/idp/idptest/fake.go`, `internal/idp/pocketid/client.go`, `client_test.go`, `writesurface_test.go`, `contract_test.go`.

**Produces:**
```go
var ErrNotFound = errors.New("no such user")
type User struct{ ID, Username, Email, DisplayName, FirstName, LastName string; Disabled bool }
type Name struct{ DisplayName, FirstName, LastName string }
// Provider gains:
ListUsers(ctx) ([]User, error)
GetUser(ctx, id string) (User, error)        // ErrNotFound on 404
SetDisabled(ctx, id string, disabled bool) error
SetEmail(ctx, id, email string) error        // emailVerified true
SetName(ctx, id string, n Name) error
```
`pocketid.Client.update(ctx, id, change func(*userUpdateRequest))` reads `GET /api/users/:id`, copies every field into `userUpdateRequest{username,email,emailVerified,firstName,lastName,displayName,isAdmin,locale,disabled}`, applies `change`, and sends `PUT /api/users/:id`.

- [ ] Tests (`client_test.go`): `SetDisabled(true)` sends one GET then one PUT whose body equals the read with `disabled:true` only changed, including `isAdmin`, `locale`, `emailVerified`; `SetEmail` changes `email` and sets `emailVerified:true`; `SetName` changes the three names, never `username`; `GetUser` on 404 is `idp.ErrNotFound`.
- [ ] `writesurface_test.go`: writes become `CreateUser:MethodPost`, `SendLoginLink:MethodPost`, `update:MethodPut`; `update`'s PUT path is `"/api/users/" + url.PathEscape(id)`; `userUpdateRequest` json fields pinned; assignments to its fields allowed only `Disabled` in `SetDisabled`, `Email` and `EmailVerified` (literal `true`) in `SetEmail`, the three names in `SetName`; `Username`, `IsAdmin`, `Locale` never assigned.
- [ ] Contract test: create a user, disable, read back disabled with username/email/displayName unchanged; re-enable; set a new email and read `emailVerified: true`; set names.
- [ ] Implement; fake gains the same methods. Run `go test ./internal/idp/...` and `go vet -tags contract ./...`. Commit.

### Task 3: Storage

**Files:** Create `internal/store/migrations/0002_signup_bans_recovery.sql`, `internal/store/bans.go`, `internal/store/accountchanges.go`; modify `applications.go`, `sweep.go`, `records.go`; tests in `internal/store/*_test.go`.

**Produces:**
```go
func (s *Store) Register(ctx, now time.Time, n NewApplication) (Application, error) // invite_id NULL
// Application.InviteID becomes 0 for registrations; InviterUsername "".
type Ban struct{ ID int64; UserID, Username, Reason, BannedBy string; BannedAt time.Time; LiftedBy string; LiftedAt time.Time }
func (s *Store) RecordBan(ctx, userID, username string, emailHMAC []byte, reason, actorSub, actorUsername string) (Ban, error) // ErrAlreadyBanned
func (s *Store) LiftBan(ctx, id int64, actorSub, actorUsername string) (Ban, error)
func (s *Store) CurrentBan(ctx, userID string) (Ban, bool, error)
func (s *Store) EmailBanned(ctx, emailHMAC []byte) (bool, error)
func (s *Store) Bans(ctx) ([]Ban, error)
func (s *Store) StartEmailChange(ctx, userID, newEmail string, codeHash []byte, expires time.Time) error
func (s *Store) TakeEmailChangeAttempt(ctx, userID string, now time.Time, max int) (EmailChange, error) // ErrNotFound when none, expired or spent
func (s *Store) FinishEmailChange(ctx, userID string) error
type Recovery struct{ ID int64; UserID, Username, NewEmail, Reason, OpenedBySub, OpenedBy, ApprovedBy, State string; OpenedAt, ExpiresAt time.Time }
func (s *Store) OpenRecovery(ctx, r Recovery) (Recovery, error) // ErrBusy when one is open for the user
func (s *Store) ClaimRecovery(ctx, id int64, approverSub string, now time.Time) (Recovery, error) // ErrNotFound, ErrSameAdmin
func (s *Store) ReleaseRecovery(ctx, id int64) error
func (s *Store) CompleteRecovery(ctx, id int64, approverSub, approver string) error
func (s *Store) CancelRecovery(ctx, id int64, actorSub string) error
func (s *Store) OpenRecoveries(ctx, now) ([]Recovery, error)
func (s *Store) AuditSubject(ctx, actorSub, action, subject string) error // the existing Audit
```
- [ ] Tests: a registration has no invite and completes, declines, dismisses and sweeps; one current ban per user; lifted bans no longer match `EmailBanned`; attempts count up and the sixth is `ErrNotFound`; an expired change is `ErrNotFound`; a recovery cannot be claimed by its opener, after expiry, or twice; the sweep expires recoveries and deletes old email changes. Each write leaves its audit row.
- [ ] Implement. Run with `ACCOUNT_TEST_DATABASE_URL`. Commit.

### Task 4: Flow — signup in both modes

**Files:** Modify `internal/flow/flow.go`, `flow_test.go`.

**Produces:** `Settings.Operators []string`, `Settings.JoinURL` (replacing `InviteURL`), `Settings.IDPURL`; `func (s *Service) Register(ctx, Applicant) (bool, error)`; `Apply` and `Register` share `submit`. A banned or in-use address creates nothing, renders the same outcome as an accepted application, and mails the address (banned: "Your application to join can't be accepted."; in use: sign in with an email code instead). Approval refuses a banned address with `ErrBanned`.

- [ ] Tests: open registration end to end against the fake (applied → approved → `Created`, `LinksSent`); banned address in both modes creates nothing and mails the refusal; `" Bob@X.ORG"` matches a ban on `bob@x.org`; in-use address creates nothing and mails the hint; ban after application makes approval fail with `ErrBanned`.
- [ ] Implement. Commit.

### Task 5: Flow — bans, self-service, recovery

**Files:** Create `internal/flow/bans.go`, `internal/flow/account.go`, `internal/flow/recovery.go` with tests.

**Produces:** `ErrOperator`, `ErrSelf`, `ErrBanned`, `ErrAlreadyBanned`, `ErrReasonRequired`, `ErrCodeWrong`, `ErrCodeExpired`, `ErrAddressRefused`, `ErrSameAdmin`; `IsOperator(username) bool`; `SearchMembers(ctx, q) ([]MemberRow, error)`; `Ban(ctx, admin, userID, reason)`; `Lift(ctx, admin, banID)`; `Bans(ctx)`; `RequestEmailChange(ctx, m, email)`; `ConfirmEmailChange(ctx, m, code)`; `Profile(ctx, m) (idp.User, error)`; `SetName(ctx, m, idp.Name)`; `OpenRecovery(ctx, admin, username, email, reason)`; `ApproveRecovery(ctx, admin, id)`; `CancelRecovery(ctx, admin, id)`; `Recoveries(ctx)`.

- [ ] Tests: ban disables, records hash and reason, audits; operator and self refused with nothing written; empty reason refused; record failure re-enables; lift re-enables. Email change: code mailed only to the new address, never logged; wrong code, expired code, sixth attempt refused; in-use and banned refused after the right code; success sets the address, mails the old one. Name change sets three names; operator refused. Recovery: same admin cannot approve; expired cannot be approved; cancel; operator refused; approval sets email and sends a link.
- [ ] Implement. Commit.

### Task 6: Web — routes by host and mode, pages

**Files:** Modify `internal/web/server.go`, `admin.go`, `member.go`, `invitee.go`, `session.go`, templates; create `internal/web/register.go`, `bans.go`, `account.go`, `recovery.go` and templates `register` (via `invite.html`), `members.html`, `ban.html`, `bans.html`, `email.html`, `name.html`, `recovery.html`; tests.

**Produces:** `Options{AccountHost, JoinHost, Signup}`; join-host registration form with a signed, expiring CSRF value (`registerCSRF`).

- [ ] Tests: every route on its own host answers and is 404 on the other, for each mode; invite-mode routes 404 in open mode and `/` on `join.` 404 in invite mode; CSRF refused on every new POST; ban confirmation page carries the session notice; no ban button for operators or self; `/account/email` and `/account/name` refuse operators; code entry rate limited.
- [ ] Implement. Commit.

### Task 7: Command, README, CHANGELOG

**Files:** `cmd/paisans-account-service/main.go`, `main_test.go`, `README.md`, `CHANGELOG.md`.

- [ ] Code of conduct loaded in both modes; sweep runs regardless. README environment table and routes. CHANGELOG `[Unreleased]` bullets of at most 35 words.
- [ ] `go vet ./... && go vet -tags contract ./... && go test -race ./...` with the test database; contract tests against `contract/compose.yaml`. Commit.
