package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultQueryTimeout  = 5 * time.Second
	MaxQueryTimeout      = 30 * time.Second
	DefaultMaxRows       = 100
	HardMaxRows          = 1000
	DefaultRedactColumns = "password,password_hash,secret,token,api_key,access_key,ssn"
)

// Config holds validated runtime configuration for db-mcp.
type Config struct {
	DatabaseURL   string
	QueryTimeout  time.Duration
	MaxRows       int
	RedactColumns []string
	LogLevel      slog.Level
}

// LogValue implements slog.LogValuer to ensure DatabaseURL is never written to logs.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("database_url", "[REDACTED]"),
		slog.Duration("query_timeout", c.QueryTimeout),
		slog.Int("max_rows", c.MaxRows),
		slog.Any("redact_columns", c.RedactColumns),
		slog.String("log_level", c.LogLevel.String()),
	)
}

// Load parses configuration from environment variables and CLI flags.
// CLI flags take precedence over environment variables.
func Load(args []string, getenv func(string) string) (*Config, error) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}

	rawDatabaseURL := strings.TrimSpace(getenv("DATABASE_URL"))
	rawQueryTimeout := strings.TrimSpace(getenv("DB_QUERY_TIMEOUT"))
	rawMaxRows := strings.TrimSpace(getenv("DB_MAX_ROWS"))
	rawRedactColumns := getenv("DB_REDACT_COLUMNS")
	_, hasRedactEnv := lookupEnv(getenv, "DB_REDACT_COLUMNS")
	rawLogLevel := strings.TrimSpace(getenv("LOG_LEVEL"))

	fs := flag.NewFlagSet("db-mcp", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	flagDatabaseURL := fs.String("database-url", "", "PostgreSQL connection URI")
	flagQueryTimeout := fs.String("query-timeout", "", "Per-query execution timeout (max 30s)")
	flagMaxRows := fs.String("max-rows", "", "Default max rows returned by run_query (max 1000)")
	flagRedactColumns := fs.String("redact-columns", "", "Comma-separated column substrings to redact")
	flagLogLevel := fs.String("log-level", "", "Log level: debug, info, warn, error")

	if err := fs.Parse(args); err != nil {
		return nil, fmt.Errorf("parse flags: %w", err)
	}

	visited := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) {
		visited[f.Name] = true
	})

	if visited["database-url"] {
		rawDatabaseURL = strings.TrimSpace(*flagDatabaseURL)
	}
	if visited["query-timeout"] {
		rawQueryTimeout = strings.TrimSpace(*flagQueryTimeout)
	}
	if visited["max-rows"] {
		rawMaxRows = strings.TrimSpace(*flagMaxRows)
	}
	if visited["redact-columns"] {
		rawRedactColumns = *flagRedactColumns
		hasRedactEnv = true
	}
	if visited["log-level"] {
		rawLogLevel = strings.TrimSpace(*flagLogLevel)
	}

	if err := validateDatabaseURL(rawDatabaseURL); err != nil {
		return nil, err
	}

	queryTimeout := DefaultQueryTimeout
	if rawQueryTimeout != "" {
		d, err := time.ParseDuration(rawQueryTimeout)
		if err != nil {
			return nil, fmt.Errorf("invalid DB_QUERY_TIMEOUT %q: %w", rawQueryTimeout, err)
		}
		queryTimeout = d
	}
	if queryTimeout <= 0 || queryTimeout > MaxQueryTimeout {
		return nil, fmt.Errorf("DB_QUERY_TIMEOUT must be > 0 and <= %s, got %s", MaxQueryTimeout, queryTimeout)
	}

	maxRows := DefaultMaxRows
	if rawMaxRows != "" {
		n, err := strconv.Atoi(rawMaxRows)
		if err != nil {
			return nil, fmt.Errorf("invalid DB_MAX_ROWS %q: %w", rawMaxRows, err)
		}
		maxRows = n
	}
	if maxRows <= 0 || maxRows > HardMaxRows {
		return nil, fmt.Errorf("DB_MAX_ROWS must be > 0 and <= %d, got %d", HardMaxRows, maxRows)
	}

	redactSource := DefaultRedactColumns
	if hasRedactEnv {
		redactSource = rawRedactColumns
	}
	redactColumns := parseRedactColumns(redactSource)

	logLevel := slog.LevelInfo
	if rawLogLevel != "" {
		lvl, err := parseLogLevel(rawLogLevel)
		if err != nil {
			return nil, err
		}
		logLevel = lvl
	}

	return &Config{
		DatabaseURL:   rawDatabaseURL,
		QueryTimeout:  queryTimeout,
		MaxRows:       maxRows,
		RedactColumns: redactColumns,
		LogLevel:      logLevel,
	}, nil
}

func lookupEnv(getenv func(string) string, key string) (string, bool) {
	v := getenv(key)
	return v, v != ""
}

// RedactSecrets scrubs DatabaseURL and any embedded password from s so error
// messages logged to stderr or returned to MCP clients never leak credentials.
func (c *Config) RedactSecrets(s string) string {
	if c == nil || c.DatabaseURL == "" || s == "" {
		return s
	}
	out := strings.ReplaceAll(s, c.DatabaseURL, "[REDACTED]")
	if u, err := url.Parse(c.DatabaseURL); err == nil && u.User != nil {
		if pw, ok := u.User.Password(); ok && pw != "" {
			out = strings.ReplaceAll(out, pw, "[REDACTED]")
		}
	}
	return out
}

func validateDatabaseURL(raw string) error {
	if raw == "" {
		return errors.New("DATABASE_URL (or --database-url) is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			return fmt.Errorf("invalid DATABASE_URL: %v", urlErr.Err)
		}
		return errors.New("invalid DATABASE_URL: malformed URL")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "postgres" && scheme != "postgresql" {
		return fmt.Errorf("invalid DATABASE_URL scheme %q: must be postgres or postgresql", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("invalid DATABASE_URL: host is required")
	}
	return nil
}

func parseRedactColumns(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.ToLower(strings.TrimSpace(p))
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func parseLogLevel(raw string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid LOG_LEVEL %q: must be debug, info, warn, or error", raw)
	}
}
