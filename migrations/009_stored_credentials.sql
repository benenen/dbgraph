-- Store source-database credentials and access tokens in SQLite.
--
-- A data source's DSN is sealed with AES-256-GCM; the key stays in the
-- environment, so the database file and its backups hold ciphertext only.
-- dsn_environment remains the fallback lookup key when no ciphertext is stored,
-- which keeps every existing data source working untouched.
--
-- Access tokens are stored as SHA-256 digests. The serving process only ever
-- needs to verify a presented token, never to reproduce one, so the database
-- holds no usable credential and needs no key.

ALTER TABLE data_sources ADD COLUMN dsn_key_id TEXT;
ALTER TABLE data_sources ADD COLUMN dsn_ciphertext BLOB;

CREATE TABLE access_credentials (
    actor TEXT PRIMARY KEY CHECK (length(actor) BETWEEN 1 AND 200),
    role INTEGER NOT NULL CHECK (role BETWEEN 1 AND 5),
    origin INTEGER NOT NULL CHECK (origin BETWEEN 1 AND 3),
    token_digest BLOB NOT NULL CHECK (length(token_digest) = 32),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

CREATE UNIQUE INDEX access_credentials_digest_idx ON access_credentials(token_digest);
