-- The first and last name an applicant gives, copied to the Pocket ID user
-- at approval. An application made before this has neither.
ALTER TABLE applications ADD COLUMN first_name text NOT NULL DEFAULT '';
ALTER TABLE applications ADD COLUMN last_name  text NOT NULL DEFAULT '';
