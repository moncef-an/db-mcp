package guard

import (
	"reflect"
	"strings"
	"testing"
)

func TestValidateReadOnlyQuery(t *testing.T) {
	tests := []struct {
		name    string
		sql     string
		wantErr bool
	}{
		// Valid queries
		{name: "simple select", sql: "SELECT 1", wantErr: false},
		{name: "select with trailing semicolon", sql: "SELECT id, email FROM users;", wantErr: false},
		{name: "select with trailing whitespace and comment", sql: "SELECT id FROM users; -- trailing comment\n", wantErr: false},
		{name: "valid CTE", sql: "WITH active AS (SELECT id FROM users WHERE active = true) SELECT * FROM active", wantErr: false},
		{name: "leading line comment", sql: "-- fetch users\nSELECT id FROM users", wantErr: false},
		{name: "leading nested block comment", sql: "/* outer /* inner */ outer */ SELECT id FROM users", wantErr: false},
		{name: "single quoted literal with blocked keyword", sql: "SELECT 'DROP TABLE users; DELETE FROM accounts;' AS msg", wantErr: false},
		{name: "escaped single quote in literal", sql: "SELECT 'it''s a DROP TABLE test' AS msg", wantErr: false},
		{name: "dollar quoted literal $$", sql: "SELECT $$DROP TABLE users; DELETE FROM users$$ AS msg", wantErr: false},
		{name: "tagged dollar quoted literal", sql: "SELECT $tag$INSERT INTO users VALUES (1); $other$ $tag$ AS msg", wantErr: false},
		{name: "double quoted identifier named delete", sql: `SELECT "delete", "update" FROM audit_log`, wantErr: false},
		{name: "keyword as substring in identifier", sql: "SELECT deleted_at, updated_at, created_by FROM users", wantErr: false},
		{name: "positional parameters", sql: "SELECT id FROM users WHERE id = $1 AND org_id = $2", wantErr: false},
		{name: "unicode dollar quoted literal", sql: "SELECT $é$DROP TABLE users; '$é$ AS msg", wantErr: false},
		{name: "unicode identifier with keyword substring", sql: "SELECT caféDELETE FROM users", wantErr: false},
		{name: "identifier containing dollar sign", sql: "SELECT col$x$ FROM users", wantErr: false},
		{name: "escape string literal E with escaped quote", sql: `SELECT E'it\'s ok; DROP TABLE users' AS msg`, wantErr: false},
		{name: "lowercase escape string literal e with backslash", sql: `SELECT e'back\\slash' AS msg`, wantErr: false},
		{name: "escape string literal with doubled quote", sql: `SELECT E'doubled''quote' AS msg`, wantErr: false},
		{name: "column named share", sql: "SELECT key, share FROM stocks", wantErr: false},
		{name: "deeply nested block comments", sql: "/* a /* b /* c */ b */ a */ SELECT 1", wantErr: false},
		{name: "unicode whitespace around select", sql: "\u00a0SELECT\u20031\u3000", wantErr: false},

		// Blocked queries
		{name: "empty string", sql: "", wantErr: true},
		{name: "whitespace only", sql: "   \n\t  ", wantErr: true},
		{name: "comment only", sql: "-- just a comment\n/* block */", wantErr: true},
		{name: "unterminated block comment", sql: "/* unterminated SELECT 1", wantErr: true},
		{name: "unterminated nested block comment", sql: "/* outer /* inner */ SELECT 1", wantErr: true},
		{name: "unterminated single quote", sql: "SELECT 'unterminated", wantErr: true},
		{name: "unterminated escape quote trailing backslash", sql: `SELECT E'unterminated\`, wantErr: true},
		{name: "unterminated dollar quote", sql: "SELECT $$unterminated", wantErr: true},
		{name: "leading explain rejected", sql: "EXPLAIN SELECT 1", wantErr: true},
		{name: "insert rejected", sql: "INSERT INTO users (name) VALUES ('alice')", wantErr: true},
		{name: "update rejected", sql: "UPDATE users SET name = 'bob'", wantErr: true},
		{name: "delete rejected", sql: "DELETE FROM users WHERE id = 1", wantErr: true},
		{name: "drop rejected", sql: "DROP TABLE users", wantErr: true},
		{name: "alter rejected", sql: "ALTER TABLE users ADD COLUMN age int", wantErr: true},
		{name: "truncate rejected", sql: "TRUNCATE TABLE users", wantErr: true},
		{name: "grant rejected", sql: "GRANT ALL ON users TO public", wantErr: true},
		{name: "revoke rejected", sql: "REVOKE ALL ON users FROM public", wantErr: true},
		{name: "create rejected", sql: "CREATE TABLE evil (id int)", wantErr: true},
		{name: "copy rejected", sql: "COPY users TO '/tmp/users.csv'", wantErr: true},
		{name: "call rejected", sql: "CALL my_proc()", wantErr: true},
		{name: "do block rejected", sql: "DO $$ BEGIN DELETE FROM users; END $$", wantErr: true},
		{name: "execute rejected", sql: "EXECUTE stmt", wantErr: true},
		{name: "set rejected", sql: "SET statement_timeout = 0", wantErr: true},
		{name: "lock rejected", sql: "LOCK TABLE users IN ACCESS EXCLUSIVE MODE", wantErr: true},
		{name: "multi-statement injection", sql: "SELECT 1; DROP TABLE users", wantErr: true},
		{name: "multi-statement two selects", sql: "SELECT 1; SELECT 2", wantErr: true},
		{name: "mutating CTE delete", sql: "WITH d AS (DELETE FROM users RETURNING *) SELECT * FROM d", wantErr: true},
		{name: "mutating CTE insert", sql: "WITH i AS (INSERT INTO users (name) VALUES ('x') RETURNING *) SELECT * FROM i", wantErr: true},
		{name: "mutating CTE update", sql: "WITH u AS (UPDATE users SET name = 'x' RETURNING *) SELECT * FROM u", wantErr: true},
		{name: "mutating CTE merge", sql: "WITH m AS (MERGE INTO users USING src ON true WHEN MATCHED THEN DELETE) SELECT * FROM m", wantErr: true},
		{name: "comment obfuscated delete", sql: "/* harmless */ DELETE FROM users", wantErr: true},
		{name: "comment between tokens in mutating CTE", sql: "WITH d AS (/*x*/ UPDATE users SET x = 1 RETURNING *) SELECT * FROM d", wantErr: true},
		{name: "unicode dollar tag desync bypass", sql: "SELECT $é$ ' $é$; DROP TABLE users; -- '", wantErr: true},
		{name: "identifier dollar tag desync bypass", sql: "SELECT col$x$ ; DROP TABLE users; -- $x$", wantErr: true},
		{name: "escape string backslash quote desync bypass", sql: `SELECT E'\''; DROP TABLE users; -- '`, wantErr: true},
		{name: "lowercase escape string backslash quote desync bypass", sql: `SELECT e'\''; DROP TABLE users; -- '`, wantErr: true},
		{name: "escape string escaped backslash desync bypass", sql: `SELECT E'\\'; DROP TABLE users; -- '`, wantErr: true},
		{name: "select into table creation", sql: "SELECT * INTO evil_table FROM users", wantErr: true},
		{name: "select into temp table creation", sql: "SELECT id INTO TEMP evil_table FROM users", wantErr: true},
		{name: "select for update", sql: "SELECT * FROM users FOR UPDATE", wantErr: true},
		{name: "select for no key update", sql: "SELECT * FROM users FOR NO KEY UPDATE", wantErr: true},
		{name: "select for share", sql: "SELECT * FROM users FOR SHARE", wantErr: true},
		{name: "select for key share", sql: "SELECT * FROM users FOR KEY SHARE", wantErr: true},
		{name: "select for share with comments", sql: "SELECT * FROM users FOR /*x*/ KEY /*y*/ SHARE", wantErr: true},
		{name: "unicode whitespace leading delete", sql: "\u00a0DELETE FROM users", wantErr: true},
		{name: "unicode whitespace multi-statement injection", sql: "SELECT 1;\u2003DROP TABLE users", wantErr: true},
		{name: "ideographic space multi-statement injection", sql: "SELECT 1;\u3000DELETE FROM users", wantErr: true},

		// NUL bytes, CR line-comment termination, and size limits
		{name: "nul byte in query", sql: "SELECT 1\x00", wantErr: true},
		{name: "nul byte in literal", sql: "SELECT '\x00'", wantErr: true},
		{name: "nul byte before semicolon", sql: "SELECT 1\x00; DROP TABLE users", wantErr: true},
		{name: "carriage return terminates line comment", sql: "SELECT 1 -- comment\r; DROP TABLE users", wantErr: true},
		{name: "query exceeds max length", sql: "SELECT '" + strings.Repeat("a", maxQueryBytes) + "'", wantErr: true},

		// Non-SELECT/WITH PostgreSQL commands
		{name: "values statement rejected", sql: "VALUES (1, 2)", wantErr: true},
		{name: "table statement rejected", sql: "TABLE users", wantErr: true},
		{name: "prepare statement rejected", sql: "PREPARE p AS SELECT 1", wantErr: true},
		{name: "deallocate statement rejected", sql: "DEALLOCATE ALL", wantErr: true},
		{name: "listen statement rejected", sql: "LISTEN events", wantErr: true},
		{name: "notify statement rejected", sql: "NOTIFY events, 'hi'", wantErr: true},
		{name: "discard statement rejected", sql: "DISCARD ALL", wantErr: true},
		{name: "reset statement rejected", sql: "RESET ALL", wantErr: true},
		{name: "vacuum statement rejected", sql: "VACUUM users", wantErr: true},
		{name: "analyze statement rejected", sql: "ANALYZE users", wantErr: true},
		{name: "cluster statement rejected", sql: "CLUSTER users", wantErr: true},
		{name: "reindex statement rejected", sql: "REINDEX TABLE users", wantErr: true},
		{name: "refresh materialized view rejected", sql: "REFRESH MATERIALIZED VIEW mv_users", wantErr: true},
		{name: "security label rejected", sql: "SECURITY LABEL ON TABLE users IS 'secret'", wantErr: true},

		// Dangerous PostgreSQL built-in functions (unquoted, schema-qualified, double-quoted)
		{name: "set_config rejected", sql: "SELECT set_config('statement_timeout', '0', false)", wantErr: true},
		{name: "double quoted set_config rejected", sql: `SELECT "set_config"('statement_timeout', '0', false)`, wantErr: true},
		{name: "schema qualified set_config rejected", sql: "SELECT pg_catalog.set_config('statement_timeout', '0', false)", wantErr: true},
		{name: "pg_read_file rejected", sql: "SELECT pg_read_file('/etc/passwd')", wantErr: true},
		{name: "double quoted pg_read_file rejected", sql: `SELECT "pg_read_file"('/etc/passwd')`, wantErr: true},
		{name: "pg_read_binary_file rejected", sql: "SELECT pg_read_binary_file('/etc/passwd')", wantErr: true},
		{name: "pg_ls_dir rejected", sql: "SELECT pg_ls_dir('/etc')", wantErr: true},
		{name: "pg_stat_file rejected", sql: "SELECT pg_stat_file('/etc/passwd')", wantErr: true},
		{name: "lo_import rejected", sql: "SELECT lo_import('/etc/passwd')", wantErr: true},
		{name: "lo_export rejected", sql: "SELECT lo_export(1234, '/tmp/out')", wantErr: true},
		{name: "pg_terminate_backend rejected", sql: "SELECT pg_terminate_backend(1234)", wantErr: true},
		{name: "pg_cancel_backend rejected", sql: "SELECT pg_cancel_backend(1234)", wantErr: true},
		{name: "pg_reload_conf rejected", sql: "SELECT pg_reload_conf()", wantErr: true},
		{name: "dblink rejected", sql: "SELECT * FROM dblink('host=127.0.0.1', 'DELETE FROM users') AS t(x int)", wantErr: true},
		{name: "dblink_exec rejected", sql: "SELECT dblink_exec('host=127.0.0.1', 'DELETE FROM users')", wantErr: true},
		{name: "dangerous function in CTE rejected", sql: "WITH x AS (SELECT pg_read_file('/etc/passwd') AS f) SELECT * FROM x", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateReadOnlyQuery(tc.sql)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateReadOnlyQuery(%q) error = %v, wantErr = %v", tc.sql, err, tc.wantErr)
			}
		})
	}
}

