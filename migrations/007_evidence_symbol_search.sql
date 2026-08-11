CREATE VIRTUAL TABLE relation_evidence_search USING fts5(
    version_id UNINDEXED,
    node_id UNINDEXED,
    project_id UNINDEXED,
    repository_name,
    file_path,
    symbol,
    constraint_name,
    tokenize = 'unicode61'
);

INSERT INTO relation_evidence_search(
    version_id, node_id, project_id, repository_name, file_path, symbol, constraint_name
)
SELECT
    evidence.version_id,
    endpoint.node_id,
    relation.project_id,
    evidence.repository_name,
    evidence.file_path,
    evidence.symbol,
    evidence.constraint_name
FROM relation_evidence evidence
JOIN relation_versions version ON version.id = evidence.version_id
JOIN relations relation ON relation.id = version.relation_id
JOIN relation_version_endpoints endpoint ON endpoint.version_id = version.id;

CREATE TRIGGER relation_evidence_search_insert
AFTER INSERT ON relation_evidence
BEGIN
    INSERT INTO relation_evidence_search(
        version_id, node_id, project_id, repository_name, file_path, symbol, constraint_name
    )
    SELECT
        NEW.version_id,
        endpoint.node_id,
        relation.project_id,
        NEW.repository_name,
        NEW.file_path,
        NEW.symbol,
        NEW.constraint_name
    FROM relation_versions version
    JOIN relations relation ON relation.id = version.relation_id
    JOIN relation_version_endpoints endpoint ON endpoint.version_id = version.id
    WHERE version.id = NEW.version_id;
END;
