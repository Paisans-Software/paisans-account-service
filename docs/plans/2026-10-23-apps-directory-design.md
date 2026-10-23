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
  ID's dashboard shows them. The two disagreeing is the failure this exists to
  avoid.
- The account service must not write anything new at Pocket ID. Its write
  surface (`internal/idp/pocketid/writesurface_test.go`) stays as it is; only
  reads are added.
- A Pocket ID hiccup must not take down the home page.

## The rule for which apps appear

Pocket ID's dashboard lists a client when it has a launch URL and either is not
group-restricted or allows one of the user's groups
(`service/oidc_service.go:673-680` and `:758-765` at `v2.14.0`, as cited in the
Paisans CLI README). The account service applies the same rule, reading from
Pocket ID on every load of the home page, so a group change shows at once.

*Builder's call:* how to read it. Pocket ID's client list gives only a count of
allowed groups (pocketid-go `FindOIDCClient` comment), so either read each
client by ID, or use an admin endpoint that answers a given user's accessible
clients if Pocket ID has one. Check Pocket ID's source at the version Paisans
deploys and cite it, as the existing client code does. A handful of apps is the
expected size; a per-request read of each is acceptable.

The user's groups come from the existing `UserGroups`.

## Two pieces, two repositories

Each piece is useful alone and they may ship in any order.

### 1. Account service: the Your apps card

- New read-only provider call(s) on `idp.Provider` (and `pocketid/client.go`,
  `idptest` fake): the clients with their name, launch URL, group restriction,
  allowed groups and whether they have a logo. Listed in `TestWriteSurface`'s
  exported methods and `TestWriteSurfaceOnTheWire` as reads.
- pocketid-go has no call that lists clients with these fields; add one there
  (`ListOIDCClients` or similar, plus `HasLogo` on `OIDCClient` if Pocket ID
  returns it) and release a new pocketid-go minor version, then bump it here.
- `flow`: `Apps(ctx, member)` returns the tiles, sorted by name (*Claude's
  call*).
- `home.html`: the Your apps card first, for every signed-in user, whatever the
  signup mode. Tiles in a responsive grid that works at phone width.
- **Logo** (*Claude's call*): when Pocket ID has one for the client, show it;
  otherwise a letter tile. *Builder's call:* whether the browser loads Pocket
  ID's logo URL directly (only if that endpoint needs no admin credential) or
  the service proxies it; never expose the API key to the browser.
- **Hostname** under the name is the launch URL's host.
- **Empty** (*Claude's call*): "You don't have any apps yet."
- **Pocket ID unreachable** (*Claude's call*): the card says the apps can't be
  listed right now; the rest of the page renders, as the invite list does after
  #35.
- Every value escaped; `TestHostileValuesEscapedOnEveryPage` covers a hostile
  client name and launch URL. A launch URL that is not `https://` is not
  rendered as a link (*Claude's call*).
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
