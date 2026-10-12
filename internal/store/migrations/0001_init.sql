CREATE TABLE invites (
  id               bigserial PRIMARY KEY,
  token_hash       bytea NOT NULL UNIQUE,
  inviter_sub      text NOT NULL,
  inviter_username text NOT NULL,
  created_at       timestamptz NOT NULL DEFAULT now(),
  expires_at       timestamptz NOT NULL,
  state            text NOT NULL CHECK (state IN ('open','applied','used','expired','revoked','declined')),
  -- create_nonce is the one-time value the create form carried, so that a
  -- resubmitted form makes no second invite.
  create_nonce     text,
  CONSTRAINT invites_create_nonce UNIQUE (inviter_sub, create_nonce)
);
CREATE INDEX invites_inviter_live ON invites (inviter_sub) WHERE state IN ('open','applied');

CREATE TABLE applications (
  id             bigserial PRIMARY KEY,
  invite_id      bigint NOT NULL UNIQUE REFERENCES invites(id),
  username       text NOT NULL,
  email          text NOT NULL,
  display_name   text NOT NULL,
  coc_version    text NOT NULL,
  agreed_at      timestamptz NOT NULL,
  state          text NOT NULL CHECK (state IN ('pending','approving','link_unsent')),
  claimed_at     timestamptz,
  -- claim_token is held by the one approval that may write the claimed
  -- application's follow-up states; a takeover replaces it.
  claim_token    text,
  idp_user_id    text,
  decided_by_sub text,
  decided_at     timestamptz
);
CREATE UNIQUE INDEX applications_username ON applications (lower(username));

CREATE TABLE declines (
  id          bigserial PRIMARY KEY,
  email_hmac  bytea NOT NULL,
  declined_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX declines_email ON declines (email_hmac);

CREATE TABLE introductions (
  id               bigserial PRIMARY KEY,
  username         text NOT NULL,
  inviter_sub      text NOT NULL,
  inviter_username text NOT NULL,
  created_at       timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE audit (
  id        bigserial PRIMARY KEY,
  at        timestamptz NOT NULL DEFAULT now(),
  actor_sub text,
  action    text NOT NULL,
  subject   text NOT NULL
);
