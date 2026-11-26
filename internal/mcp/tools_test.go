package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/moncef-an/db-mcp/internal/config"
	"github.com/moncef-an/db-mcp/internal/db"
)

type mockRepo struct {
	listTablesFn           func(ctx context.Context, schema string) ([]db.TableSummary, error)
	describeTableFn        func(ctx context.Context, schema, table string) (*db.TableSchema, error)
	executeReadOnlyQueryFn func(ctx context.Context, query string, maxRows int) (*db.QueryResult, error)
	explainQueryFn         func(ctx context.Context, query string, analyze bool) (*db.ExplainResult, error)
}

func (m *mockRepo) Ping(context.Context) error { return nil }
func (m *mockRepo) Close() error               { return nil }

func (m *mockRepo) ListTables(ctx context.Context, schema string) ([]db.TableSummary, error) {
	if m.listTablesFn != nil {
		return m.listTablesFn(ctx, schema)
	}
	return nil, nil
}

func (m *mockRepo) DescribeTable(ctx context.Context, schema, table string) (*db.TableSchema, error) {
	if m.describeTableFn != nil {
		return m.describeTableFn(ctx, schema, table)
	}
	return nil, nil
}

func (m *mockRepo) ExecuteReadOnlyQuery(ctx context.Context, query string, maxRows int) (*db.QueryResult, error) {
	if m.executeReadOnlyQueryFn != nil {
		return m.executeReadOnlyQueryFn(ctx, query, maxRows)
	}
	return nil, errors.New("not implemented")
}

func (m *mockRepo) ExplainQuery(ctx context.Context, query string, analyze bool) (*db.ExplainResult, error) {
	if m.explainQueryFn != nil {
		return m.explainQueryFn(ctx, query, analyze)
	}
	return nil, errors.New("not implemented")
}

