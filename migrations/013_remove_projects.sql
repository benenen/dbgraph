-- dbgraph:no-transaction
--
-- Remove the project.
--
-- A project scoped the catalog, relations and audit so that separate workspaces
-- could model overlapping databases independently. In practice one server holds
-- one workspace: the console never exposed a project, `serve` guaranteed exactly
-- one, and every data source was linked to it. The column bought nothing and
-- cost a segment in every URL, an argument in every MCP tool, and a term in
-- eleven uniqueness constraints.
--
-- Uniqueness narrows accordingly. A stable key was unique per project and data
-- source; it is now unique per data source. A relation fingerprint was unique
-- per project; it is now unique outright. Any row that relied on two projects
-- holding the same key would collide here — there is only ever one project, so
-- none can.
--
-- Nearly every table is referenced by a foreign key, so this runs outside the
-- surrounding transaction with foreign keys disabled, per SQLite's documented
-- rebuild procedure. The runner verifies foreign_key_check before recording it.

PRAGMA legacy_alter_table=OFF;

-- The search trigger reads `relations`. Rebuilding a table re-validates every
-- dependent object, so a trigger naming a table that is momentarily absent
-- fails the rebuild. It is dropped first and recreated with the search indexes.
DROP TRIGGER relation_evidence_search_insert;

-- nodes -----------------------------------------------------------------

CREATE TABLE nodes_rebuilt (
    id INTEGER PRIMARY KEY CHECK (id > 0),
    data_source_id INTEGER NOT NULL REFERENCES data_sources(id),
    kind INTEGER NOT NULL,
    stable_key TEXT NOT NULL CHECK (length(stable_key) BETWEEN 1 AND 500),
    created_at TEXT NOT NULL,
    UNIQUE (data_source_id, stable_key)
) STRICT;

INSERT INTO nodes_rebuilt(id, data_source_id, kind, stable_key, created_at)
SELECT id, data_source_id, kind, stable_key, created_at FROM nodes;

DROP TABLE nodes;
ALTER TABLE nodes_rebuilt RENAME TO nodes;

CREATE INDEX nodes_kind_idx ON nodes(kind, id);

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

-- relations -------------------------------------------------------------

CREATE TABLE relations_rebuilt (
    id INTEGER PRIMARY KEY CHECK (id > 0),
    relation_type INTEGER NOT NULL CHECK (relation_type BETWEEN 1 AND 2),
    create_fingerprint TEXT NOT NULL CHECK (length(create_fingerprint) = 64),
    created_at TEXT NOT NULL,
    UNIQUE (create_fingerprint)
) STRICT;

INSERT INTO relations_rebuilt(id, relation_type, create_fingerprint, created_at)
SELECT id, relation_type, create_fingerprint, created_at FROM relations;

DROP TABLE relations;
ALTER TABLE relations_rebuilt RENAME TO relations;

-- effective_edges -------------------------------------------------------

CREATE TABLE effective_edges_rebuilt (
    relation_id INTEGER PRIMARY KEY REFERENCES relations(id),
    version_id INTEGER NOT NULL UNIQUE REFERENCES relation_versions(id),
    source_node_id INTEGER NOT NULL REFERENCES nodes(id),
    target_node_id INTEGER NOT NULL REFERENCES nodes(id),
    relation_type INTEGER NOT NULL CHECK (relation_type BETWEEN 1 AND 2),
    guard_json TEXT CHECK (guard_json IS NULL OR json_valid(guard_json)),
    selector_json TEXT CHECK (selector_json IS NULL OR json_valid(selector_json)),
    transform_json TEXT NOT NULL CHECK (json_valid(transform_json)),
    confidence_bps INTEGER NOT NULL CHECK (confidence_bps BETWEEN 0 AND 10000),
    published_at TEXT NOT NULL
) STRICT;

INSERT INTO effective_edges_rebuilt(
    relation_id, version_id, source_node_id, target_node_id, relation_type,
    guard_json, selector_json, transform_json, confidence_bps, published_at
)
SELECT relation_id, version_id, source_node_id, target_node_id, relation_type,
       guard_json, selector_json, transform_json, confidence_bps, published_at
