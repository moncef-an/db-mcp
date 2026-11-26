package postgres

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
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
