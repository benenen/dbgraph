# IDEA
1. 全部使用en
2. 数据存储使用sqlite
3. 需要有一个简单的web界面来展示数据
4. 使用golang实现代码

## 1. Overall Architecture

  Source Database
        │
        ▼
  Schema Scanner ───────────────┐
                                │
  Source Repository             ▼
   Java / MyBatis ──> Code Analyzer
                                │
  Agent MCP ───────> Proposals  │
                                ▼
                       Relation Reconciler
                                │
                                ▼
                             SQLite
                    ┌───────────┴───────────┐
                    ▼                       ▼
               Web UI + REST            MCP Server

  推荐运行模式：

  dbgraph serve

  由唯一进程负责：

  - SQLite writing
  - Web UI
  - REST API
  - Streamable HTTP MCP
  - Background scan jobs

  本地 Agent 如果只支持 stdio：

  dbgraph mcp --server-url http://127.0.0.1:8080

  这个命令只做 MCP transport proxy，不再启动第二个 SQLite writer。

  ## 2. Core Domain Model

  条件关系不能只是一条 edge，而应作为独立实体：

  (B.x:Column)
      └── SOURCE ──> (ConditionalRelation) ── TARGET ──> (A.x2:Column)
                              │
                              ├── GUARD ─────> A.x1 = 1
                              ├── SELECTOR ──> A.b_id = B.id
                              ├── TRANSFORM ─> identity(B.x)
                              └── EVIDENCE ──> Java/MyBatis code

  必须区分：

  - guard：关系何时生效，例如 A.x1 = 1
  - selector：选择 B 表哪一行，例如 A.b_id = B.id
  - transform：目标值如何生成，例如 A.x2 = B.x

  Condition 使用结构化 AST：

  {
    "kind": "compare",
    "operator": "eq",
    "left": {
      "kind": "column",
      "nodeId": 1001
    },
    "right": {
      "kind": "literal",
      "valueType": "integer",
      "value": 1
    }
  }

  第一版支持：

  and, or, not
  eq, ne, gt, gte, lt, lte
  in, not_in
  is_null, is_not_null
  column, literal, parameter
  column_copy, case

  条件求值采用三态：

  - TRUE：使用该关系
  - FALSE：排除该关系
  - UNKNOWN：保留为 conditional path，并返回缺失的 context

  ## 3. SQLite Design

  SQLite 使用“关系型事实表 + 图查询 projection”，不需要模拟 Neo4j。

  核心表：

  projects
  data_sources
  repositories
  scan_runs
  jobs

  nodes
  node_versions
  node_current

  relations
  relation_versions
  relation_version_endpoints
  relation_references
  relation_evidence
  relation_events
  relation_current

  effective_edges
  analysis_findings
  suppression_rules
  audit_events

  关键设计：

  - Primary key 使用 application-generated int64 Snowflake ID
  - 类型和状态在 SQLite 中使用 INTEGER
  - REST/MCP 对外返回 English enum，例如 COLUMN、PROPOSED
  - AST 使用 TEXT CHECK(json_valid(...))
  - relation_versions、relation_events、audit_events append-only
  - 正常接口不执行物理删除
  - effective_edges 是可重建的当前生效图，用于 trace/impact
  - relation_references 单独索引 guard/selector 引用过的字段
  - FTS5 搜索 database/table/column/symbol

  SQLite 配置：

  foreign_keys = ON
  journal_mode = WAL
  busy_timeout = 5000
  single writer connection
  bounded write queue
  short write transactions

  WAL 支持 reader/writer 并行，但仍然只有一个 writer，而且不能把数据库文件放在网络文件系统。SQLite WAL (https://sqlite.org/wal.html)

  还要在启动时检查嵌入的 SQLite runtime version。SQLite 官方记录的 WAL-reset 问题在 3.51.3 及部分 backport 中才修复，因此建议要求 >= 3.51.3。SQLite WAL-reset notice
  (https://sqlite.org/wal.html#walreset)

  Graph traversal：

  - 无条件 trace：SQLite recursive CTE
  - 带 context 的 trace：Go BFS + 批量读取 effective_edges
  - AST 在 Go 中求值，不在每一跳用 JSON SQL 解析
  - 限制 maxDepth、maxNodes、maxPaths
  - 必须进行 cycle detection

  SQLite 官方支持 recursive CTE (https://sqlite.org/lang_with.html)、JSON functions (https://sqlite.org/json1.html) 和 FTS5 (https://sqlite.org/fts5.html)。

  ## 4. Go Project Structure

  dbgraph/
  ├── cmd/dbgraph/
  │   └── main.go
  ├── internal/
  │   ├── catalog/
  │   ├── relations/
  │   ├── conditions/
  │   ├── graph/
  │   ├── ingestion/
  │   ├── analysis/
  │   │   ├── java/
  │   │   ├── mybatis/
  │   │   └── sql/
  │   ├── reconcile/
  │   ├── audit/
  │   ├── jobs/
  │   ├── platform/sqlite/
  │   └── transport/
  │       ├── httpapi/
  │       └── mcp/
  ├── web/
  │   ├── templates/
  │   └── static/
  ├── migrations/
  ├── testdata/
  └── go.mod

  技术选型：

  - HTTP：Go net/http
  - Templates：html/template
  - Static assets：go:embed
  - Graph UI：vendored Cytoscape.js
  - SQLite driver：modernc.org/sqlite，避免 CGO；Go 官方 driver 列表也收录了它。Go SQL drivers (https://go.dev/wiki/SQLDrivers)
  - MCP：official MCP Go SDK (https://github.com/modelcontextprotocol/go-sdk)，同时支持 stdio 和 Streamable HTTP
  - Java AST：official Tree-sitter Go bindings (https://github.com/tree-sitter/go-tree-sitter) + Java grammar (https://github.com/tree-sitter/tree-sitter-java)
  - MyBatis XML：Go encoding/xml
  - SQL parser：定义内部 SQLParser seam，MVP 先实现 MySQL dialect

  Web、REST、MCP 必须调用相同的 application modules，不能各自直接操作 SQLite。

  ## 5. Complete Processing Flow

  ### Schema Scan

  Connect with read-only credentials
  → Read database/schema/table/column metadata
  → Normalize qualified names
  → Import immutable snapshot
  → Generate declared FK relations
  → Mark removed objects STALE
  → Publish node_current

  ### Code Analysis

  Discover changed files
  → Parse Java/MyBatis/SQL
  → Extract assignments and conditions
  → Resolve Java property to database column
  → Build guard/selector/transform AST
  → Attach file/symbol/line/Git SHA evidence
  → Score confidence
  → Deduplicate by canonical fingerprint
  → Create PROPOSED relation

  例如：

  if (a.getX1() == 1) {
      a.setX2(b.getX());
  }

  生成：

  source    = B.x
  target    = A.x2
  guard     = EQ(A.x1, 1)
  transform = IDENTITY(B.x)
  status    = PROPOSED

  不能确定的反射、运行时 SQL、复杂跨方法调用进入：

  analysis_findings
  status = UNRESOLVED

  不能静默猜测。

  ### Relation Review

  PROPOSED
  ├── APPROVED
  └── REJECTED

  APPROVED
  ├── STALE
  ├── SUPPRESSED
  ├── TOMBSTONED
  └── SUPERSEDED

  修改关系时创建新 revision：

  relation@1 APPROVED
        ↓ superseded by
  relation@2 PROPOSED → APPROVED

  旧版本永久保留。所有写操作包含：

  actor
  reason
  requestId
  expectedRevisionNo
  timestamp

  ## 6. Web UI and MCP

  Web 页面全部使用 English：

  - Overview
  - Schema Explorer
  - Graph Explorer
  - Node Details
  - Relation Details
  - Relation Review
  - Unresolved Findings
  - Scan Jobs
  - Audit History
  - Settings

  Graph Explorer 提供：

  - Column/table search
  - Upstream/downstream trace
  - Conditional edge indicator
  - Relation type/status/confidence filters
  - Guard/selector/transform details
  - Code evidence
  - Approve/reject/revise/suppress operations

  MCP tools：

  dbgraph_status
  dbgraph_search_nodes
  dbgraph_get_node
  dbgraph_get_relation
  dbgraph_trace
  dbgraph_impact
  dbgraph_explain_relation
  dbgraph_list_proposals
  dbgraph_list_unresolved

  dbgraph_propose_relation
  dbgraph_revise_relation
  dbgraph_review_relation
  dbgraph_suppress_relation
  dbgraph_restore_relation

  dbgraph_start_schema_scan
  dbgraph_start_code_scan
  dbgraph_get_job

  权限原则：

  - Agent：read + propose
  - Reviewer：approve/reject/revise
  - Admin：source configuration + scans
  - 不提供 generic execute_sql
  - Source database credentials 只从 environment variables 读取
  - SQLite、日志和 MCP response 不保存或返回密码

  ## 7. Recommended Implementation Phases

  1. Foundation

     Go skeleton、configuration、Snowflake IDs、SQLite migrations、audit、jobs。先写 domain tests。

  2. Catalog

     MySQL schema scanner、完整 table/column inventory、search 和基础 Web 页面。

  3. Conditional Relations

     Guard AST、relation revisions、manual propose/review、evidence。

  4. Graph

     effective_edges、neighbors、trace、impact、三态条件求值。

  5. MCP

     先实现 read tools，再实现 propose；review tools 默认需要更高权限。

  6. MyBatis and SQL Analysis

     XML、dynamic tags、aliases、JOIN、INSERT、UPDATE、property-column mapping。

  7. Java Analysis

     if/else、switch、assignment、setter/getter、intra-method data flow。

  8. Web Review Workflow

     图展示、关系详情、条件编辑器、审批和审计历史。

  9. Hardening

     Incremental scans、backup、limits、auth、performance 和 security review。

  每阶段按 TDD 实施，最终要求：

  go test ./...
  go test -race ./...
  go vet ./...
  staticcheck ./...
  golangci-lint run

  覆盖 unit、SQLite integration 和浏览器 E2E，整体覆盖率至少 80%。

  第一版 MVP 应限定为：

  MySQL schema import
  + Java/MyBatis XML
  + intra-method conditional flow
  + SQLite
  + local English Web UI
  + MCP read/propose
  + manual review

  不承诺识别“所有关系”；准确的目标是：建立全部字段节点，创建可证明的高置信关系，并明确列出 unresolved relations。


## 数据库之间的关联关系(带条件的)
  (B.x:Column)
      └── SOURCE ──> (r:ConditionalRelation) ── TARGET ──> (A.x2:Column)
                             │
                             ├── GUARDED_BY ──> (A.x1 = 1)
                             ├── SELECTED_BY ─> (A.b_id = B.id)
                             └── EVIDENCED_BY ─> (Java方法 / MyBatis SQL)

  你这个例子实际上包含三种不同语义：

  1. guard：什么时候关系成立，例如 A.x1 = 1
  2. selector：对应 B 表哪条记录，例如 A.b_id = B.id
  3. transform：值如何进入目标字段，例如 A.x2 = B.x

  如果没有 selector，只能知道 A.x2 来自某个 B.x，但不知道来自 B 表的哪一行。

  建议存储成这样的结构：

  {
    "id": "relation-001",
    "type": "CONDITIONAL_VALUE_COPY",
    "source": "database.schema.B.x",
    "target": "database.schema.A.x2",

    "guard": {
      "op": "EQ",
      "left": {
        "type": "COLUMN",
        "value": "database.schema.A.x1"
      },
      "right": {
        "type": "LITERAL",
        "dataType": "INTEGER",
        "value": 1
      }
    },

    "selector": {
      "op": "EQ",
      "left": {
        "type": "COLUMN",
        "value": "database.schema.A.b_id"
      },
      "right": {
        "type": "COLUMN",
        "value": "database.schema.B.id"
      }
    },

    "transform": {
      "type": "IDENTITY",
      "expression": "B.x"
    },

    "evidence": {
      "type": "CODE",
      "repository": "example-service",
      "commit": "abc123",
      "file": "src/main/java/.../ExampleService.java",
      "symbol": "ExampleService.save",
      "startLine": 82,
      "endLine": 88
    },

    "confidence": 0.96,
    "status": "PROPOSED",
    "revision": 1
  }

  其中 guard 必须保存为 AST，而不是 "A.x1 = 1" 这样的文本。这样才能支持：

  AND
  ├── EQ(A.x1, 1)
  └── IN(A.status, ["ACTIVE", "READY"])

  Agent 查询时可以传入上下文：

  {
    "tool": "trace_field",
    "arguments": {
      "from": "B.x",
      "to": "A.x2",
      "context": {
        "A.x1": 1
      }
    }
  }

  判断结果采用三态：

  - TRUE：条件成立，关系参与 trace
  - FALSE：条件不成立
  - UNKNOWN：上下文不足，返回“可能成立及所需条件”，不能直接忽略

  关系修改建议采用不可变版本，而不是直接覆盖：

  relation-001@1  APPROVED
         │
         └── superseded_by
                   ↓
  relation-001@2  PROPOSED → APPROVED

  对应 MCP 工具可以设计为：

  - propose_relation
  - revise_relation
  - review_relation
  - suppress_relation
  - restore_relation
  - explain_relation
  - trace_fields
  - impact_analysis

  普通 Agent 默认只能 propose，审核通过后才进入正式图；删除使用 TOMBSTONED，保留历史证据。

  开源实现上，我建议：

  - 用 OpenMetadata 采集数据库、表、字段和普通列级血缘；它支持人工编辑列血缘以及 REST 新增/删除 lineage。列级血缘文档
    (https://docs.open-metadata.org/v1.13.x/how-to-guides/data-lineage/column)、Lineage API (https://docs.open-metadata.org/v1.13.x/api-reference/lineage/add)

  - 单独建立 ConditionalRelation Store，保存 guard AST、代码证据、置信度和版本。
  - 自定义 MCP 聚合 OpenMetadata 与条件关系层。
  - Java/MyBatis Analyzer 从 Java 控制流、Mapper XML、动态 SQL 中产生 PROPOSED 关系。

  OpenMetadata 原生 lineage 模型没有可查询的结构化 guard，只能放 SQL、function 或描述，因此不能只依赖它现有的边模型。官方 Lineage Schema
  (https://raw.githubusercontent.com/open-metadata/OpenMetadata/main/openmetadata-spec/src/main/resources/json/schema/type/entityLineage.json)

  完整开源调研结果在/tmp/mutable-dbgraph-oss-research.md。核心结论是：OpenMetadata 最适合做七成底座，但“条件关系节点 + Java/MyBatis 分析器 + 可写 MCP”需要单独开发。