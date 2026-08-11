CREATE TABLE relations (
    id INTEGER PRIMARY KEY CHECK (id > 0),
    project_id INTEGER NOT NULL REFERENCES projects(id),
    relation_type INTEGER NOT NULL CHECK (relation_type BETWEEN 1 AND 2),
    create_fingerprint TEXT NOT NULL CHECK (length(create_fingerprint) = 64),
    created_at TEXT NOT NULL,
    UNIQUE (project_id, create_fingerprint)
) STRICT;

CREATE TABLE relation_versions (
    id INTEGER PRIMARY KEY CHECK (id > 0),
    relation_id INTEGER NOT NULL REFERENCES relations(id),
    revision_no INTEGER NOT NULL CHECK (revision_no > 0),
    proposal_kind INTEGER NOT NULL CHECK (proposal_kind BETWEEN 1 AND 2),
    confidence_bps INTEGER NOT NULL CHECK (confidence_bps BETWEEN 0 AND 10000),
    guard_json TEXT CHECK (guard_json IS NULL OR json_valid(guard_json)),
    selector_json TEXT CHECK (selector_json IS NULL OR json_valid(selector_json)),
    transform_json TEXT NOT NULL CHECK (json_valid(transform_json)),
    content_fingerprint TEXT NOT NULL CHECK (length(content_fingerprint) = 64),
    actor TEXT NOT NULL CHECK (length(actor) BETWEEN 1 AND 200),
    origin INTEGER NOT NULL CHECK (origin BETWEEN 1 AND 3),
    reason TEXT NOT NULL CHECK (length(reason) BETWEEN 1 AND 2000),
    request_id TEXT NOT NULL CHECK (length(request_id) BETWEEN 1 AND 200),
    expected_revision_no INTEGER CHECK (expected_revision_no IS NULL OR expected_revision_no > 0),
    created_at TEXT NOT NULL,
    UNIQUE (relation_id, revision_no)
) STRICT;

CREATE TRIGGER relation_versions_no_update
BEFORE UPDATE ON relation_versions
BEGIN
    SELECT RAISE(ABORT, 'relation_versions are append-only');
END;

CREATE TRIGGER relation_versions_no_delete
BEFORE DELETE ON relation_versions
BEGIN
    SELECT RAISE(ABORT, 'relation_versions are append-only');
END;

CREATE TABLE relation_version_endpoints (
    version_id INTEGER NOT NULL REFERENCES relation_versions(id),
    endpoint_kind INTEGER NOT NULL CHECK (endpoint_kind BETWEEN 1 AND 2),
    node_id INTEGER NOT NULL REFERENCES nodes(id),
    PRIMARY KEY (version_id, endpoint_kind)
) STRICT;

CREATE TABLE relation_references (
    version_id INTEGER NOT NULL REFERENCES relation_versions(id),
    reference_kind INTEGER NOT NULL CHECK (reference_kind BETWEEN 1 AND 3),
    node_id INTEGER NOT NULL REFERENCES nodes(id),
    PRIMARY KEY (version_id, reference_kind, node_id)
) STRICT;

CREATE INDEX relation_references_node_idx
ON relation_references(node_id, reference_kind, version_id);

CREATE TABLE relation_evidence (
    version_id INTEGER NOT NULL REFERENCES relation_versions(id),
    ordinal INTEGER NOT NULL CHECK (ordinal > 0),
    evidence_kind INTEGER NOT NULL CHECK (evidence_kind BETWEEN 1 AND 4),
    repository_name TEXT NOT NULL CHECK (length(repository_name) <= 500),
    commit_hash TEXT NOT NULL CHECK (length(commit_hash) <= 200),
    file_path TEXT NOT NULL CHECK (length(file_path) <= 2000),
    symbol TEXT NOT NULL CHECK (length(symbol) <= 2000),
    start_line INTEGER NOT NULL CHECK (start_line >= 0),
    end_line INTEGER NOT NULL CHECK (end_line >= start_line),
    data_source_id INTEGER REFERENCES data_sources(id),
    constraint_schema TEXT NOT NULL DEFAULT '' CHECK (length(constraint_schema) <= 500),
    constraint_name TEXT NOT NULL DEFAULT '' CHECK (length(constraint_name) <= 500),
    scan_run_id INTEGER REFERENCES schema_scan_runs(id),
    CHECK (
        (evidence_kind BETWEEN 1 AND 3 AND length(repository_name) >= 1 AND
         length(commit_hash) >= 1 AND length(file_path) >= 1 AND start_line > 0 AND
         data_source_id IS NULL AND constraint_schema = '' AND constraint_name = '' AND scan_run_id IS NULL)
        OR
        (evidence_kind = 4 AND repository_name = '' AND commit_hash = '' AND file_path = '' AND symbol = '' AND
         start_line = 0 AND end_line = 0 AND data_source_id IS NOT NULL AND
         length(constraint_schema) >= 1 AND length(constraint_name) >= 1 AND scan_run_id IS NOT NULL)
    ),
    PRIMARY KEY (version_id, ordinal)
) STRICT;

