-- An application under open signup has no invite.
ALTER TABLE applications ALTER COLUMN invite_id DROP NOT NULL;

CREATE TABLE bans (
  id                 bigserial PRIMARY KEY,
  idp_user_id        text NOT NULL,
  username           text NOT NULL,
  -- email_hmac is the banned user's address keyed under the email hash key,
  -- or null if they had none; a signup whose address matches a current ban
  -- is refused.
  email_hmac         bytea,
  reason             text NOT NULL CHECK (reason <> ''),
  banned_by_sub      text NOT NULL,
  banned_by_username text NOT NULL,
  banned_at          timestamptz NOT NULL DEFAULT now(),
  lifted_by_sub      text,
  lifted_by_username text,
  lifted_at          timestamptz
);
CREATE UNIQUE INDEX bans_current_user ON bans (idp_user_id) WHERE lifted_at IS NULL;
CREATE INDEX bans_current_email ON bans (email_hmac) WHERE lifted_at IS NULL;

-- One pending address change per member. The code is kept only as a keyed
-- hash bound to the member and the new address.
CREATE TABLE email_changes (
  idp_user_id text PRIMARY KEY,
  new_email   text NOT NULL,
  code_hash   bytea NOT NULL,
  expires_at  timestamptz NOT NULL,
  attempts    int NOT NULL DEFAULT 0,
  created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE recovery_requests (
  id                   bigserial PRIMARY KEY,
  idp_user_id          text NOT NULL,
  username             text NOT NULL,
  new_email            text NOT NULL,
  reason               text NOT NULL CHECK (reason <> ''),
  opened_by_sub        text NOT NULL,
  opened_by_username   text NOT NULL,
  opened_at            timestamptz NOT NULL DEFAULT now(),
  expires_at           timestamptz NOT NULL,
  state                text NOT NULL CHECK (state IN ('open','approving','approved','cancelled','expired')),
  -- claimed_at is when an approval took the request; a claim older than ten
  -- minutes may be taken over, as after a crash.
  claimed_at           timestamptz,
  approved_by_sub      text,
  approved_by_username text,
  closed_at            timestamptz,
  closed_by_sub        text
);
CREATE UNIQUE INDEX recovery_live_user ON recovery_requests (idp_user_id) WHERE state IN ('open','approving');
