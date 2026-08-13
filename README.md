# dbgraph

dbgraph is a local, review-driven knowledge graph for database schemas and evidence-backed conditional data relationships. It imports MySQL catalog metadata, stores immutable relation revisions in SQLite, exposes the same application services through an English Web UI and MCP, and keeps approval under human control.

dbgraph deliberately does not clone repositories or analyze source code. An external LLM Agent uses its own code search, AST, and semantic tools, then submits structured proposals and evidence to dbgraph. dbgraph validates, deduplicates, versions, reviews, persists, and traverses those proposals.

## Build and run

Requirements:

- Go 1.26.5 or newer (the minimum patched toolchain required by the security gate)
- Linux (required so dbgraph can verify local filesystem semantics before enabling SQLite WAL)
- an embedded SQLite runtime accepted by dbgraph (3.51.3 or newer)
- OpenSSL, used by the Makefile to generate development tokens and the optional local certificate
- Python Playwright with Chromium when running the mandatory browser E2E suite

The Makefile drives local development. `make run` builds `./bin/dbgraph` and serves plain HTTP on `127.0.0.1:8080`:

```sh
make run
```

Open `http://127.0.0.1:8080/` and sign in with the `DBGRAPH_WEB_TOKEN` printed at startup. Health is at `/healthz` and the Streamable HTTP MCP endpoint at `/mcp`, where loopback callers get anonymous Viewer access. `make watch` does the same and rebuilds and restarts on every source change; a failed build leaves the running process untouched.

Web sign-in over cleartext is an explicit development opt-in, because the `__Host-` session cookie requires HTTPS and dbgraph otherwise refuses to start with `DBGRAPH_WEB_*` tokens and no TLS. `make run` passes `--insecure-cleartext-web`, which:

- is rejected unless the listener is loopback, and cannot be combined with TLS;
- issues the session cookie as `dbgraph-session` without the `__Host-` prefix and without `Secure`, so a browser keeps it over HTTP;
- keeps CSRF tokens, `HttpOnly`, `SameSite=Strict`, role authorization, and audit unchanged;
- prints a warning at startup.

The session cookie and every token cross the loopback interface unencrypted in that mode, and any local process can read them. Use TLS for anything another host can reach:

```sh
make run TLS=1
```

That generates `.dbgraph-local/cert.pem` and `.dbgraph-local/key.pem` on first use and serves HTTPS with the standard `__Host-`, `Secure` cookie. Open `https://127.0.0.1:8080/`, accept the locally generated certificate for this development instance, and sign in with the printed Web Admin token.

An unauthenticated or expired browser page request is answered with `303 See Other` to `/login`. That redirect is limited to `GET` and `HEAD` navigations outside `/api/` that accept HTML; every other unauthenticated request, including all `/api/v1` calls, still returns a `401 UNAUTHENTICATED` JSON body.

Development credentials are generated once into `.dbgraph-local/dev.env` and then stay fixed: the Makefile only writes that file when it is missing, and `make clean` does not touch it, so the tokens survive every rebuild, restart, and `make watch` cycle. `make tokens` prints them, `make rotate-tokens` replaces them on purpose, and `make rotate-certs` does the same for the certificate. Never reuse these credentials outside a development machine. The equivalent commands without the Makefile are:

```sh
go build -o ./bin/dbgraph ./cmd/dbgraph
export DBGRAPH_WEB_TOKEN="$(openssl rand -hex 32)"
./bin/dbgraph serve --database ./dbgraph.sqlite --listen 127.0.0.1:8080 \
  --insecure-cleartext-web
```

```sh
install -d -m 700 .dbgraph-local
openssl req -x509 -newkey rsa:3072 -sha256 -days 30 -nodes \
  -keyout .dbgraph-local/key.pem -out .dbgraph-local/cert.pem \
  -subj '/CN=localhost' -addext 'subjectAltName=DNS:localhost,IP:127.0.0.1'
chmod 600 .dbgraph-local/key.pem
export DBGRAPH_WEB_TOKEN="$(openssl rand -hex 32)"
export DBGRAPH_MCP_TOKEN="$(openssl rand -hex 32)"
./bin/dbgraph serve --database ./dbgraph.sqlite --listen 127.0.0.1:8080 \
  --tls-cert .dbgraph-local/cert.pem --tls-key .dbgraph-local/key.pem
```

The serving process is the only SQLite writer. `dbgraph mcp` is a stdio-to-HTTP transport proxy and never opens the database. `make mcp` runs it against the local server using the matching scheme, or run it directly:

```sh
  ./bin/dbgraph mcp --server-url http://127.0.0.1:8080
```

Against a TLS server, add `SSL_CERT_FILE=.dbgraph-local/cert.pem` and use the `https://` URL.

Create a consistent online backup at a new path:

```sh
./bin/dbgraph backup --database ./dbgraph.sqlite --output ./backups/dbgraph-2026-08-11.sqlite
```

The backup command refuses to overwrite an existing file. The database, lock, WAL, backup, and shared-memory artifacts are restricted to the current user. SQLite databases on known network filesystems are rejected because WAL requires local filesystem semantics; on non-Linux platforms dbgraph fails closed instead of guessing the backing filesystem type.

## Makefile targets

`make` with no target lists everything. The most used targets are `build`, `run`, `watch`, `mcp`, `tokens`, `test`, `test-race`, `vet`, `fmt`, `lint`, `verify`, `cover`, and `clean`. Every setting is an overridable variable:

