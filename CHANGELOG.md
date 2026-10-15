# Changelog

All notable changes to this project are documented here, in the
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) format.

## [Unreleased]

### Changed

- Signup lowercases the username before checking it, so the account is created in lowercase; the form lowercases as the person types. The README lists what a username may contain.
- Pages look like Pocket ID's own settings: its header, a sidebar for the account and admin pages, and its cards, buttons, inputs, checkboxes, tables and alerts, with the Gloock font.
- The name page shows the username in a monospace code style and says it can not be changed; the signup form's username field is monospace too.

### Added

- The new invite link has a Copy link button.

## [0.4.0] - 2026-10-14

### Changed

- The service calls Pocket ID only through the shared pocketid-go client, which sends the API key only to a private address and never follows a redirect.
- ACCOUNT_IDP_URL must be a private address, such as the site's mesh address; a public one stops the service at startup, naming the variable.
- Pages and mail send members to Pocket ID at ACCOUNT_OIDC_ISSUER, its public URL, instead of ACCOUNT_IDP_URL.

### Added

- ACCOUNT_IDP_PRIVATE_NETWORKS names further CIDR networks ACCOUNT_IDP_URL may be in, for a mesh outside the private ranges.

## [0.3.0] - 2026-10-14

### Added

- Admins change any user's groups at /admin/members/{id}/groups, sending only the groups added and removed. The operators group is shown but never changed, and each change is audited.

## [0.2.0] - 2026-10-14

### Changed

- The service is paisans-account-service: module, command, binary and image renamed, every environment variable now starts with ACCOUNT_, and the cookies are account_session and account_login.

### Added

- ACCOUNT_SIGNUP chooses invite or open signup. Signup pages answer only on ACCOUNT_JOIN_PUBLIC_URL's host, everything else only on ACCOUNT_PUBLIC_URL's; open signup adds a rate-limited registration page.
- A signup whose address is banned or already has an account gets the accepted page; only that address's inbox learns why.
- Admins search members, ban with a required reason, and lift bans. A ban disables the user at Pocket ID and blocks their address from signing up.
- Members change their own email address, confirmed by a six-digit code sent to it, and their display, first and last name.
- Two admins together recover a member who lost both passkey and address: one opens a request, a different one approves it within 48 hours.
- ACCOUNT_OPERATORS names the operators, who are never banned, recovered or given a new address or name here.

## [0.1.0] - 2026-10-12

### Added

- Members make invite links within a quota; invitees agree to the code of conduct and choose their details before an account exists, with optional admin approval.
