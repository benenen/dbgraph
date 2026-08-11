CREATE TABLE relation_init_reproposal_candidates (
    session_id INTEGER NOT NULL REFERENCES relation_init_sessions(id),
    batch_id INTEGER NOT NULL REFERENCES relation_init_batches(id),
    relation_id INTEGER NOT NULL REFERENCES relations(id),
    candidate_json TEXT NOT NULL CHECK (json_valid(candidate_json) AND length(candidate_json) <= 1000000),
    created_at TEXT NOT NULL,
    PRIMARY KEY (session_id, relation_id),
    UNIQUE (batch_id, relation_id)
) STRICT;

CREATE INDEX relation_init_reproposal_candidates_batch_idx
ON relation_init_reproposal_candidates(batch_id, relation_id);

CREATE TRIGGER relation_init_reproposal_candidates_no_update
BEFORE UPDATE ON relation_init_reproposal_candidates
BEGIN
    SELECT RAISE(ABORT, 'relation init reproposal candidates are append-only');
END;

CREATE TRIGGER relation_init_reproposal_candidates_no_delete
BEFORE DELETE ON relation_init_reproposal_candidates
BEGIN
    SELECT RAISE(ABORT, 'relation init reproposal candidates are append-only');
END;