FROM effective_edges;

DROP TABLE effective_edges;
ALTER TABLE effective_edges_rebuilt RENAME TO effective_edges;

CREATE INDEX effective_edges_source_idx
ON effective_edges(source_node_id, target_node_id, relation_id);

CREATE INDEX effective_edges_target_idx
ON effective_edges(target_node_id, source_node_id, relation_id);

-- relation_events -------------------------------------------------------

CREATE TABLE relation_events_rebuilt (
    id INTEGER PRIMARY KEY CHECK (id > 0),
    relation_id INTEGER NOT NULL REFERENCES relations(id),
    version_id INTEGER REFERENCES relation_versions(id),
    event_type INTEGER NOT NULL CHECK (event_type BETWEEN 1 AND 8),
    actor TEXT NOT NULL CHECK (length(actor) BETWEEN 1 AND 200),
    origin INTEGER NOT NULL CHECK (origin BETWEEN 1 AND 3),
    reason TEXT NOT NULL CHECK (length(reason) BETWEEN 1 AND 2000),
    request_id TEXT NOT NULL CHECK (length(request_id) BETWEEN 1 AND 200),
    expected_revision_no INTEGER CHECK (expected_revision_no IS NULL OR expected_revision_no > 0),
    occurred_at TEXT NOT NULL,
    UNIQUE (actor, origin, request_id, event_type)
) STRICT;

INSERT INTO relation_events_rebuilt(
    id, relation_id, version_id, event_type, actor, origin, reason,
    request_id, expected_revision_no, occurred_at
)
SELECT id, relation_id, version_id, event_type, actor, origin, reason,
       request_id, expected_revision_no, occurred_at
FROM relation_events;

DROP TABLE relation_events;
ALTER TABLE relation_events_rebuilt RENAME TO relation_events;

CREATE INDEX relation_events_relation_time_idx
ON relation_events(relation_id, occurred_at, id);

CREATE TRIGGER relation_events_no_update
BEFORE UPDATE ON relation_events
BEGIN
    SELECT RAISE(ABORT, 'relation_events are append-only');
END;

CREATE TRIGGER relation_events_no_delete
BEFORE DELETE ON relation_events
BEGIN
    SELECT RAISE(ABORT, 'relation_events are append-only');
END;

-- repositories ----------------------------------------------------------

CREATE TABLE repositories_rebuilt (
    id INTEGER PRIMARY KEY CHECK (id > 0),
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    remote_url TEXT NOT NULL DEFAULT '' CHECK (length(remote_url) <= 2000),
    default_branch TEXT NOT NULL DEFAULT '' CHECK (length(default_branch) <= 500),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (name)
) STRICT;

INSERT INTO repositories_rebuilt(id, name, remote_url, default_branch, created_at, updated_at)
SELECT id, name, remote_url, default_branch, created_at, updated_at FROM repositories;

DROP TABLE repositories;
ALTER TABLE repositories_rebuilt RENAME TO repositories;

-- relation_init_sessions ------------------------------------------------

CREATE TABLE relation_init_sessions_rebuilt (
    id INTEGER PRIMARY KEY CHECK (id > 0),
    repository_id INTEGER NOT NULL REFERENCES repositories(id),
    mode INTEGER NOT NULL CHECK (mode BETWEEN 1 AND 2),
    source_commit TEXT NOT NULL CHECK (length(source_commit) BETWEEN 1 AND 200),
    scope_json TEXT NOT NULL CHECK (json_valid(scope_json) AND length(scope_json) <= 20000),
    status INTEGER NOT NULL CHECK (status BETWEEN 1 AND 4),
    actor TEXT NOT NULL CHECK (length(actor) BETWEEN 1 AND 200),
    origin INTEGER NOT NULL CHECK (origin BETWEEN 1 AND 3),
    request_id TEXT NOT NULL CHECK (length(request_id) BETWEEN 1 AND 200),
    created_at TEXT NOT NULL,
    completed_at TEXT,
    UNIQUE (actor, origin, request_id)
) STRICT;

INSERT INTO relation_init_sessions_rebuilt(
    id, repository_id, mode, source_commit, scope_json, status, actor, origin,
    request_id, created_at, completed_at
)
SELECT id, repository_id, mode, source_commit, scope_json, status, actor, origin,
       request_id, created_at, completed_at
