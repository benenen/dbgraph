CREATE TABLE relation_stale_versions (
    version_id INTEGER PRIMARY KEY REFERENCES relation_versions(id)
) STRICT;

CREATE TRIGGER relation_stale_versions_no_update
BEFORE UPDATE ON relation_stale_versions
BEGIN
    SELECT RAISE(ABORT, 'relation stale markers are append-only');
END;

CREATE TRIGGER relation_stale_versions_no_delete
BEFORE DELETE ON relation_stale_versions
BEGIN
    SELECT RAISE(ABORT, 'relation stale markers are append-only');
END;

DROP TRIGGER relation_events_no_update;
DROP TRIGGER relation_events_no_delete;

ALTER TABLE relation_events RENAME TO relation_events_before_stale;

CREATE TABLE relation_events (
    id INTEGER PRIMARY KEY CHECK (id > 0),
    project_id INTEGER NOT NULL REFERENCES projects(id),
    relation_id INTEGER NOT NULL REFERENCES relations(id),
    version_id INTEGER REFERENCES relation_versions(id),
    event_type INTEGER NOT NULL CHECK (event_type BETWEEN 1 AND 8),
    actor TEXT NOT NULL CHECK (length(actor) BETWEEN 1 AND 200),
    origin INTEGER NOT NULL CHECK (origin BETWEEN 1 AND 3),
    reason TEXT NOT NULL CHECK (length(reason) BETWEEN 1 AND 2000),
    request_id TEXT NOT NULL CHECK (length(request_id) BETWEEN 1 AND 200),
    expected_revision_no INTEGER CHECK (expected_revision_no IS NULL OR expected_revision_no > 0),
    occurred_at TEXT NOT NULL,
    UNIQUE (project_id, actor, origin, request_id, event_type)
) STRICT;

INSERT INTO relation_events(
    id, project_id, relation_id, version_id, event_type, actor, origin,
    reason, request_id, expected_revision_no, occurred_at
)
SELECT
    id, project_id, relation_id, version_id, event_type, actor, origin,
    reason, request_id, expected_revision_no, occurred_at
FROM relation_events_before_stale;

DROP TABLE relation_events_before_stale;

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

DROP TRIGGER relation_current_pair_check_insert;
DROP TRIGGER relation_current_pair_check_update;

ALTER TABLE relation_current RENAME TO relation_current_before_stale;

CREATE TABLE relation_current (
    relation_id INTEGER PRIMARY KEY REFERENCES relations(id),
    latest_revision_no INTEGER NOT NULL CHECK (latest_revision_no > 0),
    active_version_id INTEGER REFERENCES relation_versions(id),
    proposed_version_id INTEGER REFERENCES relation_versions(id),
    status INTEGER NOT NULL CHECK (status BETWEEN 0 AND 4),
    updated_at TEXT NOT NULL
) STRICT;

INSERT INTO relation_current(
    relation_id, latest_revision_no, active_version_id,
    proposed_version_id, status, updated_at
)
SELECT
    relation_id, latest_revision_no, active_version_id,
    proposed_version_id, status, updated_at
FROM relation_current_before_stale;

DROP TABLE relation_current_before_stale;

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
