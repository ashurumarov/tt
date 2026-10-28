BEGIN;

-- Keeps updated_at honest: application code can never forget to bump it, and
-- it stays correct for manual SQL fixes too.
CREATE OR REPLACE FUNCTION set_updated_at() RETURNS trigger AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TABLE users (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    email      text        NOT NULL,
    full_name  text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    -- The same limits the domain layer enforces, restated where they cannot
    -- be bypassed.
    CONSTRAINT users_email_length     CHECK (char_length(email) BETWEEN 3 AND 320),
    CONSTRAINT users_full_name_length CHECK (char_length(full_name) BETWEEN 1 AND 255),
    -- Emails are normalized to lowercase before insert; this makes the
    -- uniqueness index below case-insensitive in practice.
    CONSTRAINT users_email_lowercase  CHECK (email = lower(email))
);

CREATE UNIQUE INDEX users_email_uniq ON users (email);

-- Serves the keyset pagination query (ORDER BY created_at DESC, id DESC)
-- as an index-only seek, independent of how deep the page is.
CREATE INDEX users_created_at_id_idx ON users (created_at DESC, id DESC);

CREATE TRIGGER users_set_updated_at
    BEFORE UPDATE ON users
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

COMMIT;