FROM relation_init_sessions;

DROP TABLE relation_init_sessions;
ALTER TABLE relation_init_sessions_rebuilt RENAME TO relation_init_sessions;

CREATE INDEX relation_init_sessions_repository_time_idx
ON relation_init_sessions(repository_id, created_at, id);

-- unresolved_findings ---------------------------------------------------

CREATE TABLE unresolved_findings_rebuilt (
    id INTEGER PRIMARY KEY CHECK (id > 0),
    repository_id INTEGER NOT NULL REFERENCES repositories(id),
    session_id INTEGER REFERENCES relation_init_sessions(id),
    batch_id INTEGER REFERENCES relation_init_batches(id),
    fingerprint TEXT NOT NULL CHECK (length(fingerprint) = 64),
    finding_type TEXT NOT NULL CHECK (length(finding_type) BETWEEN 1 AND 100),
    summary TEXT NOT NULL CHECK (length(summary) BETWEEN 1 AND 2000),
    evidence_json TEXT NOT NULL CHECK (json_valid(evidence_json) AND length(evidence_json) <= 20000),
    status INTEGER NOT NULL CHECK (status BETWEEN 1 AND 3),
    actor TEXT NOT NULL CHECK (length(actor) BETWEEN 1 AND 200),
    origin INTEGER NOT NULL CHECK (origin BETWEEN 1 AND 3),
    created_at TEXT NOT NULL,
    UNIQUE (repository_id, fingerprint, status)
) STRICT;

INSERT INTO unresolved_findings_rebuilt(
    id, repository_id, session_id, batch_id, fingerprint, finding_type, summary,
    evidence_json, status, actor, origin, created_at
)
SELECT id, repository_id, session_id, batch_id, fingerprint, finding_type, summary,
       evidence_json, status, actor, origin, created_at
FROM unresolved_findings;

DROP TABLE unresolved_findings;
ALTER TABLE unresolved_findings_rebuilt RENAME TO unresolved_findings;

CREATE INDEX unresolved_findings_status_idx
ON unresolved_findings(status, created_at, id);

-- schema_scan_runs ------------------------------------------------------

CREATE TABLE schema_scan_runs_rebuilt (
    id INTEGER PRIMARY KEY CHECK (id > 0),
    data_source_id INTEGER NOT NULL REFERENCES data_sources(id),
    status INTEGER NOT NULL,
    node_count INTEGER NOT NULL DEFAULT 0 CHECK (node_count >= 0),
    stale_count INTEGER NOT NULL DEFAULT 0 CHECK (stale_count >= 0),
    error_code TEXT,
    error_message TEXT,
    started_at TEXT NOT NULL,
    completed_at TEXT
) STRICT;

INSERT INTO schema_scan_runs_rebuilt(
    id, data_source_id, status, node_count, stale_count, error_code,
    error_message, started_at, completed_at
)
SELECT id, data_source_id, status, node_count, stale_count, error_code,
       error_message, started_at, completed_at
FROM schema_scan_runs;

DROP TABLE schema_scan_runs;
ALTER TABLE schema_scan_runs_rebuilt RENAME TO schema_scan_runs;

CREATE INDEX schema_scan_runs_source_time_idx
ON schema_scan_runs(data_source_id, started_at, id);

CREATE TRIGGER schema_scan_runs_status_check_insert
BEFORE INSERT ON schema_scan_runs
WHEN NEW.status NOT BETWEEN 1 AND 3
BEGIN
    SELECT RAISE(ABORT, 'invalid schema scan status');
END;

CREATE TRIGGER schema_scan_runs_status_check_update
BEFORE UPDATE OF status ON schema_scan_runs
WHEN NEW.status NOT BETWEEN 1 AND 3
BEGIN
    SELECT RAISE(ABORT, 'invalid schema scan status');
END;

-- jobs ------------------------------------------------------------------

