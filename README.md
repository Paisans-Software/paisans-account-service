# paisans-account-service

paisans-account-service is the community's account service, a small Go service run by the `account` blueprint of [`paisans-stack`](https://github.com/Paisans-Software/paisans-stack), which renders the environment below. Pocket ID's own signup is off and nobody is a Pocket ID administrator, so this is where people join, where admins ban them, and where accounts are recovered.

* **Signup.** With `ACCOUNT_SIGNUP=invite`, members make invite links; with `open`, anyone registers on the join host. Either way the person agrees to the code of conduct and chooses a username, display name and email address. With `ACCOUNT_REQUIRE_APPROVAL` an admin approves the application first; then the account is created at Pocket ID, which emails a sign-in link. An address that is banned, or already has an account, gets the same page as an accepted one; only that address's inbox is told why.
* **Bans.** `/admin/members` searches Pocket ID's users by username, display name or email; a ban needs a reason, disables the user at Pocket ID and records the keyed hash of their address, so it cannot sign up again. `/admin/bans` lists every ban and lifts one. Operators (`ACCOUNT_OPERATORS`) and the admin themselves cannot be banned. A ban does not end sessions an app keeps for itself; the confirmation page says so.
* **A member's own account.** `/account/email` sends a six-digit code to the new address, valid for 15 minutes, five tries; the code is stored only as a keyed hash. The right code sets the address at Pocket ID, marked verified, and tells the old address. `/account/name` sets the display, first and last name; the username never changes. Both are refused for operators.
* **Recovery.** For a member who has lost both their passkey and their address, an admin opens a request at `/admin/recovery` with the new address and how they confirmed who the member is. A different admin approves it within 48 hours: the service sets the address, verified, and Pocket ID emails a sign-in link to it. Either admin may cancel it first. Operators are refused.

Every admin action is an audit entry. No log line carries an address or a code.

It answers on two hosts, each registering only its own routes; a route requested on the other host is a 404:

| Host | Routes |
|------|--------|
| `ACCOUNT_PUBLIC_URL`'s, `account.` | the home page, sign-in and sign-out, `/account/email`, `/account/name`, `/admin` and its applications, `/admin/members`, `/admin/bans`, `/admin/recovery`; with `ACCOUNT_SIGNUP=invite`, also `POST /invites` and `POST /invites/{id}/revoke` |
| `ACCOUNT_JOIN_PUBLIC_URL`'s, `join.` | `GET` and `POST /i/{token}` with `ACCOUNT_SIGNUP=invite`; `GET` and `POST /` (the registration page) with `open` |
| either | `/static/`; `/healthz` answers on any host |

The session cookie has no `Domain`, so the join host never holds a session. `/i/{token}`, `POST /` on the join host, and the address-change posts are rate limited per client address.

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
| `ACCOUNT_OIDC_ISSUER` | the identity app's URL |
| `ACCOUNT_OIDC_CLIENT_ID`, `ACCOUNT_OIDC_CLIENT_SECRET` | the service's own client |
| `ACCOUNT_MEMBER_GROUP`, `ACCOUNT_ADMIN_GROUP` | group names in the `groups` claim |
| `ACCOUNT_IDP_URL` | the identity app's URL, for the admin API and the logo |
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