CREATE TABLE relation_events (
    id INTEGER PRIMARY KEY CHECK (id > 0),
    project_id INTEGER NOT NULL REFERENCES projects(id),
    relation_id INTEGER NOT NULL REFERENCES relations(id),
    version_id INTEGER REFERENCES relation_versions(id),
    event_type INTEGER NOT NULL CHECK (event_type BETWEEN 1 AND 7),
    actor TEXT NOT NULL CHECK (length(actor) BETWEEN 1 AND 200),
    origin INTEGER NOT NULL CHECK (origin BETWEEN 1 AND 3),
    reason TEXT NOT NULL CHECK (length(reason) BETWEEN 1 AND 2000),
    request_id TEXT NOT NULL CHECK (length(request_id) BETWEEN 1 AND 200),
    expected_revision_no INTEGER CHECK (expected_revision_no IS NULL OR expected_revision_no > 0),
    occurred_at TEXT NOT NULL,
    UNIQUE (project_id, actor, origin, request_id, event_type)
) STRICT;

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

CREATE TABLE relation_current (
    relation_id INTEGER PRIMARY KEY REFERENCES relations(id),
    latest_revision_no INTEGER NOT NULL CHECK (latest_revision_no > 0),
    active_version_id INTEGER REFERENCES relation_versions(id),
    proposed_version_id INTEGER REFERENCES relation_versions(id),
    status INTEGER NOT NULL CHECK (status BETWEEN 0 AND 3),
    updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE effective_edges (
    relation_id INTEGER PRIMARY KEY REFERENCES relations(id),
    version_id INTEGER NOT NULL UNIQUE REFERENCES relation_versions(id),
    project_id INTEGER NOT NULL REFERENCES projects(id),
    source_node_id INTEGER NOT NULL REFERENCES nodes(id),
    target_node_id INTEGER NOT NULL REFERENCES nodes(id),
    relation_type INTEGER NOT NULL CHECK (relation_type BETWEEN 1 AND 2),
    guard_json TEXT CHECK (guard_json IS NULL OR json_valid(guard_json)),
    selector_json TEXT CHECK (selector_json IS NULL OR json_valid(selector_json)),
    transform_json TEXT NOT NULL CHECK (json_valid(transform_json)),
    confidence_bps INTEGER NOT NULL CHECK (confidence_bps BETWEEN 0 AND 10000),
    published_at TEXT NOT NULL
) STRICT;

CREATE INDEX effective_edges_source_idx
ON effective_edges(project_id, source_node_id, target_node_id, relation_id);

CREATE INDEX effective_edges_target_idx
ON effective_edges(project_id, target_node_id, source_node_id, relation_id);

CREATE TABLE suppression_rules (
    id INTEGER PRIMARY KEY CHECK (id > 0),
    relation_id INTEGER NOT NULL REFERENCES relations(id),
    approved_version_id INTEGER NOT NULL REFERENCES relation_versions(id),
    actor TEXT NOT NULL CHECK (length(actor) BETWEEN 1 AND 200),
    origin INTEGER NOT NULL CHECK (origin BETWEEN 1 AND 3),
    reason TEXT NOT NULL CHECK (length(reason) BETWEEN 1 AND 2000),
    request_id TEXT NOT NULL CHECK (length(request_id) BETWEEN 1 AND 200),
    created_at TEXT NOT NULL
) STRICT;
