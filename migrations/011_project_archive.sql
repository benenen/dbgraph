-- Archive a project instead of deleting it.
--
-- Every project has audit events from its own creation, audit_events is
-- append-only by trigger, and those rows reference the project. A physical
-- delete would either orphan the audit trail or violate the append-only rule,
-- so a retired project is marked archived and hidden from the console while its
-- history stays exactly where it was.

ALTER TABLE projects ADD COLUMN archived_at TEXT;
