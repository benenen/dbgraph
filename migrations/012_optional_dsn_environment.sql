-- dbgraph:no-transaction
--
-- Let a data source stand on its stored connection string alone.
--
-- dsn_environment named the variable the serving process reads at scan time,
-- and it was mandatory because it was once the only way to resolve a DSN. A
-- sealed connection string is now stored on the row itself, so the variable is
-- a fallback, and demanding one from an operator who already supplied the
-- credential is busywork. The column stays NOT NULL and keeps its length
-- ceiling; only the "at least one character" floor goes.
--
-- data_sources is referenced by foreign keys, so this runs outside the
-- surrounding transaction with foreign keys disabled, per SQLite's documented
-- rebuild procedure. The runner verifies foreign_key_check before recording it.

PRAGMA legacy_alter_table=OFF;

CREATE TABLE data_sources_rebuilt (
    id INTEGER PRIMARY KEY CHECK (id > 0),
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    source_kind INTEGER NOT NULL,
    dsn_environment TEXT NOT NULL CHECK (length(dsn_environment) <= 200),
    dsn_key_id TEXT,
    dsn_ciphertext BLOB,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (name)
) STRICT;

INSERT INTO data_sources_rebuilt(
    id, name, source_kind, dsn_environment, dsn_key_id, dsn_ciphertext, created_at, updated_at
)
SELECT id, name, source_kind, dsn_environment, dsn_key_id, dsn_ciphertext, created_at, updated_at
FROM data_sources;

DROP TABLE data_sources;

ALTER TABLE data_sources_rebuilt RENAME TO data_sources;

CREATE TRIGGER data_sources_kind_check_insert
BEFORE INSERT ON data_sources
WHEN NEW.source_kind <> 1
BEGIN
    SELECT RAISE(ABORT, 'invalid data source kind');
END;

CREATE TRIGGER data_sources_kind_check_update
BEFORE UPDATE OF source_kind ON data_sources
WHEN NEW.source_kind <> 1
BEGIN
    SELECT RAISE(ABORT, 'invalid data source kind');
END;
