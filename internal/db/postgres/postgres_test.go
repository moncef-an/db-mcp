package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
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

func TestExplainQuery(t *testing.T) {
	repo := openTestRepo(t, 5*time.Second)
	ctx := context.Background()

	res, err := repo.ExplainQuery(ctx, "SELECT 1", false)
	if err != nil {
		t.Fatalf("ExplainQuery(analyze=false) unexpected error: %v", err)
	}
	if !json.Valid(res.Plan) {
		t.Fatalf("ExplainQuery(analyze=false) returned invalid JSON: %s", string(res.Plan))
	}

	resAnalyze, err := repo.ExplainQuery(ctx, "SELECT 1", true)
	if err != nil {
		t.Fatalf("ExplainQuery(analyze=true) unexpected error: %v", err)
	}
	if !json.Valid(resAnalyze.Plan) {
		t.Fatalf("ExplainQuery(analyze=true) returned invalid JSON: %s", string(resAnalyze.Plan))
	}
	if !strings.Contains(string(resAnalyze.Plan), "Execution Time") {
		t.Errorf("expected Execution Time in ANALYZE plan, got: %s", string(resAnalyze.Plan))
	}
}

func TestExecuteReadOnlyQuery_DataTypes(t *testing.T) {
	repo := openTestRepo(t, 5*time.Second)
	ctx := context.Background()

	const q = `
		SELECT
			NULL::text AS nil_col,
			'hello'::text AS text_col,
			NOW() AS ts_col,
			'a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11'::uuid AS uuid_col,
			'{"k":"v"}'::jsonb AS jsonb_col,
			123.45::numeric AS num_col
	`
	res, err := repo.ExecuteReadOnlyQuery(ctx, q, 10)
	if err != nil {
		t.Fatalf("ExecuteReadOnlyQuery() unexpected error: %v", err)
	}
	if res.RowCount != 1 || len(res.Rows) != 1 {
		t.Fatalf("RowCount = %d, want 1", res.RowCount)
	}
	if res.Rows[0][0] != nil {
		t.Errorf("nil_col = %#v, want nil", res.Rows[0][0])
	}
	if res.Rows[0][1] != "hello" {
		t.Errorf("text_col = %#v, want \"hello\"", res.Rows[0][1])
	}

	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("json.Marshal(res) unexpected error: %v", err)
	}
	if !strings.Contains(string(raw), "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11") {
		t.Errorf("expected UUID string in JSON output, got: %s", string(raw))
	}
}

