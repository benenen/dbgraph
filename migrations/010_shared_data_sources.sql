-- dbgraph:no-transaction
--
-- Make a data source reusable across projects.
--
-- A data source described one MySQL owned by one project, so registering the
-- same database for a second project meant re-entering its credentials. It
-- becomes a standalone record that any number of projects link to, through
-- project_data_sources.
--
-- Only the connection is shared. Each project still scans for itself and keeps
-- its own catalog, so nodes gains project_id to its uniqueness: two projects
-- may hold the same table from the same source without colliding, and neither
-- can see the other's nodes.
--
-- Both tables are referenced by foreign keys, so this runs outside the
-- surrounding transaction with foreign keys disabled, per SQLite's documented
-- rebuild procedure. The runner verifies foreign_key_check before recording it.

PRAGMA legacy_alter_table=OFF;

BEGIN;

CREATE TABLE data_sources_rebuilt (
    id INTEGER PRIMARY KEY CHECK (id > 0),
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    source_kind INTEGER NOT NULL,
    dsn_environment TEXT NOT NULL CHECK (length(dsn_environment) BETWEEN 1 AND 200),
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

CREATE TABLE project_data_sources (
    project_id INTEGER NOT NULL REFERENCES projects(id),
    data_source_id INTEGER NOT NULL REFERENCES data_sources(id),
    created_at TEXT NOT NULL,
    PRIMARY KEY (project_id, data_source_id)
) STRICT;

-- Every existing data source keeps working: its owning project becomes its
-- first link.
INSERT INTO project_data_sources(project_id, data_source_id, created_at)
SELECT project_id, id, created_at FROM data_sources;

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

CREATE TABLE nodes_rebuilt (
    id INTEGER PRIMARY KEY CHECK (id > 0),
    project_id INTEGER NOT NULL REFERENCES projects(id),
    data_source_id INTEGER NOT NULL REFERENCES data_sources(id),
    stable_key TEXT NOT NULL CHECK (length(stable_key) BETWEEN 1 AND 1000),
    kind INTEGER NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE (project_id, data_source_id, stable_key)
) STRICT;

INSERT INTO nodes_rebuilt(id, project_id, data_source_id, kind, stable_key, created_at)
SELECT id, project_id, data_source_id, kind, stable_key, created_at FROM nodes;

DROP TABLE nodes;
ALTER TABLE nodes_rebuilt RENAME TO nodes;

CREATE INDEX nodes_project_kind_idx ON nodes(project_id, kind, id);

CREATE TRIGGER nodes_kind_check_insert
BEFORE INSERT ON nodes
WHEN NEW.kind NOT BETWEEN 1 AND 4
BEGIN
    SELECT RAISE(ABORT, 'invalid node kind');
END;

CREATE TRIGGER nodes_kind_check_update
BEFORE UPDATE OF kind ON nodes
WHEN NEW.kind NOT BETWEEN 1 AND 4
BEGIN
    SELECT RAISE(ABORT, 'invalid node kind');
END;

COMMIT;
