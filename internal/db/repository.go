package db

import (
	"context"
	"encoding/json"
	"errors"
)

// ErrTableNotFound is returned when a requested table does not exist in the target schema.
var ErrTableNotFound = errors.New("table not found")

// Repository defines the database-agnostic interface used by MCP tools.
type Repository interface {
	Ping(ctx context.Context) error
	ListTables(ctx context.Context, schema string) ([]TableSummary, error)
	DescribeTable(ctx context.Context, schema, table string) (*TableSchema, error)
	ExecuteReadOnlyQuery(ctx context.Context, query string, maxRows int) (*QueryResult, error)
	ExplainQuery(ctx context.Context, query string, analyze bool) (*ExplainResult, error)
	Close() error
}

// TableSummary describes a table or view in a PostgreSQL schema.
type TableSummary struct {
	Name          string `json:"name"`
	Type          string `json:"type"`
	EstimatedRows int64  `json:"estimated_rows"`
}

// TableSchema holds full metadata for a single table.
type TableSchema struct {
	Schema      string           `json:"schema"`
	Table       string           `json:"table"`
	Columns     []ColumnInfo     `json:"columns"`
	PrimaryKeys []string         `json:"primary_keys"`
	ForeignKeys []ForeignKeyInfo `json:"foreign_keys"`
	Indexes     []IndexInfo      `json:"indexes"`
}

// ColumnInfo describes a table column.
type ColumnInfo struct {
	Name     string  `json:"name"`
	DataType string  `json:"data_type"`
	Nullable bool    `json:"nullable"`
	Default  *string `json:"default"`
}

// ForeignKeyInfo describes a foreign key constraint on a table.
type ForeignKeyInfo struct {
	Column         string `json:"column"`
	RefTable       string `json:"ref_table"`
	RefColumn      string `json:"ref_column"`
	ConstraintName string `json:"constraint_name"`
}

// IndexInfo describes an index on a table.
type IndexInfo struct {
	Name       string   `json:"name"`
	Columns    []string `json:"columns"`
	Unique     bool     `json:"unique"`
	Definition string   `json:"definition"`
}

// QueryResult holds bounded tabular output from a read-only query.
type QueryResult struct {
	Columns         []string `json:"columns"`
	Rows            [][]any  `json:"rows"`
	RowCount        int      `json:"row_count"`
	Truncated       bool     `json:"truncated"`
	ExecutionTimeMs int64    `json:"execution_time_ms"`
}

// ExplainResult holds the raw JSON execution plan from PostgreSQL EXPLAIN.
type ExplainResult struct {
	Plan            json.RawMessage `json:"plan"`
	ExecutionTimeMs int64           `json:"execution_time_ms"`
}
