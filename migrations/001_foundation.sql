CREATE TABLE projects (
    id INTEGER PRIMARY KEY CHECK (id > 0),
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    description TEXT NOT NULL DEFAULT '' CHECK (length(description) <= 2000),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (name)
) STRICT;

CREATE TABLE id_generator_states (
    node_id INTEGER PRIMARY KEY CHECK (node_id BETWEEN 0 AND 1023),
    high_water_id INTEGER NOT NULL CHECK (high_water_id > 0),
    updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE data_sources (
    id INTEGER PRIMARY KEY CHECK (id > 0),
    project_id INTEGER NOT NULL REFERENCES projects(id),
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    source_kind INTEGER NOT NULL,
    dsn_environment TEXT NOT NULL CHECK (length(dsn_environment) BETWEEN 1 AND 200),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (project_id, name)
) STRICT;

CREATE TABLE repositories (
    id INTEGER PRIMARY KEY CHECK (id > 0),
    project_id INTEGER NOT NULL REFERENCES projects(id),
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    remote_url TEXT NOT NULL DEFAULT '' CHECK (length(remote_url) <= 2000),
    default_branch TEXT NOT NULL DEFAULT '' CHECK (length(default_branch) <= 500),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (project_id, name)
) STRICT;

CREATE TABLE schema_scan_runs (
    id INTEGER PRIMARY KEY CHECK (id > 0),
    project_id INTEGER NOT NULL REFERENCES projects(id),
    data_source_id INTEGER NOT NULL REFERENCES data_sources(id),
    status INTEGER NOT NULL,
    node_count INTEGER NOT NULL DEFAULT 0 CHECK (node_count >= 0),
    stale_count INTEGER NOT NULL DEFAULT 0 CHECK (stale_count >= 0),
    error_code TEXT,
    error_message TEXT,
    started_at TEXT NOT NULL,
    completed_at TEXT
) STRICT;

CREATE INDEX schema_scan_runs_source_time_idx
ON schema_scan_runs(data_source_id, started_at, id);

CREATE TABLE nodes (
    id INTEGER PRIMARY KEY CHECK (id > 0),
    project_id INTEGER NOT NULL REFERENCES projects(id),
    data_source_id INTEGER NOT NULL REFERENCES data_sources(id),
    stable_key TEXT NOT NULL CHECK (length(stable_key) BETWEEN 1 AND 1000),
    kind INTEGER NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE (data_source_id, stable_key)
) STRICT;

CREATE INDEX nodes_project_kind_idx ON nodes(project_id, kind, id);

CREATE TABLE node_versions (
    id INTEGER PRIMARY KEY CHECK (id > 0),
    node_id INTEGER NOT NULL REFERENCES nodes(id),
    scan_run_id INTEGER NOT NULL REFERENCES schema_scan_runs(id),
    parent_node_id INTEGER REFERENCES nodes(id),
    status INTEGER NOT NULL,
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 500),
    qualified_name TEXT NOT NULL CHECK (length(qualified_name) BETWEEN 1 AND 1000),
    data_type TEXT NOT NULL DEFAULT '' CHECK (length(data_type) <= 500),
    nullable INTEGER NOT NULL CHECK (nullable IN (0, 1)),
    ordinal_position INTEGER NOT NULL DEFAULT 0 CHECK (ordinal_position >= 0),
    metadata_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(metadata_json)),
    created_at TEXT NOT NULL
) STRICT;

CREATE INDEX node_versions_node_time_idx ON node_versions(node_id, created_at, id);
CREATE INDEX node_versions_qualified_idx ON node_versions(qualified_name, status, id);

CREATE TABLE node_current (
    node_id INTEGER PRIMARY KEY REFERENCES nodes(id),
    version_id INTEGER NOT NULL UNIQUE REFERENCES node_versions(id),
    published_at TEXT NOT NULL
) STRICT;

CREATE VIRTUAL TABLE node_search USING fts5(
    node_id UNINDEXED,
    project_id UNINDEXED,
    name,
    qualified_name,
    data_type,
    tokenize = 'unicode61'
);

CREATE TRIGGER node_versions_no_update
BEFORE UPDATE ON node_versions
BEGIN
    SELECT RAISE(ABORT, 'node_versions are append-only');
END;

CREATE TRIGGER node_versions_no_delete
BEFORE DELETE ON node_versions
BEGIN
    SELECT RAISE(ABORT, 'node_versions are append-only');
END;

CREATE TABLE jobs (
    id INTEGER PRIMARY KEY CHECK (id > 0),
    project_id INTEGER NOT NULL REFERENCES projects(id),
    job_type INTEGER NOT NULL,
    status INTEGER NOT NULL,
    payload_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(payload_json)),
    result_json TEXT CHECK (result_json IS NULL OR json_valid(result_json)),
    error_code TEXT,
    error_message TEXT,
    created_at TEXT NOT NULL,
    started_at TEXT,
    completed_at TEXT,
    revision_no INTEGER NOT NULL DEFAULT 1 CHECK (revision_no > 0)
) STRICT;

CREATE INDEX jobs_project_status_idx ON jobs(project_id, status, created_at);

CREATE TABLE audit_events (
    id INTEGER PRIMARY KEY CHECK (id > 0),
    project_id INTEGER REFERENCES projects(id),
    actor TEXT NOT NULL CHECK (length(actor) BETWEEN 1 AND 200),
    origin INTEGER NOT NULL,
    action TEXT NOT NULL CHECK (length(action) BETWEEN 1 AND 100),
    subject_type TEXT NOT NULL CHECK (length(subject_type) BETWEEN 1 AND 100),
    subject_id INTEGER,
    reason TEXT NOT NULL CHECK (length(reason) BETWEEN 1 AND 2000),
    request_id TEXT NOT NULL CHECK (length(request_id) BETWEEN 1 AND 200),
    expected_revision_no INTEGER,
    details_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(details_json)),
    occurred_at TEXT NOT NULL
) STRICT;

CREATE INDEX audit_events_project_time_idx ON audit_events(project_id, occurred_at, id);
CREATE INDEX audit_events_subject_idx ON audit_events(subject_type, subject_id, occurred_at);

CREATE TRIGGER audit_events_no_update
BEFORE UPDATE ON audit_events
BEGIN
    SELECT RAISE(ABORT, 'audit_events are append-only');
END;

CREATE TRIGGER audit_events_no_delete
BEFORE DELETE ON audit_events
BEGIN
    SELECT RAISE(ABORT, 'audit_events are append-only');
END;
