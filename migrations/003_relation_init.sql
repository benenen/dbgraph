CREATE TABLE relation_init_sessions (
    id INTEGER PRIMARY KEY CHECK (id > 0),
    project_id INTEGER NOT NULL REFERENCES projects(id),
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
    UNIQUE (project_id, actor, origin, request_id)
) STRICT;

CREATE INDEX relation_init_sessions_repository_time_idx
ON relation_init_sessions(repository_id, created_at, id);

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

CREATE TRIGGER relation_init_batches_no_update
BEFORE UPDATE ON relation_init_batches
BEGIN
    SELECT RAISE(ABORT, 'relation_init_batches are append-only');
END;

CREATE TRIGGER relation_init_batches_no_delete
BEFORE DELETE ON relation_init_batches
BEGIN
    SELECT RAISE(ABORT, 'relation_init_batches are append-only');
END;

CREATE TABLE relation_init_seen_relations (
    session_id INTEGER NOT NULL REFERENCES relation_init_sessions(id),
    relation_id INTEGER NOT NULL REFERENCES relations(id),
    batch_id INTEGER NOT NULL REFERENCES relation_init_batches(id),
    PRIMARY KEY (session_id, relation_id)
) STRICT;

CREATE TABLE relation_origins (
    relation_id INTEGER PRIMARY KEY REFERENCES relations(id),
    repository_id INTEGER NOT NULL REFERENCES repositories(id),
    first_session_id INTEGER NOT NULL REFERENCES relation_init_sessions(id),
    created_at TEXT NOT NULL
) STRICT;

CREATE INDEX relation_origins_repository_idx
ON relation_origins(repository_id, relation_id);

CREATE TABLE unresolved_findings (
    id INTEGER PRIMARY KEY CHECK (id > 0),
    project_id INTEGER NOT NULL REFERENCES projects(id),
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

CREATE INDEX unresolved_findings_project_status_idx
ON unresolved_findings(project_id, status, created_at, id);
