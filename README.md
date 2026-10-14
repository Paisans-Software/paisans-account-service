# paisans-account-service

paisans-account-service is the community's account service, a small Go service run by the `account` blueprint of [`paisans-stack`](https://github.com/Paisans-Software/paisans-stack), which renders the environment below. With invites on, members make invite links and invitees agree to the community's code of conduct, and choose their details, before an account is created at Pocket ID. An admin may approve each application first.

It answers on two hosts, each registering only its own routes; a route requested on the other host is a 404:

| Host | Routes |
|------|--------|
| `ACCOUNT_PUBLIC_URL`'s, `account.` | the home page, sign-in and sign-out, `/admin`, and with invites on the invite and application actions |
| `ACCOUNT_INVITE_PUBLIC_URL`'s, `invite.`, only when `ACCOUNT_INVITES` is `true` | the invite pages `/i/{token}` |
| either | `/static/`; `/healthz` answers on any host |

The session cookie has no `Domain`, so the invite host never holds a session. With `ACCOUNT_INVITES=false` the home page shows who is signed in, `/admin` has nothing to act on, and the hourly sweep still expires invites issued earlier.

## Environment

| Variable | Meaning |
|----------|---------|
| `ACCOUNT_LISTEN_ADDR` | listen address, default `:8080` |
| `ACCOUNT_PUBLIC_URL` | `https://<account hostname>`, no trailing slash: the OIDC redirect, links to `/admin`, the `Secure` cookie flag |
| `ACCOUNT_INVITES` | `true` or `false`, required: registers the invite routes and the invite UI |
| `ACCOUNT_INVITE_PUBLIC_URL` | `https://<invite hostname>`, no trailing slash, on a different host from `ACCOUNT_PUBLIC_URL`: invite links. Required with `ACCOUNT_INVITES=true`, refused with `false` |
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
| `ACCOUNT_INVITE_QUOTA`, `ACCOUNT_REQUIRE_APPROVAL`, `ACCOUNT_INVITE_LIFETIME_DAYS`, `ACCOUNT_APPLICATION_WAIT_DAYS`, `ACCOUNT_DECLINE_RETENTION_DAYS` | the invite settings |
| `ACCOUNT_LOGIN_LINK_DAYS` | how long the sign-in link Pocket ID sends lasts, 1 to 31 days, default 7 |
| `ACCOUNT_COC_PATH` | path to the mounted code of conduct, default `/etc/paisans-account-service/code-of-conduct.md`; read only with invites on |
| `ACCOUNT_TRUST_FORWARDED` | `true` behind the gateway's Caddy: client address is the rightmost `X-Forwarded-For` entry |

## Tests

Run the tests: `ACCOUNT_TEST_DATABASE_URL=postgres://… go test ./...`
