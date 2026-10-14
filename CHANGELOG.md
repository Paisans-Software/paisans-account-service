# Changelog

All notable changes to this project are documented here, in the
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) format.

## [Unreleased]

### Changed

- The service is paisans-account-service: module, command, binary and image renamed, every environment variable now starts with ACCOUNT_, and the cookies are account_session and account_login.

### Added

- ACCOUNT_INVITES switches invites on or off. With it on, invitee pages answer only on ACCOUNT_INVITE_PUBLIC_URL's host and everything else only on ACCOUNT_PUBLIC_URL's host.
- With ACCOUNT_INVITES false the service reads no code of conduct, so a deployment without invites mounts none.

## [0.1.0] - 2026-10-12

### Added

- Members make invite links within a quota; invitees agree to the code of conduct and choose their details before an account exists, with optional admin approval.
