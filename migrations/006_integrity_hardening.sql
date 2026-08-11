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

CREATE TRIGGER node_versions_status_check_insert
BEFORE INSERT ON node_versions
WHEN NEW.status NOT BETWEEN 1 AND 2
BEGIN
    SELECT RAISE(ABORT, 'invalid node version status');
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

CREATE TRIGGER audit_events_origin_check_insert
BEFORE INSERT ON audit_events
WHEN NEW.origin NOT BETWEEN 1 AND 3
BEGIN
    SELECT RAISE(ABORT, 'invalid audit origin');
END;

CREATE TRIGGER relation_version_endpoints_no_update
BEFORE UPDATE ON relation_version_endpoints
BEGIN
    SELECT RAISE(ABORT, 'relation version endpoints are append-only');
END;

CREATE TRIGGER relation_version_endpoints_no_delete
BEFORE DELETE ON relation_version_endpoints
BEGIN
    SELECT RAISE(ABORT, 'relation version endpoints are append-only');
END;

CREATE TRIGGER relation_references_no_update
BEFORE UPDATE ON relation_references
BEGIN
    SELECT RAISE(ABORT, 'relation references are append-only');
END;

CREATE TRIGGER relation_references_no_delete
BEFORE DELETE ON relation_references
BEGIN
    SELECT RAISE(ABORT, 'relation references are append-only');
END;

CREATE TRIGGER relation_evidence_no_update
BEFORE UPDATE ON relation_evidence
BEGIN
    SELECT RAISE(ABORT, 'relation evidence is append-only');
END;

CREATE TRIGGER relation_evidence_no_delete
BEFORE DELETE ON relation_evidence
BEGIN
    SELECT RAISE(ABORT, 'relation evidence is append-only');
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
        AND NOT EXISTS (
            SELECT 1 FROM relation_versions
            WHERE id = NEW.active_version_id AND proposal_kind = 1
        )
    )
    OR (
        NEW.status = 3
        AND NOT EXISTS (
            SELECT 1 FROM relation_versions
            WHERE id = NEW.active_version_id AND proposal_kind = 2
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
        AND NOT EXISTS (
            SELECT 1 FROM relation_versions
            WHERE id = NEW.active_version_id AND proposal_kind = 1
        )
    )
    OR (
        NEW.status = 3
        AND NOT EXISTS (
            SELECT 1 FROM relation_versions
            WHERE id = NEW.active_version_id AND proposal_kind = 2
        )
    )
BEGIN
    SELECT RAISE(ABORT, 'invalid relation current projection');
END;
