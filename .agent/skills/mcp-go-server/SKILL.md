---
name: mcp-go-server
description: Patterns, security invariants, and conventions for building and extending the db-mcp Go MCP server using github.com/modelcontextprotocol/go-sdk. Trigger when adding or modifying MCP tools, server transport, database repository queries, or logging in this repository.
---

# `db-mcp` Go MCP Server Guide

Follow these patterns and non-negotiable rules from `AGENTS.md` and `SPEC.md` when working on `db-mcp`.

## 1. `github.com/modelcontextprotocol/go-sdk` Patterns

### Server Setup & `stdio` Transport
Create the server with `mcp.NewServer` and run it over `&mcp.StdioTransport{}`:

```go
srv := mcp.NewServer(&mcp.Implementation{
    Name:    "db-mcp",
    Version: "0.1.0",
}, nil)

// Register tools before calling Run.
if err := srv.Run(ctx, &mcp.StdioTransport{}); err != nil {
    return fmt.Errorf("mcp server run: %w", err)
}
```

### Typed Tool Registration (`mcp.AddTool`)
Use generic `mcp.AddTool[In, Out]` with `json` and `jsonschema` struct tags so input/output schemas are inferred and validated automatically:

```go
type ListTablesInput struct {
    Schema string `json:"schema,omitempty" jsonschema:"PostgreSQL schema name (defaults to public)"`
}

mcp.AddTool(srv, &mcp.Tool{
    Name:        "list_tables",
    Description: "List tables and views in a schema with estimated row counts.",
}, s.handleListTables)
```

### Operational Errors (`IsError: true`)
Never panic or crash the server on bad tool input, guard rejections, missing tables, or query timeouts.
- With `mcp.AddTool`, returning a non-nil `error` from `ToolHandlerFor` automatically sets `CallToolResult.IsError = true` and places the error text in `Content` so the LLM can self-correct.
- Alternatively, construct `&mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: msg}}}` explicitly.
- Keep tool descriptions concise (1–2 sentences) and outputs bounded/structured for LLM context efficiency.

## 2. Non-Negotiable Repository Rules

1. **Zero `stdout` Pollution:** `stdout` is strictly reserved for JSON-RPC 2.0 MCP frames.
   - NEVER use `fmt.Print`, `fmt.Println`, `fmt.Printf`, `println`, or the standard `log` package.
   - Configure `log/slog` with `os.Stderr` in `cmd/db-mcp/main.go` and inject `*slog.Logger`.
   - NEVER log `DATABASE_URL` or raw credentials, even at `debug` level.
2. **Strict Layering:**
   - `internal/mcp` may only import `internal/db` (interface) and `internal/guard`—never `internal/db/postgres` or `pgx`.
   - `internal/guard` must remain pure Go with zero DB or MCP SDK imports.
3. **Database-Level Read-Only (`internal/db/postgres`):**
   - Every query execution (`run_query`, `explain_query`) MUST run inside an explicit read-only transaction:
     ```go
     tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
     if err != nil {
         return nil, fmt.Errorf("begin read-only tx: %w", err)
     }
     defer tx.Rollback()
     ```
   - Use parameterized queries (`$1`, `$2`) for all metadata lookups; never concatenate `schema` or `table` names into SQL.
4. **Bounded Resources:**
   - Always derive a timeout context for DB calls: `ctx, cancel := context.WithTimeout(ctx, r.queryTimeout); defer cancel()`.
   - Stop scanning `run_query` results at `limit + 1` rows; if an extra row exists, set `Truncated = true` and discard it.
5. **Pre-Flight Guard & Redaction:**
   - Validate all user-supplied SQL through `internal/guard` before calling `internal/db`.
   - Pass all `QueryResult` rows through `internal/guard` redaction before returning to the MCP client.
