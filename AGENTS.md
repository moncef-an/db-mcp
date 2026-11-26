# db-mcp — Project Specification & Agent Rules (v0.1)

Read this entire document before proposing plans or writing code. It serves as the single source of truth for both the system design and coding rules.

---

## 1. Overview & Tech Stack
`db-mcp` is a single-binary, zero-runtime-dependency Model Context Protocol (MCP) server written in Go. It connects AI coding assistants (Cursor, VS Code, Claude Desktop, etc.) to PostgreSQL databases safely, enabling schema inspection, read-only querying, and slow-query analysis without exposing the database to destructive operations.

- **Language:** Go (latest stable, idiomatic standard library first).
- **MCP SDK:** `github.com/modelcontextprotocol/go-sdk`.
- **PostgreSQL Driver:** `github.com/jackc/pgx/v5/stdlib` (via standard `database/sql`) or `pgxpool` encapsulated strictly inside `internal/db/postgres`.
- **Logging:** Standard library `log/slog` only.
- **Dependency Rule:** Do NOT add any third-party package without explicitly proposing it and getting approval first.

---

## 2. Scope & Non-Goals (v0.1)

**In Scope (v0.1):**
- PostgreSQL support via a clean database adapter interface.
- `stdio` MCP transport using the official Go MCP SDK.
- Strict read-only enforcement, query timeouts, row limits, and sensitive column redaction.
- 4 core MCP tools: `list_tables`, `describe_table`, `run_query`, `explain_query`.

**Out of Scope (Planned for v0.2+):**
- MySQL / SQLite adapters.
- Streamable HTTP / SSE transport.
- Write/DDL operations or migration generation.
- `pg_stat_statements` slow-query log aggregation (v0.2).

---

## 3. Architecture & Directory Structure

```text
AI Client (JSON-RPC 2.0 over stdio)
        │
        ▼
internal/mcp         (Tool registration, input/output DTOs, MCP error formatting)
        │
        ▼
internal/guard       (SQL pre-flight validation, row limits, timeout bounds, column redaction)
        │
        ▼
internal/db          (Repository interface & domain types)
        │
        ▼
internal/db/postgres (PostgreSQL implementation with ReadOnly transactions)
```

```text
db-mcp/
├── cmd/
│   └── db-mcp/
│       └── main.go              # Entrypoint, config loading, wiring, graceful shutdown
├── internal/
│   ├── config/
│   │   └── config.go            # Env vars & CLI flags parsing + validation
│   ├── db/
│   │   ├── repository.go        # DB-agnostic interface & domain types
│   │   └── postgres/
│   │       ├── postgres.go      # PostgreSQL adapter implementation
│   │       └── postgres_test.go # Integration tests (read-only enforcement)
│   ├── guard/
│   │   ├── guard.go             # SQL validator, row cap, sensitive data redactor
│   │   └── guard_test.go        # Unit tests for injection & redaction edge cases
│   └── mcp/
│       ├── server.go            # MCP server setup & tool registration
│       └── tools.go             # Tool handlers & DTOs
├── Makefile
├── AGENTS.md                    # (or SPEC.md) This file
└── go.mod
```

---

## 4. Configuration
Configured via environment variables (with optional CLI flag overrides):

| Variable | Required | Default | Description |
| :--- | :--- | :--- | :--- |
| `DATABASE_URL` | Yes | — | PostgreSQL connection string (`postgres://...`). |
| `DB_QUERY_TIMEOUT` | No | `5s` | Per-query execution timeout (hard cap: `30s`). |
| `DB_MAX_ROWS` | No | `100` | Default max rows returned by `run_query` (hard cap: `1000`). |
| `DB_REDACT_COLUMNS` | No | `password,password_hash,secret,token,api_key,access_key,ssn` | Comma-separated substrings; matching column values are replaced with `[REDACTED]`. |
| `LOG_LEVEL` | No | `info` | Structured log level (`debug`, `info`, `warn`, `error`) written strictly to `stderr`. |

---

## 5. Non-Negotiable Security & Protocol Rules
1. **ZERO `stdout` Pollution:** This server communicates via MCP over `stdio`. NEVER use `fmt.Print`, `fmt.Println`, `fmt.Printf`, `println`, or the default `log` package anywhere in the codebase. All logs MUST use `log/slog` configured with `os.Stderr`.
2. **Database-Level Read-Only (Primary Defense):** Every query execution in `internal/db/postgres` MUST run inside an explicit transaction started with `ReadOnly: true` (`BeginTx(ctx, &sql.TxOptions{ReadOnly: true})`) and MUST call `defer tx.Rollback()`. Never rely solely on string/regex validation.
3. **Pre-Flight SQL Validation (Defense in Depth):** All SQL coming from MCP tool inputs MUST pass through `internal/guard` before reaching `internal/db`:
   - Reject empty queries and multi-statement queries (semicolons separating statements outside string literals).
   - Allow only `SELECT`, `WITH` (CTEs), and `EXPLAIN` statements.
   - Reject queries containing mutating keywords (`INSERT`, `UPDATE`, `DELETE`, `DROP`, `ALTER`, `TRUNCATE`, `GRANT`, `REVOKE`, `CREATE`, `COPY`) as standalone tokens.
