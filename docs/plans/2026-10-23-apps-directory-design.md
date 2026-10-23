# Your apps on the account home page

Design for issue #27. Founder decisions are marked as such; everything marked
*builder's call* or *Claude's call* may be settled differently with reason.

## Intent

A member who signs in to the account site should see, first thing, every app
of the community they can open, and get to one in a click. Pocket ID already
has such a page (its member dashboard), and Paisans already gives each app's
client the link that page uses. The account home page mirrors it, so the
account site is a member's one starting point.

## Goals

- The account home page shows a **Your apps** card listing exactly the apps
  Pocket ID's own dashboard would show the signed-in user. *Founder.*
- Each app is a tile, like Pocket ID's dashboard: logo (or the name's first
  letter), name, hostname, the whole tile linking to the launch URL. *Founder,
  from the mockup (option A).*
- The card is the first one on the home page, above the invite cards. *Founder.*
- System apps that are not a place for a member to go stay off the list, by
  having no launch URL at all, so Pocket ID's dashboard drops them too. *Founder.*
- The list shows only apps the user's Pocket ID groups give them access to.
  The uptime monitor's client admits only `operators`, so operators see it and
  members do not. *Founder.*

## Non-goals

- No separate deny-list in the account service or its configuration. Whether an
  app is launchable is the blueprint's to say (`no_launch`).
- No ordering, pinning, hiding or favourites chosen by the member.
- No app descriptions, categories or search.
- No change to who may sign in to any app, the uptime monitor included.

## Anti-goals

- The list must never show an app the user cannot open, nor hide one Pocket
  ID's dashboard shows them, except an app whose launch URL is not a valid
  `https://` link (below). The two disagreeing otherwise is the failure this
  exists to avoid.
- The account service must not write anything new at Pocket ID. Its write
  surface (`internal/idp/pocketid/writesurface_test.go`) stays as it is; only
  reads are added.
- A Pocket ID hiccup must not take down the home page.

## The rule for which apps appear

Pocket ID's dashboard lists a client when it has a launch URL and either is not
group-restricted or allows one of the user's groups
(`service/oidc_service.go:844-857` at `v2.18.0`). The account service applies
the same rule, reading from Pocket ID on every load of the home page, so a
group change shows at once.

*Builder's call:* how to read it. Pocket ID's client list
(`controller/oidc_controller.go:31` at `v2.18.0`) includes each client's
`allowedUserGroups`, `hasLogo` and `hasDarkLogo`. The service lists the
clients, then reads each restricted client that has a launch URL by ID (`:33`)
for its allowed groups, as pocketid-go's `OIDCClients` tells a caller that
needs them to. A handful of apps is the expected size; a per-request
read of each is acceptable.

The user's groups come from the existing `UserGroups`.

## Two pieces, two repositories

Each piece is useful alone and they may ship in any order.

### 1. Account service: the Your apps card

- New read-only provider call(s) on `idp.Provider` (and `pocketid/client.go`,
  `idptest` fake): the clients with their name, launch URL, group restriction,
  allowed groups and whether they have a light or a dark logo. Listed in `TestWriteSurface`'s
  exported methods and `TestWriteSurfaceOnTheWire` as reads.
- pocketid-go has no call that lists clients with these fields; add one there
  (`OIDCClients`, plus `HasLogo` and `HasDarkLogo` on `OIDCClient`) and
  release a new pocketid-go version, then bump it here.
- `flow`: `Apps(ctx, member)` returns the tiles, sorted by name (*Claude's
  call*).
- `home.html`: the Your apps card first, for every signed-in user, whatever the
  signup mode. Tiles in a responsive grid that works at phone width.
- **Logo** (*Claude's call*): when Pocket ID has a light or a dark logo for the
  client, show it, as its dashboard does
  (`authorized-oidc-client-card.svelte:43`); otherwise a letter tile. The
  browser loads Pocket ID's public logo route directly
  (`controller/oidc_controller.go:43`), which needs no credential, so the API
  key never reaches it. A `<picture>` asks for `?light=false` when the browser
  prefers a dark scheme, as Pocket ID's dashboard does
  (`cached-image-util.ts:68`), and Pocket ID serves the other variant when the
  client has only one (`service/oidc_service.go:526-537`).
- **Hostname** under the name is the launch URL's host.
- **Empty** (*Claude's call*): "You don't have any apps yet."
- **Pocket ID unreachable** (*Claude's call*): the card says the apps can't be
  listed right now; the rest of the page renders, as the invite list does after
  #35.
- Every value escaped; `TestHostileValuesEscapedOnEveryPage` covers a hostile
  client name and launch URL. An app whose launch URL is not an absolute
  `https://` URL is left off the list, and the service logs a warning naming
  the client. *Founder.*
- README and CHANGELOG (at most 35 words a bullet).

### 2. Paisans CLI: the account app has no launch URL

- The reserved `account` blueprint (`internal/blueprints/reserved/account/
  blueprint.yaml`) gets `no_launch: true`: the account site linking to itself is
  noise. *Founder agreed.*
- `no_launch` today only stops Paisans setting a launch URL; it never clears
  one (`internal/oidcclient/oidcclient.go:253`). Extend the plan: when the
  desired launch URL is empty because the blueprint is `no_launch`, and the
  client's current launch URL is one Paisans set
  (`blueprints.ToolkitLaunchURLs`), plan `clear launch URL for <app>`, a `PUT`
  sending every field back as read with `launchURL` empty. A value an operator
  set by hand is reported and never cleared, as for a change today.
- The gate's blueprint is `no_launch` already; the clearing covers it too if a
  release ever gave it a launch URL.
- Clearing a launch URL Paisans set is the plan reconciling a client to its
  blueprint, as moving a toolkit-set launch URL already is, not migration code
  (*Claude's call*).
- README section on launch URLs, `commands.md` plan output, tests, changelog.

## Constraints

- Account service: Go, `html/template`, the existing Pocket ID look in
  `style.css`; changelog CI caps bullets at 35 words; PRs go to `develop`.
- Paisans CLI: `main` and `develop` take pull requests only; CI runs only the
  changelog check, so `go test`, `go vet` and `gofmt` run locally before merge.
  Blueprints are called blueprints in all writing. No migration code: older
  deployments are redeployed.
- Docs describe the current state only.
