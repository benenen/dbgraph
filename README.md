# dbgraph

dbgraph is a local, review-driven knowledge graph for database schemas and evidence-backed conditional data relationships. It imports MySQL catalog metadata, stores immutable relation revisions in SQLite, exposes the same application services through an English Web UI and MCP, and keeps approval under human control.

dbgraph deliberately does not clone repositories or analyze source code. An external LLM Agent uses its own code search, AST, and semantic tools, then submits structured proposals and evidence to dbgraph. dbgraph validates, deduplicates, versions, reviews, persists, and traverses those proposals.

## Build and run

Requirements:

- Go 1.26.5 or newer (the minimum patched toolchain required by the security gate)
- Linux (required so dbgraph can verify local filesystem semantics before enabling SQLite WAL)
- an embedded SQLite runtime accepted by dbgraph (3.51.3 or newer)
- OpenSSL for the local TLS quick-start
- Python Playwright with Chromium when running the mandatory browser E2E suite

```sh
go build -o ./bin/dbgraph ./cmd/dbgraph
install -d -m 700 .dbgraph-local
openssl req -x509 -newkey rsa:3072 -sha256 -days 30 -nodes \
  -keyout .dbgraph-local/key.pem -out .dbgraph-local/cert.pem \
  -subj '/CN=localhost' -addext 'subjectAltName=DNS:localhost,IP:127.0.0.1'
chmod 600 .dbgraph-local/key.pem
export DBGRAPH_WEB_ADMIN_TOKEN="$(openssl rand -hex 32)"
export DBGRAPH_MCP_AGENT_TOKEN="$(openssl rand -hex 32)"
printf 'Web Admin token: %s\nMCP Agent token: %s\n' \
  "$DBGRAPH_WEB_ADMIN_TOKEN" "$DBGRAPH_MCP_AGENT_TOKEN"
./bin/dbgraph serve --database ./dbgraph.sqlite --listen 127.0.0.1:8080 \
  --tls-cert .dbgraph-local/cert.pem --tls-key .dbgraph-local/key.pem
```

Open `https://127.0.0.1:8080/`, accept the locally generated certificate for this development instance, and sign in with the printed Web Admin token. Health is available at `/healthz`, and the Streamable HTTP MCP endpoint is `/mcp`.

The serving process is the only SQLite writer. `dbgraph mcp` is a stdio-to-HTTP transport proxy and never opens the database:

```sh
DBGRAPH_MCP_TOKEN="$DBGRAPH_MCP_AGENT_TOKEN" \
SSL_CERT_FILE=.dbgraph-local/cert.pem \
  ./bin/dbgraph mcp --server-url https://127.0.0.1:8080
```

Create a consistent online backup at a new path:

```sh
./bin/dbgraph backup --database ./dbgraph.sqlite --output ./backups/dbgraph-2026-08-11.sqlite
```

The backup command refuses to overwrite an existing file. The database, lock, WAL, backup, and shared-memory artifacts are restricted to the current user. SQLite databases on known network filesystems are rejected because WAL requires local filesystem semantics; on non-Linux platforms dbgraph fails closed instead of guessing the backing filesystem type.

## Configuration

Flags override environment variables.

| Variable | Purpose |
|---|---|
| `DBGRAPH_DATABASE_PATH` | SQLite path used by `serve` or the default backup source |
| `DBGRAPH_LISTEN_ADDRESS` | HTTP address; defaults to `127.0.0.1:8080` |
| `DBGRAPH_TLS_CERT_FILE` / `DBGRAPH_TLS_KEY_FILE` | TLS certificate and key; both are required together |
| `DBGRAPH_BACKUP_PATH` | Default `backup --output` path |
| `DBGRAPH_MCP_SERVER_URL` | Server URL used by the stdio proxy |
| `DBGRAPH_MCP_TOKEN` | Bearer token used by the stdio proxy |
| `DBGRAPH_MCP_AGENT_TOKEN` | Agent MCP credential |
| `DBGRAPH_MCP_REVIEWER_TOKEN` | Reviewer MCP credential |
| `DBGRAPH_MCP_ADMIN_TOKEN` | Admin MCP credential |
| `DBGRAPH_WEB_VIEWER_TOKEN` | Read-only Web credential |
| `DBGRAPH_WEB_EDITOR_TOKEN` | Web create/revise/tombstone credential |
| `DBGRAPH_WEB_REVIEWER_TOKEN` | Web review/revision/suppress/restore credential |
| `DBGRAPH_WEB_ADMIN_TOKEN` | Web data-source and schema-scan credential |

