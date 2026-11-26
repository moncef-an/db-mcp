package postgres

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/moncef-an/db-mcp/internal/db"
)

func openTestRepo(t *testing.T, timeout time.Duration) *Repository {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	repo, err := New(context.Background(), dsn, timeout, nil)
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	t.Cleanup(func() {
		_ = repo.Close()
	})
	return repo
}

func TestExecuteReadOnlyQuery_RejectsWritesAtDBLevel(t *testing.T) {
	repo := openTestRepo(t, 5*time.Second)

	writeQueries := []struct {
		name string
		sql  string
	}{
		{name: "create table", sql: "CREATE TABLE db_mcp_should_fail (id int)"},
		{name: "insert", sql: "INSERT INTO pg_catalog.pg_description VALUES (0, 0, 0, 'x')"},
		{name: "delete", sql: "DELETE FROM pg_catalog.pg_class WHERE false"},
		{name: "drop table", sql: "DROP TABLE IF EXISTS db_mcp_nonexistent"},
	}

	for _, tc := range writeQueries {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := repo.ExecuteReadOnlyQuery(context.Background(), tc.sql, 10); err == nil {
				t.Fatalf("expected PostgreSQL read-only error for %q, got nil", tc.sql)
			}
		})
	}
}

func TestExecuteReadOnlyQuery_Truncation(t *testing.T) {
	repo := openTestRepo(t, 5*time.Second)

	res, err := repo.ExecuteReadOnlyQuery(context.Background(), "SELECT generate_series(1, 5) AS n", 3)
	if err != nil {
		t.Fatalf("ExecuteReadOnlyQuery() unexpected error: %v", err)
	}
	if res.RowCount != 3 {
		t.Errorf("RowCount = %d, want 3", res.RowCount)
	}
	if !res.Truncated {
		t.Errorf("Truncated = false, want true")
	}

	resExact, err := repo.ExecuteReadOnlyQuery(context.Background(), "SELECT generate_series(1, 5) AS n", 5)
	if err != nil {
		t.Fatalf("ExecuteReadOnlyQuery() unexpected error: %v", err)
	}
	if resExact.RowCount != 5 {
		t.Errorf("RowCount = %d, want 5", resExact.RowCount)
	}
	if resExact.Truncated {
		t.Errorf("Truncated = true, want false")
	}
}

func TestExecuteReadOnlyQuery_TimeoutCancellation(t *testing.T) {
	repo := openTestRepo(t, 100*time.Millisecond)

	if _, err := repo.ExecuteReadOnlyQuery(context.Background(), "SELECT pg_sleep(2)", 10); err == nil {
		t.Fatal("expected timeout error for pg_sleep(2), got nil")
	}
}

func TestListTablesAndDescribeTable(t *testing.T) {
	repo := openTestRepo(t, 5*time.Second)
	ctx := context.Background()

	// Setup temporary schema/tables for isolated integration testing.
	const schema = "db_mcp_test_schema"
	_, err := repo.db.ExecContext(ctx, `
		DROP SCHEMA IF EXISTS `+schema+` CASCADE;
		CREATE SCHEMA `+schema+`;
		CREATE TABLE `+schema+`.parents (
			id BIGINT PRIMARY KEY,
			email TEXT NOT NULL UNIQUE
		);
		CREATE TABLE `+schema+`.children (
			id BIGINT PRIMARY KEY,
			parent_id BIGINT NOT NULL REFERENCES `+schema+`.parents(id),
			note TEXT DEFAULT 'none'
		);
		CREATE INDEX idx_children_parent_id ON `+schema+`.children(parent_id);
		CREATE VIEW `+schema+`.child_view AS SELECT id, parent_id FROM `+schema+`.children;
	`)
	if err != nil {
		t.Fatalf("setup test schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = repo.db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
	})

	tables, err := repo.ListTables(ctx, schema)
	if err != nil {
		t.Fatalf("ListTables() unexpected error: %v", err)
	}
	if len(tables) != 3 {
		t.Fatalf("ListTables() len = %d, want 3 (%+v)", len(tables), tables)
	}
	for _, tbl := range tables {
		if tbl.EstimatedRows < 0 {
			t.Errorf("table %q EstimatedRows = %d, want >= 0", tbl.Name, tbl.EstimatedRows)
		}
	}

	desc, err := repo.DescribeTable(ctx, schema, "children")
	if err != nil {
		t.Fatalf("DescribeTable(children) unexpected error: %v", err)
	}
	if len(desc.Columns) != 3 {
		t.Errorf("Columns len = %d, want 3", len(desc.Columns))
	}
	if len(desc.PrimaryKeys) != 1 || desc.PrimaryKeys[0] != "id" {
		t.Errorf("PrimaryKeys = %v, want [id]", desc.PrimaryKeys)
	}
	if len(desc.ForeignKeys) != 1 || desc.ForeignKeys[0].RefTable != "parents" || desc.ForeignKeys[0].RefColumn != "id" {
		t.Errorf("ForeignKeys = %+v, want reference to parents(id)", desc.ForeignKeys)
	}
	if len(desc.Indexes) < 2 {
		t.Errorf("Indexes len = %d, want >= 2 (pkey + idx_children_parent_id)", len(desc.Indexes))
	}

	_, err = repo.DescribeTable(ctx, schema, "does_not_exist")
	if !errors.Is(err, db.ErrTableNotFound) {
		t.Fatalf("DescribeTable(missing) error = %v, want errors.Is(db.ErrTableNotFound)", err)
	}
}
