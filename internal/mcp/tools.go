package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/moncef-an/db-mcp/internal/db"
)

// PingInput defines the input schema for the temporary ping tool.
type PingInput struct {
	Message string `json:"message,omitempty" jsonschema:"Optional message to echo back"`
}

// PingOutput defines the structured output schema for the temporary ping tool.
type PingOutput struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

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

func (s *Server) registerTools() {
	mcp.AddTool(s.mcpServer, &mcp.Tool{
		Name:        "ping",
		Description: "Verify that the db-mcp server is running and responsive over stdio.",
	}, s.handlePing)

	mcp.AddTool(s.mcpServer, &mcp.Tool{
		Name:        "list_tables",
		Description: "List all tables and views in a PostgreSQL schema with estimated row counts.",
	}, s.handleListTables)

	mcp.AddTool(s.mcpServer, &mcp.Tool{
		Name:        "describe_table",
		Description: "Inspect columns, data types, nullability, defaults, primary keys, foreign keys, and indexes for a table.",
	}, s.handleDescribeTable)
}

func (s *Server) handlePing(_ context.Context, _ *mcp.CallToolRequest, in PingInput) (*mcp.CallToolResult, PingOutput, error) {
	msg := strings.TrimSpace(in.Message)
	if len(msg) > 256 {
		return toolError("message exceeds 256 character limit"), PingOutput{}, nil
	}
	if msg == "" {
		msg = "pong"
	}

	s.logger.Debug("handled ping tool call", "message_len", len(msg))
	return nil, PingOutput{
		Status:  "ok",
		Message: msg,
	}, nil
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
