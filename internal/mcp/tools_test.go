package mcp

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/moncef-an/db-mcp/internal/config"
	"github.com/moncef-an/db-mcp/internal/db"
)

type mockRepo struct {
	listTablesFn    func(ctx context.Context, schema string) ([]db.TableSummary, error)
	describeTableFn func(ctx context.Context, schema, table string) (*db.TableSchema, error)
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

func (m *mockRepo) ExecuteReadOnlyQuery(context.Context, string, int) (*db.QueryResult, error) {
	return nil, errors.New("not implemented")
}

func (m *mockRepo) ExplainQuery(context.Context, string, bool) (*db.ExplainResult, error) {
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