func TestClampLimit(t *testing.T) {
	tests := []struct {
		name      string
		requested int
		maxRows   int
		want      int
	}{
		{name: "zero defaults to maxRows", requested: 0, maxRows: 100, want: 100},
		{name: "negative defaults to maxRows", requested: -5, maxRows: 100, want: 100},
		{name: "within bounds", requested: 25, maxRows: 100, want: 25},
		{name: "equal to maxRows", requested: 100, maxRows: 100, want: 100},
		{name: "exceeds maxRows clamped", requested: 500, maxRows: 100, want: 100},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClampLimit(tc.requested, tc.maxRows); got != tc.want {
				t.Fatalf("ClampLimit(%d, %d) = %d, want %d", tc.requested, tc.maxRows, got, tc.want)
			}
		})
	}
}

func TestRedactRows(t *testing.T) {
	columns := []string{"id", "email", "password_hash", "User_API_Key", "ssn"}
	rows := [][]any{
		{1, "alice@example.com", "hash123", "key-abc", "123-45-6789"},
		{2, "bob@example.com", nil, "key-xyz", "987-65-4321"},
	}
	patterns := []string{"password", "password_hash", "secret", "token", "api_key", "access_key", "ssn"}

	RedactRows(columns, rows, patterns)

	want := [][]any{
		{1, "alice@example.com", "[REDACTED]", "[REDACTED]", "[REDACTED]"},
		{2, "bob@example.com", "[REDACTED]", "[REDACTED]", "[REDACTED]"},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("RedactRows() = %#v, want %#v", rows, want)
	}
}

