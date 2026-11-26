# db-mcp — Technical Specification (v0.1)

## 1. Overview
`db-mcp` is a single-binary, zero-runtime-dependency Model Context Protocol (MCP) server written in Go. It connects AI coding assistants (Cursor, VS Code, Claude Desktop, Antigravity, etc.) to PostgreSQL databases safely over `stdio`, enabling schema discovery, read-only querying, and execution-plan inspection without exposing the database to destructive operations.

### Primary Goals
- **Zero-dependency distribution:** Single static binary (`~10–15 MB`) with instant startup.
- **Safety by default:** Read-only enforcement at both the application layer and PostgreSQL transaction layer.
- **AI-friendly outputs:** Compact, structured JSON responses with strict row limits and automatic sensitive-column redaction so the LLM context window is never flooded or exposed to secrets.
- **Extensible storage layer:** Clean adapter interface so MySQL and SQLite can be added in v0.2+ without touching the MCP or guard layers.

---

## 2. Scope & Roadmap

### In Scope (v0.1)
- PostgreSQL adapter (`internal/db/postgres`).
- MCP `stdio` transport via `github.com/modelcontextprotocol/go-sdk`.
- Defense-in-depth safety layer (`internal/guard`).
- 4 core MCP tools: `list_tables`, `describe_table`, `run_query`, `explain_query`.
- Configuration via environment variables with CLI flag overrides.
- Unit tests (`guard`, `mcp`) and PostgreSQL integration tests (`postgres`).

### Out of Scope (v0.2+)
- MySQL / MariaDB / SQLite adapters.
- Streamable HTTP / SSE transport for remote deployments.
- `pg_stat_statements` slow-query aggregation tool.
- Write/DDL operations or migration generation.

---

## 3. System Architecture

```text
AI Host / Client (JSON-RPC 2.0 over stdio)
        │
        ▼
┌──────────────────────────────────────────────────────────┐
│ internal/mcp                                             │
│ - Tool registration & JSON schema definitions            │
│ - Input validation & MCP IsError formatting              │
└───────────────────────────┬──────────────────────────────┘
                            │
                            ▼
┌──────────────────────────────────────────────────────────┐
│ internal/guard (Pure Go, zero DB/MCP dependencies)       │
│ - SQL pre-flight validation (read-only allowlist)        │
│ - Row limit clamping & truncation detection              │
│ - Sensitive column redaction ([REDACTED])                │
└───────────────────────────┬──────────────────────────────┘
                            │
                            ▼
┌──────────────────────────────────────────────────────────┐
│ internal/db (Repository interface & domain models)       │
└───────────────────────────┬──────────────────────────────┘
                            │
                            ▼
┌──────────────────────────────────────────────────────────┐
│ internal/db/postgres                                     │
│ - Connection pool (pgx/v5)                               │
│ - Explicit ReadOnly transactions + defer tx.Rollback()   │
│ - Per-query context.WithTimeout                          │
└──────────────────────────────────────────────────────────┘
```

---

## 4. Directory Structure

```text
db-mcp/
├── cmd/
│   └── db-mcp/
│       └── main.go              # Entrypoint, config loading, DI wiring, signal handling
├── internal/
│   ├── config/
│   │   ├── config.go            # Env vars & CLI flags parsing + validation
│   │   └── config_test.go
│   ├── db/
│   │   ├── repository.go        # Repository interface & domain structs
│   │   └── postgres/
│   │       ├── postgres.go      # PostgreSQL implementation
│   │       └── postgres_test.go # Integration tests (gated by TEST_DATABASE_URL)
│   ├── guard/
│   │   ├── guard.go             # SQL validator, row capper, column redactor
│   │   └── guard_test.go        # Table-driven unit tests
│   └── mcp/
│       ├── server.go            # MCP server setup & tool registration
│       ├── tools.go             # Tool handlers & input/output DTOs
│       └── tools_test.go        # Unit tests with mock db.Repository
├── Makefile
├── SPEC.md
├── AGENTS.md
└── go.mod
```

---

## 5. Core Interfaces & Domain Models (`internal/db/repository.go`)

```go
type Repository interface {
    Ping(ctx context.Context) error
    ListTables(ctx context.Context, schema string) ([]TableSummary, error)
    DescribeTable(ctx context.Context, schema, table string) (*TableSchema, error)
    ExecuteReadOnlyQuery(ctx context.Context, query string, maxRows int) (*QueryResult, error)
    ExplainQuery(ctx context.Context, query string, analyze bool) (*ExplainResult, error)
    Close() error
}
```

