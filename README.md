# paisans-invite

paisans-invite is a small Go service where members make invite links and invitees agree to the community's code of conduct, and choose their details, before an account is created at Pocket ID. An admin may approve each application first. It is deployed by the `invite` kind of [`paisans-stack`](https://github.com/Paisans-Software/paisans-stack), which renders the environment below.

## Environment

| Variable | Meaning |
|----------|---------|
| `INVITE_LISTEN_ADDR` | listen address, default `:8080` |
| `INVITE_PUBLIC_URL` | `https://<hostname>`, no trailing slash |
| `INVITE_DATABASE_URL` | Postgres URL through the site's HAProxy |
| `INVITE_SESSION_KEY` | 32 bytes as unpadded base64url (43 characters) |
| `INVITE_EMAIL_HASH_KEY` | the same shape |
| `INVITE_OIDC_ISSUER` | the identity app's URL |
| `INVITE_OIDC_CLIENT_ID`, `INVITE_OIDC_CLIENT_SECRET` | the service's own client |
| `INVITE_MEMBER_GROUP`, `INVITE_ADMIN_GROUP` | group names in the `groups` claim |
| `INVITE_IDP_URL` | the identity app's URL, for the admin API and the logo |
| `INVITE_IDP_API_KEY` | the static API key |
| `INVITE_SMTP_HOST`, `INVITE_SMTP_PORT`, `INVITE_SMTP_USER`, `INVITE_SMTP_PASSWORD`, `INVITE_SMTP_FROM` | mail |
| `INVITE_SMTP_TLS` | `none`, `starttls` or `tls` |
| `INVITE_ACCENT_COLOR` | a CSS colour, or empty for the default |
| `INVITE_QUOTA`, `INVITE_REQUIRE_APPROVAL`, `INVITE_LIFETIME_DAYS`, `INVITE_APPLICATION_WAIT_DAYS`, `INVITE_DECLINE_RETENTION_DAYS` | the settings |
| `INVITE_LOGIN_LINK_DAYS` | how long the sign-in link Pocket ID sends lasts, 1 to 31 days, default 7 |
| `INVITE_COC_PATH` | path to the mounted code of conduct, default `/etc/paisans-invite/code-of-conduct.md` |
| `INVITE_TRUST_FORWARDED` | `true` behind the gateway's Caddy: client address is the rightmost `X-Forwarded-For` entry |

## Tests

Run the tests: `INVITE_TEST_DATABASE_URL=postgres://… go test ./...`