func TestRedactRowsEdgeCases(t *testing.T) {
	t.Run("case-insensitive pattern and whitespace trimming", func(t *testing.T) {
		columns := []string{"id", "SECRET_TOKEN", "email"}
		rows := [][]any{{1, "tok-123", "a@b.com"}}
		RedactRows(columns, rows, []string{"  TOKEN  ", "", "   "})
		want := [][]any{{1, "[REDACTED]", "a@b.com"}}
		if !reflect.DeepEqual(rows, want) {
			t.Fatalf("got %#v, want %#v", rows, want)
		}
	})

	t.Run("slice safety with nil, short, and long rows", func(t *testing.T) {
		columns := []string{"id", "password"}
		rows := [][]any{
			nil,
			{1},
			{2, "secret", "extra"},
		}
		RedactRows(columns, rows, []string{"password"})
		want := [][]any{
			nil,
			{1},
			{2, "[REDACTED]", "extra"},
		}
		if !reflect.DeepEqual(rows, want) {
			t.Fatalf("got %#v, want %#v", rows, want)
		}
	})

	t.Run("empty inputs do not panic", func(t *testing.T) {
		RedactRows(nil, nil, nil)
		RedactRows([]string{"password"}, nil, []string{"password"})
		RedactRows(nil, [][]any{{"x"}}, []string{"password"})
	})
}
