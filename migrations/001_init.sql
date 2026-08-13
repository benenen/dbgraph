-- dbgraph schema.
--
-- One file, one version. The database has a single shape and this is it: there
-- is no upgrade path to maintain because nothing is deployed yet to upgrade.
-- When that changes, later files are added beside this one and this one stops
-- moving.
--
-- Every table is STRICT. Enum columns are checked by trigger rather than by a
-- CHECK constraint so a rejection names the column that was wrong. Append-only
-- history — audit events, relation events, node and relation versions — is
-- enforced by triggers that refuse UPDATE and DELETE, not by convention.

-- Tables ----------------------------------------------------------------

CREATE TABLE access_credentials (
    actor TEXT PRIMARY KEY CHECK (length(actor) BETWEEN 1 AND 200),
    role INTEGER NOT NULL CHECK (role BETWEEN 1 AND 5),
    origin INTEGER NOT NULL CHECK (origin BETWEEN 1 AND 3),
    token_digest BLOB NOT NULL CHECK (length(token_digest) = 32),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE "audit_events" (
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

CREATE TABLE "data_sources" (
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

CREATE TABLE "effective_edges" (
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

CREATE TABLE id_generator_states (
    node_id INTEGER PRIMARY KEY CHECK (node_id BETWEEN 0 AND 1023),
    high_water_id INTEGER NOT NULL CHECK (high_water_id > 0),
    updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE "jobs" (
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

CREATE TABLE node_current (
    node_id INTEGER PRIMARY KEY REFERENCES nodes(id),
    version_id INTEGER NOT NULL UNIQUE REFERENCES node_versions(id),
    published_at TEXT NOT NULL
) STRICT;

CREATE VIRTUAL TABLE node_search USING fts5(
    node_id UNINDEXED,
    name,
    qualified_name,
    data_type,
    tokenize = 'unicode61'
);

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

CREATE TABLE "nodes" (
    id INTEGER PRIMARY KEY CHECK (id > 0),
    data_source_id INTEGER NOT NULL REFERENCES data_sources(id),
    kind INTEGER NOT NULL,
    stable_key TEXT NOT NULL CHECK (length(stable_key) BETWEEN 1 AND 500),
    created_at TEXT NOT NULL,
    UNIQUE (data_source_id, stable_key)
) STRICT;

CREATE TABLE relation_current (
    relation_id INTEGER PRIMARY KEY REFERENCES relations(id),
    latest_revision_no INTEGER NOT NULL CHECK (latest_revision_no > 0),
    active_version_id INTEGER REFERENCES relation_versions(id),
    proposed_version_id INTEGER REFERENCES relation_versions(id),
    status INTEGER NOT NULL CHECK (status BETWEEN 0 AND 4),
    updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE "relation_events" (
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

CREATE VIRTUAL TABLE relation_evidence_search USING fts5(
    version_id UNINDEXED,
    node_id UNINDEXED,
    repository_name,
    file_path,
    symbol,
    constraint_name,
    tokenize = 'unicode61'
);

CREATE TABLE relation_init_batches (
    id INTEGER PRIMARY KEY CHECK (id > 0),
    session_id INTEGER NOT NULL REFERENCES relation_init_sessions(id),
    batch_no INTEGER NOT NULL CHECK (batch_no > 0),
    idempotency_key TEXT NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 200),
    payload_digest TEXT NOT NULL CHECK (length(payload_digest) = 64),
    proposal_count INTEGER NOT NULL CHECK (proposal_count >= 0),
    unresolved_count INTEGER NOT NULL CHECK (unresolved_count >= 0),
    result_json TEXT NOT NULL CHECK (json_valid(result_json) AND length(result_json) <= 100000),
    accepted_at TEXT NOT NULL,
    UNIQUE (session_id, batch_no),
    UNIQUE (session_id, idempotency_key)
) STRICT;

CREATE TABLE relation_init_reproposal_candidates (
    session_id INTEGER NOT NULL REFERENCES relation_init_sessions(id),
    batch_id INTEGER NOT NULL REFERENCES relation_init_batches(id),
    relation_id INTEGER NOT NULL REFERENCES relations(id),
    candidate_json TEXT NOT NULL CHECK (json_valid(candidate_json) AND length(candidate_json) <= 1000000),
    created_at TEXT NOT NULL,
    PRIMARY KEY (session_id, relation_id),
    UNIQUE (batch_id, relation_id)
) STRICT;

CREATE TABLE relation_init_seen_relations (
    session_id INTEGER NOT NULL REFERENCES relation_init_sessions(id),
    relation_id INTEGER NOT NULL REFERENCES relations(id),
    batch_id INTEGER NOT NULL REFERENCES relation_init_batches(id),
    PRIMARY KEY (session_id, relation_id)
) STRICT;

CREATE TABLE "relation_init_sessions" (
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

CREATE TABLE relation_origins (
    relation_id INTEGER PRIMARY KEY REFERENCES relations(id),
    repository_id INTEGER NOT NULL REFERENCES repositories(id),
    first_session_id INTEGER NOT NULL REFERENCES relation_init_sessions(id),
    created_at TEXT NOT NULL
) STRICT;

CREATE TABLE relation_references (
    version_id INTEGER NOT NULL REFERENCES relation_versions(id),
    reference_kind INTEGER NOT NULL CHECK (reference_kind BETWEEN 1 AND 3),
    node_id INTEGER NOT NULL REFERENCES nodes(id),
    PRIMARY KEY (version_id, reference_kind, node_id)
) STRICT;

CREATE TABLE relation_stale_versions (
    version_id INTEGER PRIMARY KEY REFERENCES relation_versions(id)
) STRICT;

CREATE TABLE relation_version_endpoints (
    version_id INTEGER NOT NULL REFERENCES relation_versions(id),
    endpoint_kind INTEGER NOT NULL CHECK (endpoint_kind BETWEEN 1 AND 2),
    node_id INTEGER NOT NULL REFERENCES nodes(id),
    PRIMARY KEY (version_id, endpoint_kind)
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

CREATE TABLE "relations" (
    id INTEGER PRIMARY KEY CHECK (id > 0),
    relation_type INTEGER NOT NULL CHECK (relation_type BETWEEN 1 AND 2),
    create_fingerprint TEXT NOT NULL CHECK (length(create_fingerprint) = 64),
    created_at TEXT NOT NULL,
    UNIQUE (create_fingerprint)
) STRICT;

CREATE TABLE "repositories" (
    id INTEGER PRIMARY KEY CHECK (id > 0),
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    remote_url TEXT NOT NULL DEFAULT '' CHECK (length(remote_url) <= 2000),
    default_branch TEXT NOT NULL DEFAULT '' CHECK (length(default_branch) <= 500),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (name)
) STRICT;

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

CREATE TABLE "schema_scan_runs" (
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

CREATE TABLE "unresolved_findings" (
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

-- Indexes ---------------------------------------------------------------

CREATE UNIQUE INDEX access_credentials_digest_idx ON access_credentials(token_digest);

CREATE INDEX audit_events_subject_idx ON audit_events(subject_type, subject_id, occurred_at);

CREATE INDEX audit_events_time_idx ON audit_events(occurred_at, id);

CREATE INDEX declared_foreign_key_relations_present_idx
ON declared_foreign_key_relations(data_source_id, is_present, relation_id);

CREATE INDEX effective_edges_source_idx
ON effective_edges(source_node_id, target_node_id, relation_id);

CREATE INDEX effective_edges_target_idx
ON effective_edges(target_node_id, source_node_id, relation_id);

CREATE INDEX jobs_status_idx ON jobs(status, created_at);

CREATE INDEX node_versions_node_time_idx ON node_versions(node_id, created_at, id);

CREATE INDEX node_versions_qualified_idx ON node_versions(qualified_name, status, id);

CREATE INDEX nodes_kind_idx ON nodes(kind, id);

CREATE INDEX relation_events_relation_time_idx
ON relation_events(relation_id, occurred_at, id);

CREATE INDEX relation_init_reproposal_candidates_batch_idx
ON relation_init_reproposal_candidates(batch_id, relation_id);

CREATE INDEX relation_init_sessions_repository_time_idx
ON relation_init_sessions(repository_id, created_at, id);

CREATE INDEX relation_origins_repository_idx
ON relation_origins(repository_id, relation_id);

CREATE INDEX relation_references_node_idx
ON relation_references(node_id, reference_kind, version_id);

CREATE INDEX schema_scan_foreign_keys_relation_idx
ON schema_scan_foreign_keys(relation_id, scan_run_id);

CREATE INDEX schema_scan_runs_source_time_idx
ON schema_scan_runs(data_source_id, started_at, id);

CREATE INDEX unresolved_findings_status_idx
ON unresolved_findings(status, created_at, id);

-- Triggers --------------------------------------------------------------

CREATE TRIGGER audit_events_no_delete
BEFORE DELETE ON audit_events
BEGIN
    SELECT RAISE(ABORT, 'audit_events are append-only');
END;
CREATE TRIGGER audit_events_no_update
BEFORE UPDATE ON audit_events
BEGIN
    SELECT RAISE(ABORT, 'audit_events are append-only');
END;

CREATE TRIGGER audit_events_origin_check_insert
BEFORE INSERT ON audit_events
WHEN NEW.origin NOT BETWEEN 1 AND 3
BEGIN
    SELECT RAISE(ABORT, 'invalid audit origin');
END;

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

CREATE TRIGGER node_versions_no_delete
BEFORE DELETE ON node_versions
BEGIN
    SELECT RAISE(ABORT, 'node_versions are append-only');
END;

CREATE TRIGGER node_versions_no_update
BEFORE UPDATE ON node_versions
BEGIN
    SELECT RAISE(ABORT, 'node_versions are append-only');
END;

CREATE TRIGGER node_versions_status_check_insert
BEFORE INSERT ON node_versions
WHEN NEW.status NOT BETWEEN 1 AND 2
BEGIN
    SELECT RAISE(ABORT, 'invalid node version status');
END;

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

CREATE TRIGGER relation_current_pair_check_insert
BEFORE INSERT ON relation_current
WHEN
    (NEW.status = 0 AND NEW.active_version_id IS NOT NULL)
    OR (NEW.status <> 0 AND NEW.active_version_id IS NULL)
    OR NOT EXISTS (
        SELECT 1 FROM relation_versions
        WHERE relation_id = NEW.relation_id AND revision_no = NEW.latest_revision_no
    )
    OR (
        NEW.active_version_id IS NOT NULL
        AND NOT EXISTS (
            SELECT 1 FROM relation_versions
            WHERE id = NEW.active_version_id AND relation_id = NEW.relation_id
        )
    )
    OR (
        NEW.proposed_version_id IS NOT NULL
        AND NOT EXISTS (
            SELECT 1 FROM relation_versions
            WHERE id = NEW.proposed_version_id
              AND relation_id = NEW.relation_id
              AND revision_no = NEW.latest_revision_no
        )
    )
    OR NEW.active_version_id = NEW.proposed_version_id
    OR (
        NEW.status IN (1, 2)
        AND (
            NOT EXISTS (
                SELECT 1 FROM relation_versions
                WHERE id = NEW.active_version_id AND proposal_kind = 1
            )
            OR EXISTS (
                SELECT 1 FROM relation_stale_versions
                WHERE version_id = NEW.active_version_id
            )
        )
    )
    OR (
        NEW.status = 3
        AND (
            NOT EXISTS (
                SELECT 1 FROM relation_versions
                WHERE id = NEW.active_version_id AND proposal_kind = 2
            )
            OR EXISTS (
                SELECT 1 FROM relation_stale_versions
                WHERE version_id = NEW.active_version_id
            )
        )
    )
    OR (
        NEW.status = 4
        AND (
            NOT EXISTS (
                SELECT 1 FROM relation_versions
                WHERE id = NEW.active_version_id AND proposal_kind = 2
            )
            OR NOT EXISTS (
                SELECT 1 FROM relation_stale_versions
                WHERE version_id = NEW.active_version_id
            )
        )
    )
BEGIN
    SELECT RAISE(ABORT, 'invalid relation current projection');
END;

CREATE TRIGGER relation_current_pair_check_update
BEFORE UPDATE ON relation_current
WHEN
    (NEW.status = 0 AND NEW.active_version_id IS NOT NULL)
    OR (NEW.status <> 0 AND NEW.active_version_id IS NULL)
    OR NOT EXISTS (
        SELECT 1 FROM relation_versions
        WHERE relation_id = NEW.relation_id AND revision_no = NEW.latest_revision_no
    )
    OR (
        NEW.active_version_id IS NOT NULL
        AND NOT EXISTS (
            SELECT 1 FROM relation_versions
            WHERE id = NEW.active_version_id AND relation_id = NEW.relation_id
        )
    )
    OR (
        NEW.proposed_version_id IS NOT NULL
        AND NOT EXISTS (
            SELECT 1 FROM relation_versions
            WHERE id = NEW.proposed_version_id
              AND relation_id = NEW.relation_id
              AND revision_no = NEW.latest_revision_no
        )
    )
    OR NEW.active_version_id = NEW.proposed_version_id
    OR (
        NEW.status IN (1, 2)
        AND (
            NOT EXISTS (
                SELECT 1 FROM relation_versions
                WHERE id = NEW.active_version_id AND proposal_kind = 1
            )
            OR EXISTS (
                SELECT 1 FROM relation_stale_versions
                WHERE version_id = NEW.active_version_id
            )
        )
    )
    OR (
        NEW.status = 3
        AND (
            NOT EXISTS (
                SELECT 1 FROM relation_versions
                WHERE id = NEW.active_version_id AND proposal_kind = 2
            )
            OR EXISTS (
                SELECT 1 FROM relation_stale_versions
                WHERE version_id = NEW.active_version_id
            )
        )
    )
    OR (
        NEW.status = 4
        AND (
            NOT EXISTS (
                SELECT 1 FROM relation_versions
                WHERE id = NEW.active_version_id AND proposal_kind = 2
            )
            OR NOT EXISTS (
                SELECT 1 FROM relation_stale_versions
                WHERE version_id = NEW.active_version_id
            )
        )
    )
BEGIN
    SELECT RAISE(ABORT, 'invalid relation current projection');
END;

CREATE TRIGGER relation_events_no_delete
BEFORE DELETE ON relation_events
BEGIN
    SELECT RAISE(ABORT, 'relation_events are append-only');
END;

CREATE TRIGGER relation_events_no_update
BEFORE UPDATE ON relation_events
BEGIN
    SELECT RAISE(ABORT, 'relation_events are append-only');
END;

CREATE TRIGGER relation_evidence_no_delete
BEFORE DELETE ON relation_evidence
BEGIN
    SELECT RAISE(ABORT, 'relation evidence is append-only');
END;

CREATE TRIGGER relation_evidence_no_update
BEFORE UPDATE ON relation_evidence
BEGIN
    SELECT RAISE(ABORT, 'relation evidence is append-only');
END;

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

CREATE TRIGGER relation_init_batches_no_delete
BEFORE DELETE ON relation_init_batches
BEGIN
    SELECT RAISE(ABORT, 'relation_init_batches are append-only');
END;

CREATE TRIGGER relation_init_batches_no_update
BEFORE UPDATE ON relation_init_batches
BEGIN
    SELECT RAISE(ABORT, 'relation_init_batches are append-only');
END;

CREATE TRIGGER relation_init_reproposal_candidates_no_delete
BEFORE DELETE ON relation_init_reproposal_candidates
BEGIN
    SELECT RAISE(ABORT, 'relation init reproposal candidates are append-only');
END;

CREATE TRIGGER relation_init_reproposal_candidates_no_update
BEFORE UPDATE ON relation_init_reproposal_candidates
BEGIN
    SELECT RAISE(ABORT, 'relation init reproposal candidates are append-only');
END;

CREATE TRIGGER relation_references_no_delete
BEFORE DELETE ON relation_references
BEGIN
    SELECT RAISE(ABORT, 'relation references are append-only');
END;

CREATE TRIGGER relation_references_no_update
BEFORE UPDATE ON relation_references
BEGIN
    SELECT RAISE(ABORT, 'relation references are append-only');
END;

CREATE TRIGGER relation_stale_versions_no_delete
BEFORE DELETE ON relation_stale_versions
BEGIN
    SELECT RAISE(ABORT, 'relation stale markers are append-only');
END;

CREATE TRIGGER relation_stale_versions_no_update
BEFORE UPDATE ON relation_stale_versions
BEGIN
    SELECT RAISE(ABORT, 'relation stale markers are append-only');
END;

CREATE TRIGGER relation_version_endpoints_no_delete
BEFORE DELETE ON relation_version_endpoints
BEGIN
    SELECT RAISE(ABORT, 'relation version endpoints are append-only');
END;

CREATE TRIGGER relation_version_endpoints_no_update
BEFORE UPDATE ON relation_version_endpoints
BEGIN
    SELECT RAISE(ABORT, 'relation version endpoints are append-only');
END;

CREATE TRIGGER relation_versions_no_delete
BEFORE DELETE ON relation_versions
BEGIN
    SELECT RAISE(ABORT, 'relation_versions are append-only');
END;

CREATE TRIGGER relation_versions_no_update
BEFORE UPDATE ON relation_versions
BEGIN
    SELECT RAISE(ABORT, 'relation_versions are append-only');
END;

CREATE TRIGGER schema_scan_foreign_keys_no_delete
BEFORE DELETE ON schema_scan_foreign_keys
BEGIN
    SELECT RAISE(ABORT, 'schema scan foreign keys are append-only');
END;

CREATE TRIGGER schema_scan_foreign_keys_no_update
BEFORE UPDATE ON schema_scan_foreign_keys
BEGIN
    SELECT RAISE(ABORT, 'schema scan foreign keys are append-only');
END;

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
