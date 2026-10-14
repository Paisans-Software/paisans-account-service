# Changing a member's groups Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Any admin changes any group of any user at `/admin/members/{id}/groups`, except `operators`, with every change audited and the confirmation saying it takes effect at the next sign-in.

**Architecture:** `internal/idp` gains group reads and one new write, `PUT /api/users/:id/user-groups`, made only by `(*Client).ChangeGroups`: it reads the user's groups afresh, builds the new list as that read plus the additions minus the removals, refuses an unknown group or any change to `operators` membership, writes, reads again and returns what actually changed. `writesurface_test.go` pins the route statically and drives it against a recording server. `internal/flow` validates the request against the group list and audits; `internal/web` adds the page, the CSRF-guarded post and a Groups link on `/admin/members`. No migration: the existing `audit` table records the change.

**Tech Stack:** Go standard library `net/http`, `html/template`, pgx v5, Pocket ID v2.18 admin API.

**Spec:** `docs/specs/2026-10-14-account-promotion.md` in `Paisans-Software/paisans-stack`, building on `docs/specs/2026-10-14-account-signup-bans-recovery.md`.

## Global Constraints

- Any admin changes any group of any user, themselves and other admins included, except `operators`.
- `operators` is shown and never changed here, by form tampering included; the name is fixed (`idp.OperatorsGroup = "operators"`, as the toolkit's `blueprints.OperatorsGroup`).
- An operator's other groups can change; the page notes that the next `apply` restores `members`, `operators` and every app's admin and member group.
- The save names groups to add and groups to remove, never the whole set.
- The write: fresh `GET /api/users/:id` immediately before; body `{"userGroupIds": fresh + added - removed}`; refused if `operators` membership would differ; `GET` again after; audit what actually changed.
- The confirmation page says a change takes effect at the next sign-in and an open app session keeps its privileges until it expires (paisans-account-service#4).
- Every POST carries the CSRF value; no log line carries an address.
- CHANGELOG `[Unreleased]` bullets are at most 35 words.

## Review Focus

- A group changed by someone else between page load and save must be kept as the fresh read found it; only groups the admin changed move.
- A post that names the `operators` group's ID in `add` or `remove`, by hand-crafted form, must change nothing and say why.
- A post naming a group ID that does not exist must change nothing (Pocket ID would silently drop it).
- A save with no box changed must change nothing and say so, not write the same list back.
- A write whose follow-up read fails must still leave an audit entry, marked unconfirmed, with what was sent.

---

### Task 1: Identity provider groups and the group write

**Files:** Modify `internal/idp/idp.go`, `internal/idp/idptest/fake.go`, `internal/idp/pocketid/client.go`, `internal/idp/pocketid/writesurface_test.go`, `internal/idp/pocketid/client_test.go`, `internal/idp/pocketid/contract_test.go`.

**Produces:**
```go
const OperatorsGroup = "operators"
var ErrOperatorsGroup = errors.New("identity provider: operators membership would change")
var ErrNoSuchGroup = errors.New("identity provider: no such group")
var ErrUnconfirmed = errors.New("identity provider: the group change was sent but could not be read back")
type Group struct{ ID, Name, FriendlyName string }
type GroupChange struct{ Added, Removed []Group }
// Provider gains:
ListGroups(ctx) ([]Group, error)
UserGroups(ctx, userID string) ([]Group, error)        // ErrNotFound on 404
ChangeGroups(ctx, userID string, add, remove []string) (GroupChange, error)
```
The fake: `Groups map[string][]string` (name to usernames) stays the source of truth; a group's ID is `"g-"+name`; `FriendlyNames map[string]string` optional; `OnChangeGroups func()` runs after the fresh read and before the write, outside the lock, so a test can change groups in between.

- [x] Tests in `writesurface_test.go`: `TestWriteSurface` counts `ChangeGroups:MethodPut` as the fourth write and pins it statically — one `userGroupsRequest{UserGroupIDs: next}` literal, path `"/api/users/" + url.PathEscape(id) + "/user-groups"`, `next` assigned once from `groupList(groupIDs(before), add, remove)`, `before` assigned once from `c.userGroups(ctx, id)`, and `if err := checkGroups(before, next, all); err != nil { return ... }` at the top level before the PUT; `userGroupsRequest` has exactly one field, `userGroupIds`, assigned nowhere else. `TestGroupWriteBody` drives `ChangeGroups` against a recording server for add, remove, both, a no-op removal, a duplicate add, a concurrent change between page and save, and asserts the PUT body is exactly the fresh list with the requested change; adding or removing `operators`, or an unknown ID, makes no PUT.
- [x] Contract test `TestContractGroups`: add, then a group added behind the client's back is kept through a removal, `operators` refused both ways, the user's other fields and another user's groups unchanged.
- [x] Run `go test ./internal/idp/...` — fails.
- [x] Implement client and fake.
- [x] Run — passes. Commit `feat: change a user's groups at the identity provider`.

### Task 2: The flow

**Files:** Create `internal/flow/groups.go`, `internal/flow/groups_test.go`.

**Consumes:** Task 1. **Produces:**
```go
var ErrOperatorsGroup = errors.New("the operators group is managed in the deployment's configuration, not here")
var ErrNoSuchGroup = errors.New("that group no longer exists; reload the page")
var ErrNoGroupChange = errors.New("no group was changed")
type GroupRow struct{ idp.Group; In, Locked bool }
type GroupsPage struct{ Member MemberRow; Groups []GroupRow; Operator bool }
func (s *Service) MemberGroups(ctx, admin Member, userID string) (GroupsPage, error)
func (s *Service) ChangeGroups(ctx, admin Member, userID string, add, remove []string) (idp.GroupChange, error)
```
Audit action `groups.change`, subject `user=<id> added=<names> removed=<names>`; `groups.change_unconfirmed` when the read-back failed.

- [x] Tests: add, remove, both; self; another admin; an operator's `admins` removed; `operators` in add or remove refused with nothing written; unknown ID refused; empty change refused; members refused; a concurrent change kept; the audit subject lists what actually changed; an unconfirmed write is audited.
- [x] Run `go test ./internal/flow/` — fails. Implement. Passes. Commit.

### Task 3: The page

**Files:** Create `internal/web/groups.go`, `internal/web/templates/groups.html`, `internal/web/groups_test.go`. Modify `internal/web/server.go`, `internal/web/templates/members.html`, `internal/web/accounts_test.go` (CSRF list).

- [x] Tests: the page lists every group with the user's marked and `operators` disabled; the operator note; a save posts `group` (checked) and `was` (checked at load); the confirmation names what was added and removed and carries the session notice; a tampered `group=g-operators` is 422 and changes nothing; missing CSRF is 403; a member gets 403; an unknown user is 404.
- [x] Run `go test ./internal/web/` — fails. Implement. Passes. Commit.

### Task 4: Documentation

**Files:** Modify `README.md`, `CHANGELOG.md`.

- [x] README: a **Groups** bullet and `/admin/members/{id}/groups` in the route table. CHANGELOG `[Unreleased]` `### Added` bullet, at most 35 words.
- [x] `go vet ./... && go vet -tags contract ./... && go test -race ./...`; contract tests against Pocket ID v2.18.0 locally; the suite again in a fresh clone. Commit.