func TestHandleListTables(t *testing.T) {
	t.Run("defaults to public schema", func(t *testing.T) {
		var gotSchema string
		srv := NewServer(&mockRepo{
			listTablesFn: func(_ context.Context, schema string) ([]db.TableSummary, error) {
				gotSchema = schema
				return []db.TableSummary{
					{Name: "users", Type: "BASE TABLE", EstimatedRows: 42},
				}, nil
			},
		}, &config.Config{MaxRows: 100}, nil)

		res, out, err := srv.handleListTables(context.Background(), nil, ListTablesInput{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res != nil && res.IsError {
			t.Fatalf("expected IsError=false, got %+v", res)
		}
		if gotSchema != "public" || out.Schema != "public" {
			t.Errorf("schema = %q (out %q), want public", gotSchema, out.Schema)
		}
		if len(out.Tables) != 1 || out.Tables[0].Name != "users" {
			t.Errorf("unexpected tables: %+v", out.Tables)
		}
	})

	t.Run("custom schema and empty slice", func(t *testing.T) {
		srv := NewServer(&mockRepo{
			listTablesFn: func(_ context.Context, schema string) ([]db.TableSummary, error) {
				if schema != "analytics" {
					return nil, fmt.Errorf("unexpected schema %q", schema)
				}
				return nil, nil
			},
		}, &config.Config{MaxRows: 100}, nil)

		res, out, err := srv.handleListTables(context.Background(), nil, ListTablesInput{Schema: " analytics "})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res != nil && res.IsError {
			t.Fatalf("expected IsError=false, got %+v", res)
		}
		if out.Tables == nil || len(out.Tables) != 0 {
			t.Errorf("Tables = %#v, want non-nil empty slice", out.Tables)
		}
	})

	t.Run("repository error returns IsError true", func(t *testing.T) {
		srv := NewServer(&mockRepo{
			listTablesFn: func(context.Context, string) ([]db.TableSummary, error) {
				return nil, context.DeadlineExceeded
			},
		}, &config.Config{MaxRows: 100}, nil)

		res, _, err := srv.handleListTables(context.Background(), nil, ListTablesInput{})
		if err != nil {
			t.Fatalf("unexpected Go error: %v", err)
		}
		if res == nil || !res.IsError {
			t.Fatalf("expected CallToolResult.IsError=true, got %+v", res)
		}
	})
}

func TestHandleDescribeTable(t *testing.T) {
	t.Run("empty table returns IsError true", func(t *testing.T) {
		srv := NewServer(&mockRepo{}, &config.Config{MaxRows: 100}, nil)

		res, _, err := srv.handleDescribeTable(context.Background(), nil, DescribeTableInput{Table: "   "})
		if err != nil {
			t.Fatalf("unexpected Go error: %v", err)
		}
		if res == nil || !res.IsError {
			t.Fatalf("expected CallToolResult.IsError=true, got %+v", res)
		}
	})

	t.Run("missing table returns IsError true", func(t *testing.T) {
		srv := NewServer(&mockRepo{
			describeTableFn: func(context.Context, string, string) (*db.TableSchema, error) {
				return nil, db.ErrTableNotFound
			},
		}, &config.Config{MaxRows: 100}, nil)

		res, _, err := srv.handleDescribeTable(context.Background(), nil, DescribeTableInput{Table: "ghost"})
		if err != nil {
			t.Fatalf("unexpected Go error: %v", err)
		}
		if res == nil || !res.IsError {
			t.Fatalf("expected CallToolResult.IsError=true, got %+v", res)
		}
	})

	t.Run("valid table returns structured schema", func(t *testing.T) {
		want := &db.TableSchema{
			Schema: "public",
			Table:  "users",
			Columns: []db.ColumnInfo{
				{Name: "id", DataType: "bigint", Nullable: false},
			},
			PrimaryKeys: []string{"id"},
			ForeignKeys: []db.ForeignKeyInfo{},
			Indexes: []db.IndexInfo{
				{Name: "users_pkey", Columns: []string{"id"}, Unique: true, Definition: "CREATE UNIQUE INDEX users_pkey ON public.users USING btree (id)"},
			},
		}

		srv := NewServer(&mockRepo{
			describeTableFn: func(_ context.Context, schema, table string) (*db.TableSchema, error) {
				if schema != "public" || table != "users" {
					return nil, fmt.Errorf("unexpected args %s.%s", schema, table)
				}
				return want, nil
			},
		}, &config.Config{MaxRows: 100}, nil)

		res, out, err := srv.handleDescribeTable(context.Background(), nil, DescribeTableInput{Table: "users"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res != nil && res.IsError {
			t.Fatalf("expected IsError=false, got %+v", res)
		}
		if !reflect.DeepEqual(out, want) {
			t.Fatalf("DescribeTable output = %+v, want %+v", out, want)
		}
	})
}

func TestHandleRunQuery(t *testing.T) {
	t.Run("guard blocks DROP TABLE without calling repo", func(t *testing.T) {
		called := false
		srv := NewServer(&mockRepo{
			executeReadOnlyQueryFn: func(context.Context, string, int) (*db.QueryResult, error) {
				called = true
				return nil, nil
			},
		}, &config.Config{MaxRows: 100}, nil)

		res, _, err := srv.handleRunQuery(context.Background(), nil, RunQueryInput{SQL: "DROP TABLE users"})
		if err != nil {
			t.Fatalf("unexpected Go error: %v", err)
		}
		if called {
			t.Fatal("expected guard to block query before calling repository")
		}
		if res == nil || !res.IsError {
			t.Fatalf("expected IsError=true, got %+v", res)
		}
	})

	t.Run("clamps limit and redacts sensitive columns", func(t *testing.T) {
		var gotMaxRows int
		srv := NewServer(&mockRepo{
			executeReadOnlyQueryFn: func(_ context.Context, _ string, maxRows int) (*db.QueryResult, error) {
				gotMaxRows = maxRows
				return &db.QueryResult{
					Columns:         []string{"id", "email", "password_hash"},
					Rows:            [][]any{{1, "alice@example.com", "secret-hash"}},
					RowCount:        1,
					Truncated:       false,
					ExecutionTimeMs: 3,
				}, nil
			},
		}, &config.Config{
			MaxRows:       50,
			RedactColumns: []string{"password_hash", "token"},
		}, nil)

		res, out, err := srv.handleRunQuery(context.Background(), nil, RunQueryInput{
			SQL:   "SELECT id, email, password_hash FROM users",
			Limit: 9999,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res != nil && res.IsError {
			t.Fatalf("expected IsError=false, got %+v", res)
		}
		if gotMaxRows != 50 {
			t.Errorf("maxRows passed to repo = %d, want 50", gotMaxRows)
		}
		wantRows := [][]any{{1, "alice@example.com", "[REDACTED]"}}
		if !reflect.DeepEqual(out.Rows, wantRows) {
			t.Errorf("Rows = %#v, want %#v", out.Rows, wantRows)
		}
	})

	t.Run("repo error returns IsError true", func(t *testing.T) {
		srv := NewServer(&mockRepo{
			executeReadOnlyQueryFn: func(context.Context, string, int) (*db.QueryResult, error) {
				return nil, context.DeadlineExceeded
			},
		}, &config.Config{MaxRows: 100}, nil)

		res, _, err := srv.handleRunQuery(context.Background(), nil, RunQueryInput{SQL: "SELECT 1"})
		if err != nil {
			t.Fatalf("unexpected Go error: %v", err)
		}
		if res == nil || !res.IsError {
			t.Fatalf("expected IsError=true, got %+v", res)
		}
	})
}

func TestHandleExplainQuery(t *testing.T) {
	t.Run("guard blocks DELETE and leading EXPLAIN without calling repo", func(t *testing.T) {
		for _, badSQL := range []string{"DELETE FROM users", "EXPLAIN SELECT 1"} {
			called := false
			srv := NewServer(&mockRepo{
				explainQueryFn: func(context.Context, string, bool) (*db.ExplainResult, error) {
					called = true
					return nil, nil
				},
			}, &config.Config{MaxRows: 100}, nil)

			res, _, err := srv.handleExplainQuery(context.Background(), nil, ExplainQueryInput{SQL: badSQL})
			if err != nil {
				t.Fatalf("unexpected Go error: %v", err)
			}
			if called {
				t.Fatalf("expected guard to block %q before calling repo", badSQL)
			}
			if res == nil || !res.IsError {
				t.Fatalf("expected IsError=true for %q, got %+v", badSQL, res)
			}
		}
	})

	t.Run("forwards analyze flag and preserves json.RawMessage plan", func(t *testing.T) {
		wantPlan := json.RawMessage(`[{"Plan":{"Node Type":"Seq Scan"}}]`)
		var gotAnalyze bool
		srv := NewServer(&mockRepo{
			explainQueryFn: func(_ context.Context, _ string, analyze bool) (*db.ExplainResult, error) {
				gotAnalyze = analyze
				return &db.ExplainResult{
					Plan:            wantPlan,
					ExecutionTimeMs: 7,
				}, nil
			},
		}, &config.Config{MaxRows: 100}, nil)

		res, out, err := srv.handleExplainQuery(context.Background(), nil, ExplainQueryInput{
			SQL:     "SELECT * FROM users",
			Analyze: true,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res != nil && res.IsError {
			t.Fatalf("expected IsError=false, got %+v", res)
		}
		if !gotAnalyze {
			t.Error("expected analyze=true to be forwarded to repo")
		}
		if string(out.Plan) != string(wantPlan) {
			t.Errorf("Plan = %s, want %s", string(out.Plan), string(wantPlan))
		}
	})
}

func TestHandleQuery_MaxSQLLengthDoS(t *testing.T) {
	hugeSQL := "SELECT /* " + strings.Repeat("a", maxSQLLength) + " */ 1"

	t.Run("run_query rejects oversized SQL before repo", func(t *testing.T) {
		called := false
		srv := NewServer(&mockRepo{
			executeReadOnlyQueryFn: func(context.Context, string, int) (*db.QueryResult, error) {
				called = true
				return nil, nil
			},
		}, &config.Config{MaxRows: 100}, nil)

		res, _, err := srv.handleRunQuery(context.Background(), nil, RunQueryInput{SQL: hugeSQL})
		if err != nil {
			t.Fatalf("unexpected Go error: %v", err)
		}
		if called {
			t.Fatal("expected oversized SQL to be rejected before calling repo")
		}
		if res == nil || !res.IsError {
			t.Fatalf("expected IsError=true, got %+v", res)
		}
	})

	t.Run("explain_query rejects oversized SQL before repo", func(t *testing.T) {
		called := false
		srv := NewServer(&mockRepo{
			explainQueryFn: func(context.Context, string, bool) (*db.ExplainResult, error) {
				called = true
				return nil, nil
			},
		}, &config.Config{MaxRows: 100}, nil)

		res, _, err := srv.handleExplainQuery(context.Background(), nil, ExplainQueryInput{SQL: hugeSQL})
		if err != nil {
			t.Fatalf("unexpected Go error: %v", err)
		}
		if called {
			t.Fatal("expected oversized SQL to be rejected before calling repo")
		}
		if res == nil || !res.IsError {
			t.Fatalf("expected IsError=true, got %+v", res)
		}
	})
}

func TestHandleRunQuery_LimitClampingEdgeCases(t *testing.T) {
	tests := []struct {
		name         string
		cfgMaxRows   int
		requestLimit int
		wantMaxRows  int
	}{
		{name: "negative limit defaults to cfg.MaxRows", cfgMaxRows: 100, requestLimit: -1, wantMaxRows: 100},
		{name: "zero limit defaults to cfg.MaxRows", cfgMaxRows: 100, requestLimit: 0, wantMaxRows: 100},
		{name: "math.MaxInt limit clamps to cfg.MaxRows", cfgMaxRows: 100, requestLimit: math.MaxInt, wantMaxRows: 100},
		{name: "unvalidated cfg.MaxRows=math.MaxInt clamps to HardMaxRows", cfgMaxRows: math.MaxInt, requestLimit: math.MaxInt, wantMaxRows: config.HardMaxRows},
		{name: "unvalidated cfg.MaxRows=0 defaults to DefaultMaxRows", cfgMaxRows: 0, requestLimit: 0, wantMaxRows: config.DefaultMaxRows},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotMaxRows int
			srv := NewServer(&mockRepo{
				executeReadOnlyQueryFn: func(_ context.Context, _ string, maxRows int) (*db.QueryResult, error) {
					gotMaxRows = maxRows
					return &db.QueryResult{Columns: []string{"ok"}, Rows: [][]any{{1}}, RowCount: 1}, nil
				},
			}, &config.Config{MaxRows: tc.cfgMaxRows}, nil)

			res, _, err := srv.handleRunQuery(context.Background(), nil, RunQueryInput{
				SQL:   "SELECT 1",
				Limit: tc.requestLimit,
			})
			if err != nil {
				t.Fatalf("unexpected Go error: %v", err)
			}
			if res != nil && res.IsError {
				t.Fatalf("expected IsError=false, got %+v", res)
			}
			if gotMaxRows != tc.wantMaxRows {
				t.Errorf("gotMaxRows = %d, want %d", gotMaxRows, tc.wantMaxRows)
			}
		})
	}
}

func TestToolErrors_RedactDatabaseURLAndPassword(t *testing.T) {
	dsn := "postgres://dbuser:topsecret123@db.internal:5432/prod"
	srv := NewServer(&mockRepo{
		listTablesFn: func(context.Context, string) ([]db.TableSummary, error) {
			return nil, fmt.Errorf("dial %s: password topsecret123 rejected", dsn)
		},
	}, &config.Config{DatabaseURL: dsn, MaxRows: 100}, nil)

	res, _, err := srv.handleListTables(context.Background(), nil, ListTablesInput{})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if res == nil || !res.IsError || len(res.Content) == 0 {
		t.Fatalf("expected IsError=true with content, got %+v", res)
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected *mcp.TextContent, got %T", res.Content[0])
	}
	if strings.Contains(tc.Text, "topsecret123") || strings.Contains(tc.Text, "db.internal") {
		t.Fatalf("toolError leaked DSN or password: %q", tc.Text)
	}
}