4. **Bounded Resources:**
   - Every DB call and tool handler MUST accept `ctx context.Context` and respect `DB_QUERY_TIMEOUT` via `context.WithTimeout`.
   - `run_query` stops scanning after `limit + 1` rows; if an extra row exists, `truncated: true` is returned and the extra row is discarded.
5. **Sensitive Data Redaction & No Secret Leakage:**
   - All query results MUST pass through `internal/guard` redaction before returning to the MCP client: any column whose lowercase name contains a configured redact pattern has its value replaced with `"[REDACTED]"`.
   - Never log `DATABASE_URL` or raw credentials, even at `debug` level.

---

## 6. MCP Tools Contract (v0.1)

### 6.1 `list_tables`
- **Description:** Lists all tables and views in a schema with estimated row counts.
- **Input:** `{ "schema": "public" }` (`schema` optional, defaults to `"public"`).
- **Output:** `{ "schema": "public", "tables": [{ "name": "users", "type": "BASE TABLE", "estimated_rows": 1420 }] }`.

### 6.2 `describe_table`
- **Description:** Returns full schema metadata for a table: columns, data types, nullability, defaults, primary keys, foreign keys, and indexes.
- **Input:** `{ "table": "users", "schema": "public" }` (`table` required, `schema` optional).
- **Output:** Structured JSON with `columns[]`, `primary_keys[]`, `foreign_keys[]`, and `indexes[]`.

### 6.3 `run_query`
- **Description:** Executes a read-only SQL query (`SELECT` / `WITH`) and returns tabular results.
- **Input:** `{ "sql": "SELECT id, email FROM users LIMIT 10", "limit": 100 }` (`sql` required, `limit` optional, clamped to `DB_MAX_ROWS`).
- **Output:** `{ "columns": [...], "rows": [[...]], "row_count": 10, "truncated": false, "execution_time_ms": 4 }`.

### 6.4 `explain_query`
- **Description:** Runs `EXPLAIN (FORMAT JSON)` on a read-only query to inspect the execution plan and diagnose slow queries.
- **Input:** `{ "sql": "SELECT ...", "analyze": false }` (`analyze` optional; when `true`, runs `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)` inside a rolled-back `ReadOnly` transaction).
- **Output:** `{ "plan": { ... }, "execution_time_ms": 12 }`.

---

## 7. Coding Conventions & Architecture Rules
- **Strict Layering:**
  - `internal/mcp` may only depend on `internal/db` (interface) and `internal/guard`. It must NEVER import `internal/db/postgres` or `pgx`.
  - `internal/guard` must remain pure Go with zero database or MCP SDK imports so it is fast and trivial to unit test.
- **Dependency Injection:** Wire all dependencies explicitly in `cmd/db-mcp/main.go`. No package-level global DB connections or mutable singletons.
- **Error Handling:**
  - Wrap internal errors with context using `fmt.Errorf("context: %w", err)`.
  - Return operational errors (invalid SQL, timeout, table not found) as structured MCP tool errors (`IsError: true`) with clear, actionable messages—never panic or crash the server process on bad tool input.
- **SQL Safety:** Use parameterized queries (`$1`, `$2`) for all metadata queries in `list_tables` and `describe_table`. Never concatenate `schema` or `table` names directly into SQL strings.

---

## 8. Testing & Verification Requirements
Every feature or bug fix MUST include tests:
- **`internal/guard` (Unit Tests):** Table-driven tests covering valid `SELECT`/`WITH` queries, multi-statement injection (`SELECT 1; DROP TABLE users`), mutating CTEs (`WITH d AS (DELETE FROM users RETURNING *) SELECT * FROM d`), row truncation, and column redaction.
- **`internal/db/postgres` (Integration Tests):** Guarded by `TEST_DATABASE_URL` env var (skip cleanly if unset). Must verify that write attempts fail even if the guard is bypassed.
- **`internal/mcp` (Unit Tests):** Use a mock `db.Repository` to test tool schemas, error handling, and output formatting.

Run these verification commands before concluding any implementation task:
```bash
gofmt -l .
go vet ./...
go test -race ./...
```

---

## 9. Implementation Milestones (Vertical Slices)
- **M1 — Skeleton & Transport:** Initialize Go module, `Makefile`, `config`, `slog` to `stderr`, and a bare MCP server with a temporary `ping` tool verified via `@modelcontextprotocol/inspector`.
- **M2 — Guard & DB Layer:** Implement `internal/guard` (with full unit tests) and `internal/db/postgres` with `ReadOnly` transaction enforcement + integration tests proving `INSERT`/`DELETE`/`DROP` fail.
- **M3 — Schema Discovery Tools:** Implement `list_tables` and `describe_table` end-to-end and test with MCP Inspector.
- **M4 — Query & Performance Tools:** Implement `run_query` (with row cap + redaction) and `explain_query`, then remove the temporary `ping` tool.
- **M5 — Release Readiness:** Add `README.md` with VHS terminal GIF, client setup snippets (VS Code / Cursor / Claude), and `.goreleaser.yaml` for cross-platform binaries.

---

## 10. Agent Workflow
- Work **one milestone or vertical slice at a time** (following Section 9).
- Before modifying more than 2 files, present a concise plan listing the files to create/edit and the key design choices.
- Keep diffs minimal and focused—do not refactor unrelated code or rename existing symbols unless requested.
- When implementing non-trivial logic (such as SQL guard checks or transaction handling), briefly explain *why* the approach was chosen so the maintainer can review and learn from every change.