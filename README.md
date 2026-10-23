# paisans-account-service

paisans-account-service is the community's account service, a small Go service run by the `account` blueprint of [Paisans](https://github.com/Paisans-Software/paisans-cli), which renders the environment below. Pocket ID's own signup is off and nobody is a Pocket ID administrator, so this is where people join, where admins ban them and change their groups, and where accounts are recovered.

* **Signup.** With `ACCOUNT_SIGNUP=invite`, members make invite links; with `open`, anyone registers on the join host. Either way the person agrees to the code of conduct and chooses a username, display name, first and last name and email address; the last name is optional. The account at Pocket ID gets all of them. With `ACCOUNT_REQUIRE_APPROVAL` an admin approves the request first; then the account is created at Pocket ID, its address marked verified, and Pocket ID emails a sign-in link to it. An address that is banned, or already has an account, gets the same page as an accepted one; only that address's inbox is told why.
* **Members.** `/admin/members` lists Pocket ID's users, 50 to a page, each with their status: active, disabled at Pocket ID, or banned. A search by username, display name or email narrows the list, and each column header sorts it by name, username, email or status, again to reverse it. Pocket ID's API does not give a user's creation date, so the list has no joined column.
* **Bans.** A ban, offered on each row of `/admin/members`, needs a reason, disables the user at Pocket ID and records the keyed hash of their address, so it cannot sign up again. `/admin/bans` lists every ban and lifts one. Operators (`ACCOUNT_OPERATORS`) and the admin themselves cannot be banned. A ban does not end sessions an app keeps for itself; the confirmation page says so.
* **Groups.** `/admin/members/{id}/groups` lists every Pocket ID group with the user's own marked. Any admin changes any user's groups, their own and other admins' included; the save names only the groups added and removed, so a change someone else made to another group meanwhile is kept. `operators` is shown and never changed here: it belongs to `paisans-stack.yaml`, and the next `paisans deployment apply` restores an operator's groups. This service reads the admin group from Pocket ID on every request, so a change to it here takes effect at once; other apps see a change at the user's next sign-in, and the confirmation page says so.
* **A member's own account.** `/account/email` sends a six-digit code to the new address, valid for 15 minutes, five tries; the code is stored only as a keyed hash. The right code sets the address at Pocket ID, marked verified, and tells the old address. `/account/name` sets the display, first and last name; the username never changes. Both are refused for operators.
* **Your apps.** The home page opens with every app Pocket ID's own dashboard shows the signed-in user: each a tile with the app's logo, or its name's first letter, its name and its host, linking to its launch URL. Pocket ID is read on every load, so a change to someone's groups shows at once. An app whose launch URL is not an absolute `https://` URL is left off, with a warning in the log. If Pocket ID cannot be read, the card says so and the rest of the page renders.
* **Recovery.** For a member who has lost both their passkey and their address, an admin opens a request at `/admin/recovery` with the new address and how they confirmed who the member is. A different admin approves it within 48 hours: the service sets the address, verified, and Pocket ID emails a sign-in link to it. Either admin may cancel it first. An operator who is the admin group's only member recovers the account on submit, with no second admin, since they already hold the host and Pocket ID's API key; the request is recorded as theirs alone. If the admin group cannot be read, the request waits for a second admin. Operators are not recovered.

Every admin action is an audit entry. No log line carries an address or a code.

It calls Pocket ID's API through [pocketid-go](https://github.com/Paisans-Software/pocketid-go), the client the Paisans CLI uses, and only through it. It needs Pocket ID 2.18 or newer. The key goes only to a private address: loopback, RFC 1918, RFC 6598, IPv6 unique local, a network in `ACCOUNT_IDP_PRIVATE_NETWORKS`, or a hostname without a dot. A hostname with a dot is refused even when it resolves to a private address. A redirect from Pocket ID is an error and is never followed.

It answers on two hosts, each registering only its own routes; a route requested on the other host is a 404:

| Host | Routes |
|------|--------|
| `ACCOUNT_PUBLIC_URL`'s, `account.` | the home page, sign-in and sign-out, `/account/email`, `/account/name`, `/admin` and its invite requests, `/admin/members` and each member's `/admin/members/{id}/groups`, `/admin/bans`, `/admin/recovery`; with `ACCOUNT_SIGNUP=invite`, also `POST /invites` and `POST /invites/{id}/revoke` |
| `ACCOUNT_JOIN_PUBLIC_URL`'s, `join.` | `GET` and `POST /i/{token}` with `ACCOUNT_SIGNUP=invite`; `GET` and `POST /` (the registration page) with `open` |
| either | `/static/`; `/healthz` answers on any host |

The session cookie has no `Domain`, so the join host never holds a session. `/i/{token}`, `POST /` on the join host, and the address-change posts are rate limited per client address.

## Usernames

