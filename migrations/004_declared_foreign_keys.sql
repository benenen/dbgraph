CREATE TABLE declared_foreign_key_relations (
    data_source_id INTEGER NOT NULL REFERENCES data_sources(id),
    stable_key TEXT NOT NULL CHECK (length(stable_key) = 64),
    relation_id INTEGER NOT NULL UNIQUE REFERENCES relations(id),
    source_node_id INTEGER NOT NULL REFERENCES nodes(id),
    target_node_id INTEGER NOT NULL REFERENCES nodes(id),
    constraint_schema TEXT NOT NULL CHECK (length(constraint_schema) BETWEEN 1 AND 500),
    constraint_name TEXT NOT NULL CHECK (length(constraint_name) BETWEEN 1 AND 500),
    ordinal INTEGER NOT NULL CHECK (ordinal > 0),
    last_seen_scan_run_id INTEGER NOT NULL REFERENCES schema_scan_runs(id),
    is_present INTEGER NOT NULL CHECK (is_present IN (0, 1)),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (data_source_id, stable_key)
) STRICT;

CREATE INDEX declared_foreign_key_relations_present_idx
ON declared_foreign_key_relations(data_source_id, is_present, relation_id);

CREATE TABLE schema_scan_foreign_keys (
    scan_run_id INTEGER NOT NULL REFERENCES schema_scan_runs(id),
    data_source_id INTEGER NOT NULL REFERENCES data_sources(id),
    stable_key TEXT NOT NULL CHECK (length(stable_key) = 64),
    relation_id INTEGER NOT NULL REFERENCES relations(id),
    source_node_id INTEGER NOT NULL REFERENCES nodes(id),
    target_node_id INTEGER NOT NULL REFERENCES nodes(id),
    constraint_schema TEXT NOT NULL CHECK (length(constraint_schema) BETWEEN 1 AND 500),
    constraint_name TEXT NOT NULL CHECK (length(constraint_name) BETWEEN 1 AND 500),
    ordinal INTEGER NOT NULL CHECK (ordinal > 0),
    recorded_at TEXT NOT NULL,
    PRIMARY KEY (scan_run_id, stable_key)
) STRICT;

CREATE INDEX schema_scan_foreign_keys_relation_idx
ON schema_scan_foreign_keys(relation_id, scan_run_id);

CREATE TRIGGER schema_scan_foreign_keys_no_update
BEFORE UPDATE ON schema_scan_foreign_keys
BEGIN
    SELECT RAISE(ABORT, 'schema scan foreign keys are append-only');
END;

CREATE TRIGGER schema_scan_foreign_keys_no_delete
BEFORE DELETE ON schema_scan_foreign_keys
BEGIN
    SELECT RAISE(ABORT, 'schema scan foreign keys are append-only');
END;
