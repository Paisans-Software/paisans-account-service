-- A recovery its opener completed alone, as the deployment's only admin and
-- one of its operators. Every other request has a second admin's approval.
ALTER TABLE recovery_requests ADD COLUMN sole_operator boolean NOT NULL DEFAULT false;