Every configured access token must be 32 random bytes encoded as exactly 64 hexadecimal characters; generate one with `openssl rand -hex 32`. Web credentials require TLS. A non-loopback listener requires both `--tls-cert`/`--tls-key` and at least one `DBGRAPH_MCP_*_TOKEN`; anonymous MCP Viewer access is loopback-only. MySQL DSNs are never stored in SQLite: a data source stores only an environment-variable name, such as `ORDERS_MYSQL_DSN`, and the serving process reads that variable when a scan runs. Use a read-only MySQL account with verified TLS.

## Roles and review model

- Viewer: read catalog, relations, traversal, jobs, sessions, unresolved findings, and audit history.
- Agent: Viewer access plus individual relation create/revision/tombstone proposals and relation-init sessions.
- Editor: Web relation create/revision/tombstone proposals.
- Reviewer: revision proposals plus approve/reject/suppress/restore. Reviewer edits create a new proposed revision; they do not overwrite approved content.
- Admin: project, evidence-repository, data-source, and full/incremental schema-scan administration.

An approved revision remains effective while its replacement is pending. Rejection leaves the effective graph unchanged. Tombstone, suppression, restoration, and stale candidates are explicit reviewed state transitions. Historical relation versions, endpoints, references, evidence, events, scan facts, init batches, and audit events are append-only.

## Agent-driven relation initialization

There is no dbgraph source-code parser and no generic SQL execution tool. An Agent initialization flow is:

1. Register a project, evidence repository, and MySQL data source as Admin.
2. Run a schema scan and use `dbgraph_search_nodes` / `dbgraph_get_node` to resolve catalog columns.
3. Call `dbgraph_begin_relation_init` with `FULL`, or `INCREMENTAL` and `scope.relationIds`.
4. Analyze the source repository outside dbgraph.
5. Submit bounded, retryable batches with `dbgraph_propose_relations`. Reuse the same idempotency key only for an identical payload.
6. Report uncertain dynamic behavior through `unresolved` instead of guessing.
7. Call `dbgraph_complete_relation_init`. Completion creates only reviewable proposals; it never changes the effective graph.
8. A Reviewer approves or rejects each candidate.

The canonical literal AST shape follows `IDEA.md`:

```json
{
  "kind": "compare",
  "operator": "eq",
  "left": { "kind": "column", "nodeId": "1001" },
  "right": { "kind": "literal", "valueType": "integer", "value": 1 }
}
```

The first version supports `and`, `or`, `not`, all six ordered comparisons, `in`, `not_in`, null checks, column/literal/parameter values, `column_copy`, and recursive `case` transforms. Traversal evaluates guard, selector, and transform with TRUE/FALSE/UNKNOWN results and reports missing context for UNKNOWN paths.

The MCP server exposes exactly these tools:

```text
dbgraph_status                       dbgraph_search_nodes
dbgraph_get_node                     dbgraph_get_relation
dbgraph_trace                        dbgraph_impact
dbgraph_explain_relation             dbgraph_list_proposals
dbgraph_list_unresolved              dbgraph_propose_relation
dbgraph_begin_relation_init          dbgraph_propose_relations
dbgraph_complete_relation_init       dbgraph_get_relation_init
dbgraph_propose_relation_revision    dbgraph_propose_relation_tombstone
dbgraph_review_relation              dbgraph_suppress_relation
dbgraph_restore_relation             dbgraph_start_schema_scan
dbgraph_get_job
```

## Schema scans

Full scans import the database/schema/table/column inventory, publish immutable node versions, reconcile declared foreign keys, and mark removed objects stale. Incremental scans accept one to 100 explicit `schema.table` entries, query only those source tables plus referenced target tables, and mark omissions only inside the requested source scope. Failed connection/query/publication attempts remain visible as failed `schema_scan_runs` and jobs.

## Traversal

Unconditional trace and impact use a bounded SQLite recursive CTE. Requests with column or parameter context use bounded Go BFS so relation ASTs are parsed once and evaluated in Go. Both paths enforce depth, node, path, frontier, edge-expansion, raw-AST, result-step, result-byte, cancellation, and cycle limits.

## Verification

```sh
go test ./...
go test -race ./...
go vet ./...
staticcheck ./...
golangci-lint run
```

The test suite includes domain unit tests, real file-backed SQLite integration tests, subprocess serve/MCP checks, and a real Chromium workflow covering login, structured relation editing, evidence, review, traversal, and Reviewer state changes. Project coverage must remain at or above 80%.
