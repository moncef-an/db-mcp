package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/moncef-an/db-mcp/internal/db"
)

var _ db.Repository = (*Repository)(nil)

// Repository implements db.Repository backed by PostgreSQL via pgx/v5/stdlib.
type Repository struct {
	db           *sql.DB
	queryTimeout time.Duration
	logger       *slog.Logger
}

// New opens a PostgreSQL connection pool, verifies connectivity with Ping, and returns a Repository.
func New(ctx context.Context, dsn string, queryTimeout time.Duration, logger *slog.Logger) (*Repository, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if queryTimeout <= 0 {
		queryTimeout = 5 * time.Second
	}

	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres pool: %w", err)
	}

	sqlDB.SetMaxOpenConns(10)
	sqlDB.SetMaxIdleConns(5)
	sqlDB.SetConnMaxLifetime(30 * time.Minute)

	repo := &Repository{
		db:           sqlDB,
		queryTimeout: queryTimeout,
		logger:       logger,
	}

	if err := repo.Ping(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}

	return repo, nil
}

// Ping verifies database reachability within queryTimeout.
func (r *Repository) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, r.queryTimeout)
	defer cancel()

	if err := r.db.PingContext(ctx); err != nil {
		return fmt.Errorf("postgres ping: %w", err)
	}
	return nil
}

// Close closes the underlying database connection pool.
func (r *Repository) Close() error {
	if err := r.db.Close(); err != nil {
		return fmt.Errorf("close postgres pool: %w", err)
	}
	return nil
}