CREATE TABLE jobs_rebuilt (
    id INTEGER PRIMARY KEY CHECK (id > 0),
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

INSERT INTO jobs_rebuilt(
    id, job_type, status, payload_json, result_json, error_code, error_message,
    created_at, started_at, completed_at, revision_no
)
SELECT id, job_type, status, payload_json, result_json, error_code, error_message,
       created_at, started_at, completed_at, revision_no
FROM jobs;

DROP TABLE jobs;
ALTER TABLE jobs_rebuilt RENAME TO jobs;

CREATE INDEX jobs_status_idx ON jobs(status, created_at);

CREATE TRIGGER jobs_enum_check_insert
BEFORE INSERT ON jobs
WHEN NEW.job_type <> 1 OR NEW.status NOT BETWEEN 1 AND 5
BEGIN
    SELECT RAISE(ABORT, 'invalid job enum');
END;

CREATE TRIGGER jobs_enum_check_update
BEFORE UPDATE OF job_type, status ON jobs
WHEN NEW.job_type <> 1 OR NEW.status NOT BETWEEN 1 AND 5
BEGIN
    SELECT RAISE(ABORT, 'invalid job enum');
END;

-- audit_events ----------------------------------------------------------
-- Rebuilt last among the audited tables: its append-only triggers are dropped
-- with the old table and recreated here, so nothing can quietly edit history
-- outside this migration.

CREATE TABLE audit_events_rebuilt (
    id INTEGER PRIMARY KEY CHECK (id > 0),
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

INSERT INTO audit_events_rebuilt(
    id, actor, origin, action, subject_type, subject_id, reason, request_id,
    expected_revision_no, details_json, occurred_at
)
SELECT id, actor, origin, action, subject_type, subject_id, reason, request_id,
       expected_revision_no, details_json, occurred_at
FROM audit_events;

DROP TABLE audit_events;
ALTER TABLE audit_events_rebuilt RENAME TO audit_events;

CREATE INDEX audit_events_time_idx ON audit_events(occurred_at, id);

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

CREATE TRIGGER audit_events_origin_check_insert
BEFORE INSERT ON audit_events
WHEN NEW.origin NOT BETWEEN 1 AND 3
BEGIN
    SELECT RAISE(ABORT, 'invalid audit origin');
END;

-- Search indexes --------------------------------------------------------
-- An FTS5 table cannot be altered, so each is dropped and rebuilt from the
-- rows it indexes. Rebuilding from source also repairs any drift.

DROP TABLE node_search;

CREATE VIRTUAL TABLE node_search USING fts5(
    node_id UNINDEXED,
    name,
    qualified_name,
    data_type,
    tokenize = 'unicode61'
);

INSERT INTO node_search(node_id, name, qualified_name, data_type)
SELECT n.id, nv.name, nv.qualified_name, nv.data_type
FROM nodes n
JOIN node_current nc ON nc.node_id = n.id
JOIN node_versions nv ON nv.id = nc.version_id;

DROP TABLE relation_evidence_search;

CREATE VIRTUAL TABLE relation_evidence_search USING fts5(
    version_id UNINDEXED,
    node_id UNINDEXED,
    repository_name,
    file_path,
    symbol,
    constraint_name,
    tokenize = 'unicode61'
);

INSERT INTO relation_evidence_search(
    version_id, node_id, repository_name, file_path, symbol, constraint_name
)
SELECT evidence.version_id, endpoint.node_id, evidence.repository_name,
       evidence.file_path, evidence.symbol, evidence.constraint_name
FROM relation_evidence evidence
JOIN relation_version_endpoints endpoint ON endpoint.version_id = evidence.version_id;

CREATE TRIGGER relation_evidence_search_insert
AFTER INSERT ON relation_evidence
BEGIN
    INSERT INTO relation_evidence_search(
        version_id, node_id, repository_name, file_path, symbol, constraint_name
    )
    SELECT
        NEW.version_id,
        endpoint.node_id,
        NEW.repository_name,
        NEW.file_path,
        NEW.symbol,
        NEW.constraint_name
    FROM relation_versions version
    JOIN relation_version_endpoints endpoint ON endpoint.version_id = version.id
    WHERE version.id = NEW.version_id;
END;

-- The project itself ----------------------------------------------------

DROP TABLE project_data_sources;

DROP TABLE projects;
