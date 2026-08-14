CREATE TABLE repository_identities (
    repository_id INTEGER NOT NULL REFERENCES repositories(id),
    identity_kind TEXT NOT NULL CHECK (identity_kind = 'GIT_REMOTE'),
    normalized_value TEXT NOT NULL CHECK (length(normalized_value) BETWEEN 1 AND 2000),
    PRIMARY KEY (repository_id, identity_kind, normalized_value)
) STRICT;

CREATE INDEX repository_identities_value_idx
ON repository_identities(identity_kind, normalized_value, repository_id);

CREATE TABLE repository_identity_backfill_state (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    last_repository_id INTEGER NOT NULL CHECK (last_repository_id >= 0),
    completed_at TEXT
) STRICT;

INSERT INTO repository_identity_backfill_state(singleton, last_repository_id)
VALUES (1, 0);

CREATE TABLE source_binding_sets (
    id INTEGER PRIMARY KEY CHECK (id > 0),
    repository_id INTEGER NOT NULL REFERENCES repositories(id),
    context_name TEXT NOT NULL CHECK (length(context_name) BETWEEN 1 AND 100),
    created_at TEXT NOT NULL,
    UNIQUE (repository_id, context_name)
) STRICT;

CREATE TABLE source_binding_revisions (
    id INTEGER PRIMARY KEY CHECK (id > 0),
    binding_set_id INTEGER NOT NULL REFERENCES source_binding_sets(id),
    revision_no INTEGER NOT NULL CHECK (revision_no > 0),
    expected_revision_no INTEGER NOT NULL CHECK (expected_revision_no BETWEEN 0 AND 1000000),
    actor TEXT NOT NULL CHECK (length(actor) BETWEEN 1 AND 200),
    origin INTEGER NOT NULL CHECK (origin BETWEEN 1 AND 2),
    reason TEXT NOT NULL CHECK (length(reason) BETWEEN 1 AND 2000),
    request_id TEXT NOT NULL CHECK (length(request_id) BETWEEN 1 AND 200),
    created_at TEXT NOT NULL,
    CHECK (revision_no = expected_revision_no + 1),
    UNIQUE (binding_set_id, id),
    UNIQUE (binding_set_id, revision_no),
    UNIQUE (actor, origin, request_id)
) STRICT;

CREATE TABLE source_binding_members (
    binding_revision_id INTEGER NOT NULL REFERENCES source_binding_revisions(id),
    data_source_id INTEGER NOT NULL REFERENCES data_sources(id),
    PRIMARY KEY (binding_revision_id, data_source_id)
) STRICT;

CREATE TABLE source_binding_current (
    binding_set_id INTEGER PRIMARY KEY REFERENCES source_binding_sets(id),
    binding_revision_id INTEGER NOT NULL UNIQUE,
    FOREIGN KEY (binding_set_id, binding_revision_id)
        REFERENCES source_binding_revisions(binding_set_id, id)
) STRICT;

CREATE INDEX source_binding_sets_repository_idx
ON source_binding_sets(repository_id, context_name);

CREATE INDEX source_binding_members_data_source_idx
ON source_binding_members(data_source_id, binding_revision_id);

CREATE TRIGGER source_binding_revisions_no_update
BEFORE UPDATE ON source_binding_revisions
BEGIN
    SELECT RAISE(ABORT, 'source binding revisions are append-only');
END;

CREATE TRIGGER source_binding_revisions_no_delete
BEFORE DELETE ON source_binding_revisions
BEGIN
    SELECT RAISE(ABORT, 'source binding revisions are append-only');
END;

CREATE TRIGGER source_binding_members_no_update
BEFORE UPDATE ON source_binding_members
BEGIN
    SELECT RAISE(ABORT, 'source binding members are append-only');
END;

CREATE TRIGGER source_binding_members_no_delete
BEFORE DELETE ON source_binding_members
BEGIN
    SELECT RAISE(ABORT, 'source binding members are append-only');
END;
