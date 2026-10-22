-- The Pocket ID user a used invite made, recorded when the invite is marked
-- used. An invite used before this has none.
ALTER TABLE invites ADD COLUMN invitee_sub text;
