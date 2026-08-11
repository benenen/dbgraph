# dbgraph Repository Instructions

## Project Status

- This repository is currently in the design phase. `IDEA.md` is the product and architecture source of truth until implementation documents replace individual sections.
- The intended implementation language is Go. Persistent storage is SQLite. The product includes a local English Web UI, REST endpoints, and an MCP server.
- Keep source code, identifiers, configuration keys, user-facing UI text, logs, and new project documentation in English.
- Do not claim build or test success while `go.mod` and the relevant implementation do not exist.

## Product Boundary

dbgraph owns:

- read-only source-database schema discovery;
- database, schema, table, and column catalog storage;
- conditional relation contracts and validation;
- proposal deduplication, immutable revisions, review, audit, and publication;
- SQLite persistence and graph projections;
- trace, impact, search, Web, REST, and MCP interfaces.

dbgraph does not:

- clone, read, parse, or semantically analyze application source repositories;
- embed Java, MyBatis, SQL, Tree-sitter, or other source-code analyzers;
- treat repository metadata as permission to access a repository;
- accept evidence as proof that dbgraph inspected the referenced file.

An external LLM Agent analyzes source code using its own tools. It reads the dbgraph catalog and submits evidence-backed relation proposals through MCP. dbgraph validates the proposal contract, not the truth of the source-code interpretation.

## Domain Invariants

- Model a conditional relationship as a first-class entity, not as an edge with an unstructured label.
- Keep `guard`, `selector`, and `transform` distinct:
  - `guard`: when the relation applies;
  - `selector`: which source row is selected;
  - `transform`: how the source value becomes the target value.
- Store relation conditions as a validated JSON AST. This AST is protocol data and is not a source-code parse tree.
- Evaluate conditions with three-valued logic: `TRUE`, `FALSE`, and `UNKNOWN`. Preserve an unknown path and report missing context.
- Never update an approved relation in place. Create a new `PROPOSED` revision.
- Keep the current approved revision active until a replacement is approved. Rejected proposals must not change the effective graph.
- Use `expectedRevisionNo` for optimistic concurrency. Return a conflict with the current revision instead of using last-write-wins.
- Use `TOMBSTONED` revisions instead of physical deletion. Preserve history and evidence.
- Completing a relation-init session may only create `PROPOSED` stale or tombstone candidates. Completion itself must never change an approved revision or the effective graph.
- Require Reviewer approval before any relation-init candidate can mark an existing relation stale, tombstoned, or superseded.
- Incomplete relation-init sessions must not create invalidation candidates or change existing relations.
- Rebuild affected `effective_edges` atomically only after an approved state transition.
- Record every state-changing action with actor, origin, reason, request ID, expected revision, and timestamp.

## Module Seams

- Web, REST, and MCP are adapters. They must call the same application modules and must not write SQLite directly.
- Keep the relation write interface small and shared:
  - `ProposeCreate`
  - `ProposeRevision`
  - `ProposeTombstone`
  - `Review`
  - `Suppress`
  - `Restore`
- Put validation, authorization inputs, version checks, audit creation, reconciliation, and publication behind that interface.
- Keep catalog, relations, conditions, graph traversal, ingestion, reconciliation, audit, jobs, SQLite, and transport concerns in focused packages.
- Introduce an adapter seam only when at least two implementations or a real variability point exist.

## Agent and Web Relation Updates

- Agents may initialize relations in bounded, retryable batches and may later propose individual creates, revisions, or tombstones during ordinary use.
- Web Editors may perform the same proposal operations from Graph Explorer and Relation Details.
- Web and Agent proposals use the same validation, revision state machine, review queue, audit records, and publication path.
- Reviewer actions approve, reject, suppress, or restore. Editing content creates another proposed revision.
- Require a reason and structured evidence for changes. Store repository, commit, file, symbol, and line metadata when available.
- Do not silently invent relationships. Record uncertain cases as unresolved findings.

## SQLite Rules

- Use one application process as the only SQLite writer.
- `dbgraph serve` owns SQLite writes, Web, REST, Streamable HTTP MCP, and background schema-scan jobs.
- `dbgraph mcp --server-url ...` is a transport proxy and must never create another writer.
- Configure `foreign_keys=ON`, WAL mode, a bounded write queue, a busy timeout, and short write transactions.
- Do not place the WAL database on a network filesystem.
- Check the embedded SQLite runtime at startup and enforce the minimum version selected in `IDEA.md`.
- Use parameterized queries only.
- Keep revision, relation-event, and audit-event records append-only.
- Use recursive CTEs for unconditional traversal and bounded Go BFS for contextual traversal.
- Enforce maximum depth, nodes, paths, and cycle detection.

## Security

- Treat database metadata, MCP payloads, Web forms, relation ASTs, and Agent evidence as untrusted input.
- Validate node IDs, enums, relation types, required fields, AST shape, AST depth, AST node count, batch size, string length, and evidence count.
- Read source-database credentials only from environment variables. Never store or return credentials in SQLite, logs, Web responses, REST responses, or MCP responses.
- Do not expose a generic `execute_sql` tool.
- Enforce authentication and role authorization server-side for every state-changing Web or MCP operation.
- Web sessions must use secure cookie settings and CSRF protection. Escape all rendered content and do not render user-provided HTML.
- Rate-limit state-changing and expensive endpoints. Return safe client errors without internal stacks or secrets.
- Review authorization, input validation, SQL injection, XSS, CSRF, sensitive logging, and dependency vulnerabilities before a commit containing security-sensitive changes.

## Testing and Verification

- Use TDD for implementation changes: add a failing test, implement the smallest correct change, then refactor.
- Test through module interfaces. Do not bypass an interface to assert internal SQLite details unless the test is specifically an adapter integration test.
- Cover domain logic with unit tests, SQLite behavior with integration tests, and critical Web flows with browser E2E tests.
- Maintain at least 80% overall coverage once executable code exists.
- Include negative tests for invalid ASTs, authorization, CSRF, stale revisions, partial init sessions, traversal limits, and concurrent updates.
- When the Go project exists, run the applicable gates before handoff:

```sh
gofmt -w <changed-go-files>
go test ./...
go test -race ./...
go vet ./...
staticcheck ./...
golangci-lint run
```

- If a tool is unavailable or a command cannot run because the project is not initialized, report that limitation explicitly.

## Change Discipline

- Plan complex features before editing and keep changes narrow and reversible.
- Preserve unrelated working-tree changes. Stage explicit files or hunks; do not use `git add -A`.
- Do not add a source-code analyzer to dbgraph as a convenience. Keep analysis in the external Agent workflow.
- Do not weaken immutable revision, review, audit, or single-writer guarantees to simplify an adapter.
- Update `IDEA.md` when a product or architecture decision changes. Avoid duplicating the same decision in additional top-level documents.
- Use Conventional Commit messages: `<type>: <description>`.
- Do not commit, push, deploy, or modify external systems unless the user explicitly requests it.
