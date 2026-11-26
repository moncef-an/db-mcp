package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

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
				if b, ok := v.([]byte); ok {
					vals[i] = string(b)
				}
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

// ListTables will be implemented in Phase 3.
func (r *Repository) ListTables(_ context.Context, _ string) ([]db.TableSummary, error) {
	return nil, errors.New("list_tables not implemented")
}

// DescribeTable will be implemented in Phase 3.
func (r *Repository) DescribeTable(_ context.Context, _, _ string) (*db.TableSchema, error) {
	return nil, errors.New("describe_table not implemented")
}

// ExplainQuery will be implemented in Phase 4.
func (r *Repository) ExplainQuery(_ context.Context, _ string, _ bool) (*db.ExplainResult, error) {
	return nil, errors.New("explain_query not implemented")
}
