package config

import (
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"
)

func envFromMap(m map[string]string) func(string) string {
	return func(k string) string {
		return m[k]
	}
}

func TestLoad_DefaultsAndEnv(t *testing.T) {
	cfg, err := Load(nil, envFromMap(map[string]string{
		"DATABASE_URL": "postgres://user:pass@localhost:5432/appdb?sslmode=disable",
	}))
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}

	if cfg.QueryTimeout != DefaultQueryTimeout {
		t.Errorf("QueryTimeout = %v, want %v", cfg.QueryTimeout, DefaultQueryTimeout)
	}
	if cfg.MaxRows != DefaultMaxRows {
		t.Errorf("MaxRows = %d, want %d", cfg.MaxRows, DefaultMaxRows)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want %v", cfg.LogLevel, slog.LevelInfo)
	}
	wantRedact := []string{"password", "password_hash", "secret", "token", "api_key", "access_key", "ssn"}
	if !reflect.DeepEqual(cfg.RedactColumns, wantRedact) {
		t.Errorf("RedactColumns = %v, want %v", cfg.RedactColumns, wantRedact)
	}
}

func TestLoad_FlagsOverrideEnv(t *testing.T) {
	env := envFromMap(map[string]string{
		"DATABASE_URL":      "postgres://envhost:5432/envdb",
		"DB_QUERY_TIMEOUT":  "10s",
		"DB_MAX_ROWS":       "50",
		"DB_REDACT_COLUMNS": "secret",
		"LOG_LEVEL":         "warn",
	})
	args := []string{
		"--database-url", "postgresql://flaghost:5432/flagdb",
		"--query-timeout", "15s",
		"--max-rows", "250",
		"--redact-columns", "Token, API_Key ",
		"--log-level", "debug",
	}

	cfg, err := Load(args, env)
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}

	if cfg.DatabaseURL != "postgresql://flaghost:5432/flagdb" {
		t.Errorf("DatabaseURL = %q, want flag value", cfg.DatabaseURL)
	}
	if cfg.QueryTimeout != 15*time.Second {
		t.Errorf("QueryTimeout = %v, want 15s", cfg.QueryTimeout)
	}
	if cfg.MaxRows != 250 {
		t.Errorf("MaxRows = %d, want 250", cfg.MaxRows)
	}
	wantRedact := []string{"token", "api_key"}
	if !reflect.DeepEqual(cfg.RedactColumns, wantRedact) {
		t.Errorf("RedactColumns = %v, want %v", cfg.RedactColumns, wantRedact)
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Errorf("LogLevel = %v, want debug", cfg.LogLevel)
	}
}

func TestLoad_ValidationErrors(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		args []string
	}{
		{
			name: "missing database url",
			env:  map[string]string{},
		},
		{
			name: "invalid scheme",
			env:  map[string]string{"DATABASE_URL": "mysql://localhost:3306/db"},
		},
		{
			name: "missing host",
			env:  map[string]string{"DATABASE_URL": "postgres:///db"},
		},
		{
			name: "query timeout exceeds hard cap",
			env: map[string]string{
				"DATABASE_URL":     "postgres://localhost:5432/db",
				"DB_QUERY_TIMEOUT": "31s",
			},
		},
		{
			name: "query timeout zero",
			env: map[string]string{
				"DATABASE_URL":     "postgres://localhost:5432/db",
				"DB_QUERY_TIMEOUT": "0s",
			},
		},
		{
			name: "max rows exceeds hard cap",
			env: map[string]string{
				"DATABASE_URL": "postgres://localhost:5432/db",
				"DB_MAX_ROWS":  "1001",
			},
		},
		{
			name: "max rows zero",
			env: map[string]string{
				"DATABASE_URL": "postgres://localhost:5432/db",
				"DB_MAX_ROWS":  "0",
			},
		},
		{
			name: "invalid log level",
			env: map[string]string{
				"DATABASE_URL": "postgres://localhost:5432/db",
				"LOG_LEVEL":    "trace",
			},
		},
		{
			name: "unknown cli flag",
			env:  map[string]string{"DATABASE_URL": "postgres://localhost:5432/db"},
			args: []string{"--nonexistent-flag"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Load(tc.args, envFromMap(tc.env)); err == nil {
				t.Fatalf("Load() expected error for %q, got nil", tc.name)
			}
		})
	}
}

func TestConfig_LogValueRedactsDatabaseURL(t *testing.T) {
	cfg := Config{
		DatabaseURL:  "postgres://admin:supersecret@db.internal:5432/prod",
		QueryTimeout: 5 * time.Second,
		MaxRows:      100,
		LogLevel:     slog.LevelInfo,
	}

	rendered := cfg.LogValue().String()
	if strings.Contains(rendered, "supersecret") || strings.Contains(rendered, "db.internal") {
		t.Fatalf("LogValue() leaked DatabaseURL: %s", rendered)
	}
	if !strings.Contains(rendered, "[REDACTED]") {
		t.Fatalf("LogValue() missing [REDACTED] marker: %s", rendered)
	}
}