### Domain Structs
- `TableSummary`: `Name`, `Type` (`BASE TABLE` / `VIEW`), `EstimatedRows` (`int64`).
- `TableSchema`: `Schema`, `Table`, `Columns []ColumnInfo`, `PrimaryKeys []string`, `ForeignKeys []ForeignKeyInfo`, `Indexes []IndexInfo`.
- `ColumnInfo`: `Name`, `DataType`, `Nullable` (`bool`), `Default` (`*string`).
- `ForeignKeyInfo`: `Column`, `RefTable`, `RefColumn`, `ConstraintName`.
- `IndexInfo`: `Name`, `Columns []string`, `Unique` (`bool`), `Definition`.
- `QueryResult`: `Columns []string`, `Rows [][]any`, `RowCount` (`int`), `Truncated` (`bool`), `ExecutionTimeMs` (`int64`).
- `ExplainResult`: `Plan` (`json.RawMessage`), `ExecutionTimeMs` (`int64`).

---

## 6. Configuration

Loaded from environment variables with optional CLI flag overrides (flags take precedence):

| Env Variable | CLI Flag | Required | Default | Hard Limit | Description |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `DATABASE_URL` | `--database-url` | Yes | — | — | PostgreSQL connection URI (`postgres://...`). |
| `DB_QUERY_TIMEOUT` | `--query-timeout` | No | `5s` | `30s` | Per-query timeout applied via `context.WithTimeout`. |
| `DB_MAX_ROWS` | `--max-rows` | No | `100` | `1000` | Default max rows returned by `run_query`. |
| `DB_REDACT_COLUMNS` | `--redact-columns` | No | `password,password_hash,secret,token,api_key,access_key,ssn` | — | Comma-separated case-insensitive substrings to redact. |
| `LOG_LEVEL` | `--log-level` | No | `info` | — | `debug`, `info`, `warn`, `error` (strictly to `stderr`). |

Startup fails fast with a non-zero exit code and a clear `stderr` log if `DATABASE_URL` is missing, malformed, or unreachable on initial `Ping`.

---

## 7. Security & Safety Model

1. **Database-Level Read-Only (Primary Defense):**
   - Every query (`run_query`, `explain_query`) executes inside `BeginTx(ctx, &sql.TxOptions{ReadOnly: true})`.
   - Every transaction calls `defer tx.Rollback()` unconditionally.
2. **Pre-Flight SQL Guard (`internal/guard`):**
   - Strip leading/trailing whitespace and SQL comments (`-- ...`, `/* ... */`) before inspection.
   - Reject empty strings.
   - Reject multi-statement queries (any `;` outside single-quoted string literals, except a single optional trailing `;`).
   - Require the first keyword to be `SELECT`, `WITH`, or `EXPLAIN` (for `run_query`, only `SELECT` or `WITH`).
   - Reject mutating keywords anywhere as standalone tokens outside string literals: `INSERT`, `UPDATE`, `DELETE`, `DROP`, `ALTER`, `TRUNCATE`, `GRANT`, `REVOKE`, `CREATE`, `COPY`, `CALL`, `DO`, `EXECUTE`, `SET`, `LOCK`.
3. **Resource Bounds:**
   - All DB calls run with `ctx, cancel := context.WithTimeout(parentCtx, cfg.QueryTimeout)`.
   - `ExecuteReadOnlyQuery` scans at most `limit + 1` rows. If row `limit + 1` exists, `Truncated = true` and that extra row is dropped.
4. **Sensitive Column Redaction:**
   - Before serializing `QueryResult`, every column name is lowercased and checked against `DB_REDACT_COLUMNS`. Matching column values are replaced with `"[REDACTED]"`.
5. **Protocol Hygiene (`stdio`):**
   - `stdout` is strictly reserved for JSON-RPC 2.0 MCP frames.
   - All logs go to `os.Stderr` via `log/slog`. Never log `DATABASE_URL`.

---

## 8. MCP Tools Contract (v0.1)

### 8.1 `list_tables`
- **Purpose:** Discover tables and views in a schema along with approximate row counts (from `pg_class.reltuples`).
- **Input:**
  ```json
  { "schema": "public" }
  ```
  - `schema` (string, optional, default `"public"`).
- **Output:**
  ```json
  {
    "schema": "public",
    "tables": [
      { "name": "users", "type": "BASE TABLE", "estimated_rows": 1420 }
    ]
  }
  ```

