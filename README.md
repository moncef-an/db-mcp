# db-mcp

A fast, single-binary, zero-runtime-dependency **Model Context Protocol (MCP)** server for PostgreSQL written in Go. Connect Claude Desktop, Cursor, VS Code, or Antigravity to your PostgreSQL databases over `stdio` for safe schema discovery, bounded read-only querying, and `EXPLAIN` plan inspection.

---

## Security Architecture

`db-mcp` enforces **defense-in-depth** across application, driver, and database layers:

1. **Pure-Go SQL Lexer Guard (`internal/guard`):**
   - Tokenizes SQL before it ever reaches the driver, handling `--` (`\n` and `\r`) and nested `/* ... */` comments, single-quoted and `E'...'` escape strings, double-quoted identifiers, and PostgreSQL dollar-quoted strings (`$$...$$` / `$tag$...$tag$`).
   - Rejects NUL bytes (`\x00`), empty/oversized queries (`> 64 KiB`), multi-statement queries, non-`SELECT`/`WITH` statements, mutating/privileged keywords (`INSERT`, `UPDATE`, `DELETE`, `DROP`, `ALTER`, `TRUNCATE`, `GRANT`, `REVOKE`, `CREATE`, `COPY`, `CALL`, `DO`, `EXECUTE`, `SET`, `LOCK`, `INTO`, `MERGE`, `FOR SHARE`), and dangerous PostgreSQL built-in functions (`set_config`, `pg_read_file`, `pg_ls_dir`, `pg_stat_file`, `lo_import`, `lo_export`, `pg_terminate_backend`, `pg_cancel_backend`, `pg_reload_conf`, `dblink*`).
2. **Forced Extended Protocol & Read-Only Sessions (`internal/db/postgres`):**
   - Forces `pgx.QueryExecModeExec` (preventing `simple_protocol` multi-statement smuggling).
   - Configures session-level `default_transaction_read_only=on`, `statement_timeout`, and `lock_timeout`.
   - Wraps every query and `EXPLAIN` call in `BeginTx(ctx, &sql.TxOptions{ReadOnly: true})` with unconditional `defer tx.Rollback()` and `context.WithTimeout`.
3. **Bounded Resources:**
   - `run_query` stops scanning at `limit + 1` rows (hard cap `1000`), sets `truncated: true` if more rows exist, and enforces a `10 MiB` payload cap on query results and `EXPLAIN` plans.
4. **Automatic Redaction & `stdio` Hygiene:**
   - Matching columns (`password`, `password_hash`, `secret`, `token`, `api_key`, `access_key`, `ssn`, etc.) are replaced with `[REDACTED]` before serialization.
   - `DATABASE_URL` credentials are redacted from logs and error messages. All logs go strictly to `stderr`; `stdout` carries only JSON-RPC 2.0 MCP frames.

---

## Tools Reference

| Tool | Description | Parameters |
| :--- | :--- | :--- |
| `list_tables` | List tables and views in a schema with estimated row counts (`pg_class.reltuples`). | `schema` (optional, default `"public"`) |
| `describe_table` | Inspect columns, data types, nullability, defaults, primary keys, foreign keys, and indexes. | `table` (required), `schema` (optional, default `"public"`) |
| `run_query` | Execute a read-only `SELECT` or `WITH` query with row clamping and column redaction. | `sql` (required), `limit` (optional, clamped to `DB_MAX_ROWS`) |
| `explain_query` | Return PostgreSQL's JSON execution plan for a `SELECT` or `WITH` query. | `sql` (required), `analyze` (optional, default `false`) |

<details>
<summary>Example tool outputs</summary>

**`list_tables`**
```json
{
  "schema": "public",
  "tables": [
    { "name": "users", "type": "BASE TABLE", "estimated_rows": 1420 }
  ]
}
```

**`run_query`**
```json
{
  "columns": ["id", "email", "password_hash"],
  "rows": [[1, "alice@example.com", "[REDACTED]"]],
  "row_count": 1,
  "truncated": false,
  "execution_time_ms": 4
}
```

</details>

---

## Installation

### Go Install
```bash
go install github.com/moncef-an/db-mcp/cmd/db-mcp@latest
```

### GitHub Releases
Download the prebuilt static binary for Linux, macOS, or Windows (`amd64` / `arm64`) from the [Releases](https://github.com/moncef-an/db-mcp/releases) page.

### Docker
```bash
docker run -i --rm \
  -e DATABASE_URL="postgres://readonly_user:password@host.docker.internal:5432/mydb?sslmode=disable" \
  db-mcp:latest
```

---

## Client Configuration

### Claude Desktop (`claude_desktop_config.json`)
```json
{
  "mcpServers": {
    "postgres": {
      "command": "db-mcp",
      "env": {
        "DATABASE_URL": "postgres://readonly_user:password@localhost:5432/mydb?sslmode=disable"
      }
    }
  }
}
```

### Cursor (`.cursor/mcp.json`)
```json
{
  "mcpServers": {
    "postgres": {
      "command": "db-mcp",
      "args": ["--max-rows", "100", "--query-timeout", "5s"],
      "env": {
        "DATABASE_URL": "postgres://readonly_user:password@localhost:5432/mydb?sslmode=disable"
      }
    }
  }
}
```

### VS Code / Antigravity (`.vscode/mcp.json`)
```json
{
  "servers": {
    "postgres": {
      "type": "stdio",
      "command": "db-mcp",
      "env": {
        "DATABASE_URL": "postgres://readonly_user:password@localhost:5432/mydb?sslmode=disable"
      }
    }
  }
}
```

---

## Recommended PostgreSQL Read-Only Role

For production databases, pair `db-mcp` with a dedicated least-privilege PostgreSQL role:

```sql
CREATE ROLE db_mcp_readonly WITH LOGIN PASSWORD 'change-me-strong-password';
GRANT CONNECT ON DATABASE mydb TO db_mcp_readonly;
GRANT USAGE ON SCHEMA public TO db_mcp_readonly;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO db_mcp_readonly;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO db_mcp_readonly;
```

---

## Configuration Reference

CLI flags take precedence over environment variables. Invalid or out-of-bounds values fail fast at startup.

| Env Variable | CLI Flag | Required | Default | Hard Limit | Description |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `DATABASE_URL` | `--database-url` | Yes | — | — | PostgreSQL connection URI (`postgres://` or `postgresql://`). |
| `DB_QUERY_TIMEOUT` | `--query-timeout` | No | `5s` | `30s` | Per-query timeout (`> 0` and `<= 30s`). |
| `DB_MAX_ROWS` | `--max-rows` | No | `100` | `1000` | Default max rows returned by `run_query` (`> 0` and `<= 1000`). |
| `DB_REDACT_COLUMNS` | `--redact-columns` | No | `password,password_hash,secret,token,api_key,access_key,ssn` | — | Comma-separated case-insensitive substrings to redact. |
| `LOG_LEVEL` | `--log-level` | No | `info` | — | Structured log level (`debug`, `info`, `warn`, `error`) to `stderr`. |