// withReadOnlyTx executes fn inside an explicit ReadOnly transaction bounded by queryTimeout
// and always rolls back on exit.
func (r *Repository) withReadOnlyTx(ctx context.Context, fn func(ctx context.Context, tx *sql.Tx) error) error {
	ctx, cancel := context.WithTimeout(ctx, r.queryTimeout)
	defer cancel()

	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("begin read-only tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	return fn(ctx, tx)
}

// ExecuteReadOnlyQuery runs a query inside a read-only transaction and caps results at maxRows.
func (r *Repository) ExecuteReadOnlyQuery(ctx context.Context, query string, maxRows int) (*db.QueryResult, error) {
	if maxRows <= 0 {
		maxRows = 1
	}

	start := time.Now()
	var (
		cols       []string
		resultRows [][]any
		truncated  bool
	)

	err := r.withReadOnlyTx(ctx, func(txCtx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(txCtx, query)
		if err != nil {
			return fmt.Errorf("execute query: %w", err)
		}
		defer rows.Close()

		cols, err = rows.Columns()
		if err != nil {
			return fmt.Errorf("read columns: %w", err)
		}

		resultRows = make([][]any, 0)
		for rows.Next() {
			if len(resultRows) == maxRows {
				truncated = true
				break
			}

			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				return fmt.Errorf("scan row: %w", err)
			}

			for i, v := range vals {
				vals[i] = normalizeValue(v)
			}
			resultRows = append(resultRows, vals)
		}

		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate rows: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return &db.QueryResult{
		Columns:         cols,
		Rows:            resultRows,
		RowCount:        len(resultRows),
		Truncated:       truncated,
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

// ListTables lists all base tables and views in schema with estimated row counts.
func (r *Repository) ListTables(ctx context.Context, schema string) ([]db.TableSummary, error) {
	if schema == "" {
		schema = "public"
	}

	const query = `
		SELECT
			t.table_name,
			t.table_type,
			COALESCE(GREATEST(c.reltuples, 0)::bigint, 0) AS estimated_rows
		FROM information_schema.tables t
		LEFT JOIN pg_catalog.pg_namespace n ON n.nspname = t.table_schema
		LEFT JOIN pg_catalog.pg_class c ON c.relnamespace = n.oid AND c.relname = t.table_name
		WHERE t.table_schema = $1
		  AND t.table_type IN ('BASE TABLE', 'VIEW')
		ORDER BY t.table_name`

	tables := make([]db.TableSummary, 0)
	err := r.withReadOnlyTx(ctx, func(txCtx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(txCtx, query, schema)
		if err != nil {
			return fmt.Errorf("list tables in schema %q: %w", schema, err)
		}
		defer rows.Close()

		for rows.Next() {
			var ts db.TableSummary
			if err := rows.Scan(&ts.Name, &ts.Type, &ts.EstimatedRows); err != nil {
				return fmt.Errorf("scan table summary: %w", err)
			}
			tables = append(tables, ts)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate table summaries: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return tables, nil
}

// DescribeTable returns columns, primary keys, foreign keys, and indexes for schema.table.
func (r *Repository) DescribeTable(ctx context.Context, schema, table string) (*db.TableSchema, error) {
	if schema == "" {
		schema = "public"
	}

	result := &db.TableSchema{
		Schema:      schema,
		Table:       table,
		Columns:     make([]db.ColumnInfo, 0),
		PrimaryKeys: make([]string, 0),
		ForeignKeys: make([]db.ForeignKeyInfo, 0),
		Indexes:     make([]db.IndexInfo, 0),
	}

	err := r.withReadOnlyTx(ctx, func(txCtx context.Context, tx *sql.Tx) error {
		var exists bool
		const existsQuery = `
			SELECT EXISTS (
				SELECT 1
				FROM information_schema.tables
				WHERE table_schema = $1
				  AND table_name = $2
			)`
		if err := tx.QueryRowContext(txCtx, existsQuery, schema, table).Scan(&exists); err != nil {
			return fmt.Errorf("check table existence: %w", err)
		}
		if !exists {
			return fmt.Errorf("%w: %s.%s", db.ErrTableNotFound, schema, table)
		}

		const colsQuery = `
			SELECT
				column_name,
				data_type,
				(is_nullable = 'YES') AS nullable,
				column_default
			FROM information_schema.columns
			WHERE table_schema = $1
			  AND table_name = $2
			ORDER BY ordinal_position`
		colRows, err := tx.QueryContext(txCtx, colsQuery, schema, table)
		if err != nil {
			return fmt.Errorf("query columns: %w", err)
		}
		defer colRows.Close()

		for colRows.Next() {
			var col db.ColumnInfo
			var def sql.NullString
			if err := colRows.Scan(&col.Name, &col.DataType, &col.Nullable, &def); err != nil {
				return fmt.Errorf("scan column: %w", err)
			}
			if def.Valid {
				v := def.String
				col.Default = &v
			}
			result.Columns = append(result.Columns, col)
		}
		if err := colRows.Err(); err != nil {
			return fmt.Errorf("iterate columns: %w", err)
		}

		const pkQuery = `
			SELECT a.attname
			FROM pg_catalog.pg_index i
			JOIN pg_catalog.pg_class c ON c.oid = i.indrelid
			JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
			JOIN pg_catalog.pg_attribute a ON a.attrelid = c.oid AND a.attnum = ANY(i.indkey)
			WHERE n.nspname = $1
			  AND c.relname = $2
			  AND i.indisprimary
			ORDER BY array_position(i.indkey, a.attnum)`
		pkRows, err := tx.QueryContext(txCtx, pkQuery, schema, table)
		if err != nil {
			return fmt.Errorf("query primary keys: %w", err)
		}
		defer pkRows.Close()

		for pkRows.Next() {
			var pk string
			if err := pkRows.Scan(&pk); err != nil {
				return fmt.Errorf("scan primary key: %w", err)
			}
			result.PrimaryKeys = append(result.PrimaryKeys, pk)
		}
		if err := pkRows.Err(); err != nil {
			return fmt.Errorf("iterate primary keys: %w", err)
		}

		const fkQuery = `
			SELECT
				a.attname AS column_name,
				fc.relname AS ref_table,
				fa.attname AS ref_column,
				con.conname AS constraint_name
			FROM pg_catalog.pg_constraint con
			JOIN pg_catalog.pg_class c ON c.oid = con.conrelid
			JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
			JOIN pg_catalog.pg_class fc ON fc.oid = con.confrelid
			CROSS JOIN LATERAL unnest(con.conkey, con.confkey) WITH ORDINALITY AS k(attnum, fattnum, ord)
			JOIN pg_catalog.pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.attnum
			JOIN pg_catalog.pg_attribute fa ON fa.attrelid = con.confrelid AND fa.attnum = k.fattnum
			WHERE n.nspname = $1
			  AND c.relname = $2
			  AND con.contype = 'f'
			ORDER BY con.conname, k.ord`
		fkRows, err := tx.QueryContext(txCtx, fkQuery, schema, table)
		if err != nil {
			return fmt.Errorf("query foreign keys: %w", err)
		}
		defer fkRows.Close()

		for fkRows.Next() {
			var fk db.ForeignKeyInfo
			if err := fkRows.Scan(&fk.Column, &fk.RefTable, &fk.RefColumn, &fk.ConstraintName); err != nil {
				return fmt.Errorf("scan foreign key: %w", err)
			}
			result.ForeignKeys = append(result.ForeignKeys, fk)
		}
		if err := fkRows.Err(); err != nil {
			return fmt.Errorf("iterate foreign keys: %w", err)
		}

		const idxQuery = `
			SELECT
				ic.relname AS index_name,
				i.indisunique AS is_unique,
				pg_catalog.pg_get_indexdef(ic.oid) AS definition,
				COALESCE(
					(
						SELECT string_agg(a.attname, ',' ORDER BY k.ord)
						FROM unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord)
						JOIN pg_catalog.pg_attribute a ON a.attrelid = c.oid AND a.attnum = k.attnum
						WHERE k.attnum > 0
					),
					''
				) AS columns_csv
			FROM pg_catalog.pg_index i
			JOIN pg_catalog.pg_class c ON c.oid = i.indrelid
			JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
			JOIN pg_catalog.pg_class ic ON ic.oid = i.indexrelid
			WHERE n.nspname = $1
			  AND c.relname = $2
			ORDER BY ic.relname`
		idxRows, err := tx.QueryContext(txCtx, idxQuery, schema, table)
		if err != nil {
			return fmt.Errorf("query indexes: %w", err)
		}
		defer idxRows.Close()

		for idxRows.Next() {
			var idx db.IndexInfo
			var colsCSV string
			if err := idxRows.Scan(&idx.Name, &idx.Unique, &idx.Definition, &colsCSV); err != nil {
				return fmt.Errorf("scan index: %w", err)
			}
			if colsCSV != "" {
				idx.Columns = strings.Split(colsCSV, ",")
			} else {
				idx.Columns = make([]string, 0)
			}
			result.Indexes = append(result.Indexes, idx)
		}
		if err := idxRows.Err(); err != nil {
			return fmt.Errorf("iterate indexes: %w", err)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ExplainQuery runs EXPLAIN (FORMAT JSON) or EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)
// inside a rolled-back read-only transaction.
func (r *Repository) ExplainQuery(ctx context.Context, query string, analyze bool) (*db.ExplainResult, error) {
	prefix := "EXPLAIN (FORMAT JSON) "
	if analyze {
		prefix = "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "
	}

	start := time.Now()
	var rawPlan []byte

	err := r.withReadOnlyTx(ctx, func(txCtx context.Context, tx *sql.Tx) error {
		if err := tx.QueryRowContext(txCtx, prefix+query).Scan(&rawPlan); err != nil {
			return fmt.Errorf("explain query: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return &db.ExplainResult{
		Plan:            json.RawMessage(rawPlan),
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

// normalizeValue converts database/sql scanned values into JSON-friendly primitives.
func normalizeValue(v any) any {
	switch val := v.(type) {
	case nil:
		return nil
	case []byte:
		if utf8.Valid(val) {
			return string(val)
		}
		return val
	case [16]byte:
		return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
			val[0:4], val[4:6], val[6:8], val[8:10], val[10:16])
	default:
		return val
	}
}