func TestListTablesAndDescribeTable(t *testing.T) {
	repo := openTestRepo(t, 5*time.Second)
	ctx := context.Background()

	// Use a separate plain admin connection for DDL setup/teardown because repo
	// enforces session-level default_transaction_read_only=on.
	adminDB, err := sql.Open("pgx", os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatalf("open admin db: %v", err)
	}
	t.Cleanup(func() {
		_ = adminDB.Close()
	})

	// Setup temporary schema/tables for isolated integration testing.
	const schema = "db_mcp_test_schema"
	_, err = adminDB.ExecContext(ctx, `
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
		_, _ = adminDB.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
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

func TestParseSecureConfig_NoCredentialLeak(t *testing.T) {
	badDSNs := []struct {
		name string
		dsn  string
	}{
		{
			name: "malformed url dsn",
			dsn:  "postgres://admin:SuperSecret123@db.internal:badport/app",
		},
		{
			name: "malformed kv dsn",
			dsn:  "host=db.internal user=admin password=SuperSecret123 port=badport",
		},
	}

	for _, tc := range badDSNs {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseSecureConfig(tc.dsn, 5*time.Second)
			if err == nil {
				t.Fatal("expected error for malformed DSN, got nil")
			}
			if strings.Contains(err.Error(), "SuperSecret123") || strings.Contains(err.Error(), "db.internal") {
				t.Fatalf("parseSecureConfig leaked credentials/DSN in error: %v", err)
			}

			_, newErr := New(context.Background(), tc.dsn, 5*time.Second, nil)
			if newErr == nil {
				t.Fatal("expected New() error for malformed DSN, got nil")
			}
			if strings.Contains(newErr.Error(), "SuperSecret123") || strings.Contains(newErr.Error(), "db.internal") {
				t.Fatalf("New() leaked credentials/DSN in error: %v", newErr)
			}
		})
	}
}

func TestParseSecureConfig_EnforcesExtendedProtocolAndSessionDefaults(t *testing.T) {
	cfg, err := parseSecureConfig("postgres://user:pass@localhost:5432/mydb?default_query_exec_mode=simple_protocol", 7*time.Second)
	if err != nil {
		t.Fatalf("parseSecureConfig() unexpected error: %v", err)
	}
	if cfg.DefaultQueryExecMode == pgx.QueryExecModeSimpleProtocol {
		t.Fatalf("DefaultQueryExecMode = SimpleProtocol, want extended protocol")
	}
	if cfg.DefaultQueryExecMode != pgx.QueryExecModeExec {
		t.Fatalf("DefaultQueryExecMode = %v, want QueryExecModeExec", cfg.DefaultQueryExecMode)
	}
	if got := cfg.RuntimeParams["default_transaction_read_only"]; got != "on" {
		t.Errorf("default_transaction_read_only = %q, want \"on\"", got)
	}
	if got := cfg.RuntimeParams["statement_timeout"]; got != "7000" {
		t.Errorf("statement_timeout = %q, want \"7000\"", got)
	}
	if got := cfg.RuntimeParams["lock_timeout"]; got != "7000" {
		t.Errorf("lock_timeout = %q, want \"7000\"", got)
	}
}

func TestRedactCredentials(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{
			in:   `dial error for postgres://alice:s3cr3t@db.example.com:5432/prod?sslmode=require`,
			want: `dial error for [REDACTED_DSN]`,
		},
		{
			in:   `failed postgresql://bob:pw@localhost/db`,
			want: `failed [REDACTED_DSN]`,
		},
		{
			in:   `host=localhost user=bob password=mysecret dbname=prod`,
			want: `host=localhost user=bob password=[REDACTED] dbname=prod`,
		},
		{
			in:   `host=localhost password='my secret' dbname=prod`,
			want: `host=localhost password=[REDACTED] dbname=prod`,
		},
	}

	for _, tc := range cases {
		got := redactCredentials(tc.in)
		if got != tc.want {
			t.Errorf("redactCredentials(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestInputValidation_ExecuteAndExplain(t *testing.T) {
	repo := &Repository{queryTimeout: 5 * time.Second}
	ctx := context.Background()

	for _, q := range []string{"", "   ", "\t\n", "SELECT\x00 1"} {
		if _, err := repo.ExecuteReadOnlyQuery(ctx, q, 10); err == nil {
			t.Errorf("ExecuteReadOnlyQuery(%q) expected error, got nil", q)
		}
		if _, err := repo.ExplainQuery(ctx, q, false); err == nil {
			t.Errorf("ExplainQuery(%q) expected error, got nil", q)
		}
	}

	for _, q := range []string{"EXPLAIN SELECT 1", "  explain (analyze) select 1", "EXPLAIN"} {
		if _, err := repo.ExplainQuery(ctx, q, false); err == nil {
			t.Errorf("ExplainQuery(%q) expected leading EXPLAIN error, got nil", q)
		}
	}
}

func TestEstimateValueBytes(t *testing.T) {
	if got := estimateValueBytes(nil); got != 4 {
		t.Errorf("estimateValueBytes(nil) = %d, want 4", got)
	}
	if got := estimateValueBytes("hello"); got != 5 {
		t.Errorf("estimateValueBytes(\"hello\") = %d, want 5", got)
	}
	if got := estimateValueBytes([]byte{1, 2, 3}); got != 3 {
		t.Errorf("estimateValueBytes([]byte) = %d, want 3", got)
	}
	if got := estimateValueBytes(42); got != 32 {
		t.Errorf("estimateValueBytes(42) = %d, want 32", got)
	}
}

func TestExecuteReadOnlyQuery_RejectsHugePayload(t *testing.T) {
	repo := openTestRepo(t, 5*time.Second)

	_, err := repo.ExecuteReadOnlyQuery(context.Background(), "SELECT repeat('A', 11 * 1024 * 1024)", 10)
	if err == nil || !strings.Contains(err.Error(), "exceeds maximum allowed size") {
		t.Fatalf("expected size limit error for huge payload, got: %v", err)
	}
}

func TestExplainQuery_RejectsBreakoutAndWrites(t *testing.T) {
	repo := openTestRepo(t, 5*time.Second)
	ctx := context.Background()

	if _, err := repo.ExplainQuery(ctx, "SELECT 1; COMMIT; BEGIN READ WRITE; DROP TABLE IF EXISTS db_mcp_nonexistent", false); err == nil {
		t.Fatal("expected error for multi-statement breakout in ExplainQuery, got nil")
	}
	if _, err := repo.ExplainQuery(ctx, "DELETE FROM pg_catalog.pg_class WHERE false", true); err == nil {
		t.Fatal("expected read-only error for EXPLAIN ANALYZE DELETE, got nil")
	}
}

func TestSessionDefaults_ReadOnlyAndTimeout(t *testing.T) {
	repo := openTestRepo(t, 3*time.Second)
	res, err := repo.ExecuteReadOnlyQuery(context.Background(), "SHOW default_transaction_read_only", 1)
	if err != nil {
		t.Fatalf("SHOW default_transaction_read_only failed: %v", err)
	}
	if len(res.Rows) != 1 || res.Rows[0][0] != "on" {
		t.Fatalf("default_transaction_read_only = %v, want [[on]]", res.Rows)
	}
}