| Variable | Default | Purpose |
|---|---|---|
| `TLS` | `0` | `0` serves cleartext HTTP with `--insecure-cleartext-web`; `1` serves HTTPS with the generated certificate |
| `LISTEN` | `127.0.0.1:8080` | Listen address |
| `DATABASE` | `./dbgraph.sqlite` | SQLite path |
| `LOCAL_DIR` | `.dbgraph-local` | Directory holding `dev.env`, `cert.pem`, and `key.pem` |
| `MYSQL_TLS` | `0` | `0` adds `--insecure-mysql-tls` so scans can reach a source MySQL without a certificate |
| `WATCH_INTERVAL` | `1` | Seconds between `make watch` change checks |
| `CERT_DAYS` | `30` | Validity of the generated development certificate |

For example, `make watch TLS=1 LISTEN=127.0.0.1:9090 DATABASE=/tmp/dbgraph.sqlite`.

## Configuration

Flags override environment variables.

| Variable | Purpose |
|---|---|
| `DBGRAPH_DATABASE_PATH` | SQLite path used by `serve` or the default backup source |
| `DBGRAPH_LISTEN_ADDRESS` | HTTP address; defaults to `127.0.0.1:8080` |
| `DBGRAPH_TLS_CERT_FILE` / `DBGRAPH_TLS_KEY_FILE` | TLS certificate and key; both are required together |
| `DBGRAPH_INSECURE_CLEARTEXT_WEB` | Development only: allow Web sign-in without TLS on a loopback listener |
| `DBGRAPH_INSECURE_MYSQL_TLS` | Development only: allow schema scans to reach MySQL over TCP without verified TLS |
| `DBGRAPH_SECRET_KEY` | 32 random bytes as 64 hex characters; seals stored DSNs. Required only to store a DSN with a data source |
| `DBGRAPH_BACKUP_PATH` | Default `backup --output` path |
| `DBGRAPH_MCP_SERVER_URL` | Server URL used by the stdio proxy |
| `DBGRAPH_MCP_TOKEN` | Bearer token used by the stdio proxy |
| `DBGRAPH_MCP_TOKEN` | The MCP credential; carries Admin |
| `DBGRAPH_WEB_TOKEN` | The Web credential; carries Admin |

Every configured access token must be 32 random bytes encoded as exactly 64 hexadecimal characters; generate one with `openssl rand -hex 32`. Tokens are seeded from the environment on startup and stored as SHA-256 digests in `access_credentials`; later starts authenticate from the database, so the variables only need to be present the first time or when rotating. The database holds no presentable credential, because the server verifies a token rather than reproducing one. Web credentials require TLS unless `--insecure-cleartext-web` is set, which is loopback-only and refuses to run alongside TLS. A non-loopback listener requires both `--tls-cert`/`--tls-key` and at least one `DBGRAPH_MCP_*_TOKEN`; anonymous MCP Viewer access is loopback-only. A data source resolves its DSN in one of two ways. Supply the connection string when creating it and dbgraph seals it with AES-256-GCM under `DBGRAPH_SECRET_KEY` and stores the ciphertext, so the database file and its backups hold no readable credential and the key never enters SQLite. Otherwise the source names an environment variable, such as `ORDERS_MYSQL_DSN`, that the serving process reads when a scan runs; that variable also serves as the fallback when no ciphertext is stored. A stored DSN that cannot be decrypted fails the scan rather than falling back, so a key problem is visible instead of silently connecting with a stale credential. The connection string is write-only: no Web, REST, or MCP response ever returns it. Use a read-only MySQL account with verified TLS; `--insecure-mysql-tls` waives the verified-TLS requirement for local development and, like the cleartext Web option, is refused unless the listener is loopback.

## Roles and review model

- Viewer: read catalog, relations, traversal, jobs, sessions, unresolved findings, audit history, and the data source list.
- Agent: Viewer access plus individual relation create/revision/tombstone proposals and relation-init sessions.
- Editor: Web relation create/revision/tombstone proposals.
- Reviewer: revision proposals plus approve/reject/suppress/restore. Reviewer edits create a new proposed revision; they do not overwrite approved content.
- Admin: evidence-repository, data-source, and full/incremental schema-scan administration; only Admin can list evidence repositories.

An approved revision remains effective while its replacement is pending. Rejection leaves the effective graph unchanged. Tombstone, suppression, restoration, and stale candidates are explicit reviewed state transitions. Historical relation versions, endpoints, references, evidence, events, scan facts, init batches, and audit events are append-only.

`GET /api/v1/data-sources` lists the service-wide source registry for every signed-in role. Only Admin responses include `dsnEnvironment` and timestamps, so deployment metadata never reaches a non-Admin role. Evidence repositories are listed through the Admin-only `GET /api/v1/repositories`. Catalog node search uses `GET /api/v1/nodes` and accepts an optional `dataSourceId` filter. The console routes and API payloads carry no separate workspace identifier.

## Agent-driven relation initialization

There is no dbgraph source-code parser and no generic SQL execution tool. An Agent initialization flow is:

1. Register an evidence repository and MySQL data source as Admin.
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
make verify
```

That runs the same gates directly:

```sh
gofmt -l -w cmd internal migrations tests
go test ./...
go test -race ./...
go vet ./...
staticcheck ./...
golangci-lint run
```

`make verify` skips `staticcheck` and `golangci-lint` with a printed notice when they are not installed; run them explicitly before a handoff.

The test suite includes domain unit tests, real file-backed SQLite integration tests, subprocess serve/MCP checks, and a real Chromium workflow covering login and data-source administration without exposing stored credentials. Overall coverage must remain at or above 80%.