### 8.2 `describe_table`
- **Purpose:** Inspect full metadata for a single table so the AI can write accurate queries and diagnose missing indexes.
- **Input:**
  ```json
  { "table": "users", "schema": "public" }
  ```
  - `table` (string, required).
  - `schema` (string, optional, default `"public"`).
- **Output:**
  ```json
  {
    "schema": "public",
    "table": "users",
    "columns": [
      { "name": "id", "data_type": "bigint", "nullable": false, "default": "nextval('users_id_seq'::regclass)" }
    ],
    "primary_keys": ["id"],
    "foreign_keys": [],
    "indexes": [
      { "name": "users_pkey", "columns": ["id"], "unique": true, "definition": "CREATE UNIQUE INDEX ..." }
    ]
  }
  ```
- **Error Handling:** Returns MCP tool error (`IsError: true`) if the table does not exist.

### 8.3 `run_query`
- **Purpose:** Execute a read-only SQL query and return bounded, redacted tabular results.
- **Input:**
  ```json
  { "sql": "SELECT id, email, password_hash FROM users LIMIT 10", "limit": 100 }
  ```
  - `sql` (string, required).
  - `limit` (integer, optional; defaults to `DB_MAX_ROWS`, clamped to `[1, DB_MAX_ROWS]`).
- **Output:**
  ```json
  {
    "columns": ["id", "email", "password_hash"],
    "rows": [[1, "alice@example.com", "[REDACTED]"]],
    "row_count": 1,
    "truncated": false,
    "execution_time_ms": 4
  }
  ```
- **Error Handling:** Returns MCP tool error (`IsError: true`) on guard rejection, SQL syntax error, or query timeout.

### 8.4 `explain_query`
- **Purpose:** Inspect PostgreSQL's execution plan for slow-query diagnosis.
- **Input:**
  ```json
  { "sql": "SELECT * FROM orders WHERE user_id = 42", "analyze": false }
  ```
  - `sql` (string, required; must pass `guard` as a valid `SELECT`/`WITH` query without a leading `EXPLAIN`).
  - `analyze` (boolean, optional, default `false`). When `true`, executes `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)` inside a rolled-back `ReadOnly` transaction.
- **Output:**
  ```json
  {
    "plan": [ { "Plan": { "Node Type": "Seq Scan", "Relation Name": "orders" } } ],
    "execution_time_ms": 12
  }
  ```

---

## 9. Testing Strategy & Acceptance Criteria

- **`internal/guard` (Unit Tests):**
  - Valid queries: simple `SELECT`, `WITH` CTE, string literals containing blocked words (`SELECT 'DROP TABLE' AS msg`), trailing semicolon.
  - Blocked queries: `INSERT`, `UPDATE`, `DELETE`, `DROP`, `ALTER`, `TRUNCATE`, multi-statement (`SELECT 1; DROP TABLE users`), mutating CTE (`WITH d AS (DELETE FROM users RETURNING *) SELECT * FROM d`), comment-obfuscated keywords (`/* comment */ DELETE FROM users`).
  - Redaction: verifies matching columns become `"[REDACTED]"` while non-matching columns remain untouched.
- **`internal/db/postgres` (Integration Tests):**
  - Skipped cleanly when `TEST_DATABASE_URL` is unset.
  - Proves `ExecuteReadOnlyQuery` rejects write attempts at the PostgreSQL level even if called directly without `guard`.
  - Verifies `list_tables`, `describe_table`, `run_query` truncation, and `explain_query`.
- **`internal/mcp` (Unit Tests):**
  - Uses a mock `db.Repository` to verify tool argument parsing, guard integration, and `IsError: true` formatting.

---

## 10. Implementation Milestones

- **M1 — Skeleton & Transport:** Initialize `go.mod`, `Makefile`, `internal/config`, `slog` (`stderr`), and a minimal MCP server with a temporary `ping` tool verified via `@modelcontextprotocol/inspector`.
- **M2 — Guard & PostgreSQL Adapter:** Implement `internal/guard` with full unit tests and `internal/db/postgres` with `ReadOnly` transaction enforcement + integration tests.
- **M3 — Schema Discovery Tools:** Wire `list_tables` and `describe_table` end-to-end and verify with MCP Inspector against a local PostgreSQL database.
- **M4 — Query & Explain Tools:** Wire `run_query` (row cap + redaction) and `explain_query`, add `internal/mcp` unit tests, and remove the temporary `ping` tool.
- **M5 — Packaging & Launch:** Add `README.md` (with VHS terminal GIF and client config snippets for VS Code, Cursor, and Claude Desktop) and `.goreleaser.yaml` for cross-platform release binaries.