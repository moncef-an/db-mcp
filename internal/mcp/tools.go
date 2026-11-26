package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/moncef-an/db-mcp/internal/db"
	"github.com/moncef-an/db-mcp/internal/guard"
)

// ListTablesInput defines the input schema for list_tables.
type ListTablesInput struct {
	Schema string `json:"schema,omitempty" jsonschema:"PostgreSQL schema name (defaults to public)"`
}

// ListTablesOutput defines the structured output schema for list_tables.
type ListTablesOutput struct {
	Schema string            `json:"schema"`
	Tables []db.TableSummary `json:"tables"`
}

// DescribeTableInput defines the input schema for describe_table.
type DescribeTableInput struct {
	Table  string `json:"table" jsonschema:"Table name to inspect (required)"`
	Schema string `json:"schema,omitempty" jsonschema:"PostgreSQL schema name (defaults to public)"`
}

// RunQueryInput defines the input schema for run_query.
type RunQueryInput struct {
	SQL   string `json:"sql" jsonschema:"Read-only SQL SELECT or WITH query to execute (required)"`
	Limit int    `json:"limit,omitempty" jsonschema:"Maximum number of rows to return (clamped to server max_rows)"`
}

// ExplainQueryInput defines the input schema for explain_query.
type ExplainQueryInput struct {
	SQL     string `json:"sql" jsonschema:"Read-only SQL SELECT or WITH query to explain without a leading EXPLAIN (required)"`
	Analyze bool   `json:"analyze,omitempty" jsonschema:"If true, runs EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) inside a rolled-back read-only transaction"`
}

func (s *Server) registerTools() {
	mcp.AddTool(s.mcpServer, &mcp.Tool{
		Name:        "list_tables",
		Description: "List all tables and views in a PostgreSQL schema with estimated row counts.",
	}, s.handleListTables)

	mcp.AddTool(s.mcpServer, &mcp.Tool{
		Name:        "describe_table",
		Description: "Inspect columns, data types, nullability, defaults, primary keys, foreign keys, and indexes for a table.",
	}, s.handleDescribeTable)

	mcp.AddTool(s.mcpServer, &mcp.Tool{
		Name:        "run_query",
		Description: "Execute a read-only SQL SELECT or WITH query and return bounded, redacted tabular results.",
	}, s.handleRunQuery)

	mcp.AddTool(s.mcpServer, &mcp.Tool{
		Name:        "explain_query",
		Description: "Inspect PostgreSQL's JSON execution plan for a read-only SELECT or WITH query.",
	}, s.handleExplainQuery)
}

func (s *Server) handleListTables(ctx context.Context, _ *mcp.CallToolRequest, in ListTablesInput) (*mcp.CallToolResult, ListTablesOutput, error) {
	schema := strings.TrimSpace(in.Schema)
	if schema == "" {
		schema = "public"
	}

	tables, err := s.repo.ListTables(ctx, schema)
	if err != nil {
		s.logger.Warn("list_tables failed", "schema", schema, "error", err)
		return toolError(fmt.Sprintf("failed to list tables in schema %q: %v", schema, err)), ListTablesOutput{}, nil
	}
	if tables == nil {
		tables = make([]db.TableSummary, 0)
	}

	s.logger.Debug("handled list_tables", "schema", schema, "count", len(tables))
	return nil, ListTablesOutput{
		Schema: schema,
		Tables: tables,
	}, nil
}

func (s *Server) handleDescribeTable(ctx context.Context, _ *mcp.CallToolRequest, in DescribeTableInput) (*mcp.CallToolResult, *db.TableSchema, error) {
	table := strings.TrimSpace(in.Table)
	if table == "" {
		return toolError("table is required"), nil, nil
	}

	schema := strings.TrimSpace(in.Schema)
	if schema == "" {
		schema = "public"
	}

	desc, err := s.repo.DescribeTable(ctx, schema, table)
	if err != nil {
		if errors.Is(err, db.ErrTableNotFound) {
			return toolError(fmt.Sprintf("table %q not found in schema %q", table, schema)), nil, nil
		}
		s.logger.Warn("describe_table failed", "schema", schema, "table", table, "error", err)
		return toolError(fmt.Sprintf("failed to describe table %q.%q: %v", schema, table, err)), nil, nil
	}

	s.logger.Debug("handled describe_table", "schema", schema, "table", table)
	return nil, desc, nil
}

func (s *Server) handleRunQuery(ctx context.Context, _ *mcp.CallToolRequest, in RunQueryInput) (*mcp.CallToolResult, *db.QueryResult, error) {
	if err := guard.ValidateReadOnlyQuery(in.SQL); err != nil {
		s.logger.Warn("run_query rejected by guard", "error", err)
		return toolError(fmt.Sprintf("query rejected: %v", err)), nil, nil
	}

	limit := guard.ClampLimit(in.Limit, s.cfg.MaxRows)
	res, err := s.repo.ExecuteReadOnlyQuery(ctx, in.SQL, limit)
	if err != nil {
		s.logger.Warn("run_query failed", "error", err)
		return toolError(fmt.Sprintf("query failed: %v", err)), nil, nil
	}

	if res.Columns == nil {
		res.Columns = make([]string, 0)
	}
	if res.Rows == nil {
		res.Rows = make([][]any, 0)
	}
	guard.RedactRows(res.Columns, res.Rows, s.cfg.RedactColumns)

	s.logger.Debug("handled run_query", "row_count", res.RowCount, "truncated", res.Truncated, "execution_time_ms", res.ExecutionTimeMs)
	return nil, res, nil
}

func (s *Server) handleExplainQuery(ctx context.Context, _ *mcp.CallToolRequest, in ExplainQueryInput) (*mcp.CallToolResult, *db.ExplainResult, error) {
	if err := guard.ValidateReadOnlyQuery(in.SQL); err != nil {
		s.logger.Warn("explain_query rejected by guard", "error", err)
		return toolError(fmt.Sprintf("query rejected: %v", err)), nil, nil
	}

	res, err := s.repo.ExplainQuery(ctx, in.SQL, in.Analyze)
	if err != nil {
		s.logger.Warn("explain_query failed", "analyze", in.Analyze, "error", err)
		return toolError(fmt.Sprintf("explain failed: %v", err)), nil, nil
	}

	s.logger.Debug("handled explain_query", "analyze", in.Analyze, "execution_time_ms", res.ExecutionTimeMs)
	return nil, res, nil
}

// toolError returns an explicit MCP tool error result (IsError: true) so operational
// failures are surfaced to the LLM without crashing or emitting protocol-level errors.
func toolError(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{
			&mcp.TextContent{Text: msg},
		},
	}
}