A username is chosen once, at signup, and never changes. The service trims surrounding spaces and lowercases it before checking it, so `Alice` and `alice` are the same name and the account is created as `alice`; the signup form lowercases as the person types, so they see the name they will get.

After lowercasing, a username is allowed when it:

* is 1 to 50 characters long;
* uses only `a`–`z`, `0`–`9`, `.` (dot), `_` (underscore), `-` (hyphen) and `@`;
* starts and ends with a letter or a number.

Anything else is refused with the reason on the form: spaces anywhere inside, accented or other non-ASCII letters, emoji, and every other symbol. `alice`, `alice.b`, `a_1`, `bob-smith` and `x` are allowed; `.alice`, `alice-`, `al ice` and `zoë` are not. These are Pocket ID's own rule (`UsernameRule` in `internal/idp/idp.go`), narrowed to lowercase. A name that differs from an existing one only in case is taken.

## Environment

| Variable | Meaning |
|----------|---------|
| `ACCOUNT_LISTEN_ADDR` | listen address, default `:8080` |
| `ACCOUNT_PUBLIC_URL` | `https://<account hostname>`, no trailing slash: the OIDC redirect, links to `/admin`, the `Secure` cookie flag |
| `ACCOUNT_SIGNUP` | `invite` or `open`, required: who can join, and which routes the join host serves |
| `ACCOUNT_JOIN_PUBLIC_URL` | `https://<join hostname>`, required, no trailing slash, on a different host from `ACCOUNT_PUBLIC_URL`: invite links and the registration page |
| `ACCOUNT_OPERATORS` | the operators' usernames, comma separated, may be empty: never banned, recovered, or given a new address or name here |
| `ACCOUNT_DATABASE_URL` | Postgres URL through the site's HAProxy |
| `ACCOUNT_SESSION_KEY` | 32 bytes as unpadded base64url (43 characters) |
| `ACCOUNT_EMAIL_HASH_KEY` | the same shape |
| `ACCOUNT_OIDC_ISSUER` | the identity app's public URL: sign-in, the logo, and the links pages and mail send members to |
| `ACCOUNT_OIDC_CLIENT_ID`, `ACCOUNT_OIDC_CLIENT_SECRET` | the service's own client |
| `ACCOUNT_MEMBER_GROUP`, `ACCOUNT_ADMIN_GROUP` | group names in the `groups` claim |
| `ACCOUNT_IDP_URL` | Pocket ID's API, which the key is sent to, at a private address such as `http://<site mesh address>:1413`; a public one stops the service at startup |
| `ACCOUNT_IDP_PRIVATE_NETWORKS` | optional CIDR networks, comma separated, that `ACCOUNT_IDP_URL` may also be in, for a mesh outside the private ranges |
| `ACCOUNT_IDP_API_KEY` | the static API key |
| `ACCOUNT_SMTP_HOST`, `ACCOUNT_SMTP_PORT`, `ACCOUNT_SMTP_USER`, `ACCOUNT_SMTP_PASSWORD`, `ACCOUNT_SMTP_FROM` | mail |
| `ACCOUNT_SMTP_TLS` | `none`, `starttls` or `tls` |
| `ACCOUNT_ACCENT_COLOR` | a CSS colour, or empty for the default |
| `ACCOUNT_REQUIRE_APPROVAL`, `ACCOUNT_APPLICATION_WAIT_DAYS`, `ACCOUNT_DECLINE_RETENTION_DAYS` | the signup settings, in either mode |
| `ACCOUNT_INVITE_QUOTA`, `ACCOUNT_INVITE_LIFETIME_DAYS` | the invite settings, used with `ACCOUNT_SIGNUP=invite` |
| `ACCOUNT_LOGIN_LINK_DAYS` | how long the sign-in link Pocket ID sends lasts, 1 to 31 days, default 7 |
| `ACCOUNT_COC_PATH` | path to the mounted code of conduct, default `/etc/paisans-account-service/code-of-conduct.md`; required in either signup mode |
| `ACCOUNT_TRUST_FORWARDED` | `true` behind the gateway's Caddy: client address is the rightmost `X-Forwarded-For` entry |

## Tests

Run the tests: `ACCOUNT_TEST_DATABASE_URL=postgres://… go test ./...`

`internal/idp/pocketid/writesurface_test.go` pins what the service may write at Pocket ID: creating a user, sending a sign-in link, updating a user, and changing their groups. An update changes only its own fields: a ban or its lifting the disabled state, an address change or a recovery the address, always marked verified, and a name change the display, first and last name. The username, the admin flag and the groups go back as read. A failure there is a question for the founder, not a test to fix.

The contract test runs against a real Pocket ID and Mailpit, as `.github/workflows/contract.yml` starts them: `go test -tags contract -run Contract ./internal/idp/pocketid/`. It creates users, so each run needs a fresh Pocket ID.
