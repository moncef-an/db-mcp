package guard

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	redactedValue = "[REDACTED]"
	maxQueryBytes = 64 * 1024
)

var blockedKeywords = map[string]struct{}{
	"INSERT":   {},
	"UPDATE":   {},
	"DELETE":   {},
	"DROP":     {},
	"ALTER":    {},
	"TRUNCATE": {},
	"GRANT":    {},
	"REVOKE":   {},
	"CREATE":   {},
	"COPY":     {},
	"CALL":     {},
	"DO":       {},
	"EXECUTE":  {},
	"SET":      {},
	"LOCK":     {},
	"INTO":     {},
	"MERGE":    {},
}

var blockedFunctions = map[string]struct{}{
	"SET_CONFIG":           {},
	"PG_READ_FILE":         {},
	"PG_READ_BINARY_FILE":  {},
	"PG_LS_DIR":            {},
	"PG_STAT_FILE":         {},
	"LO_IMPORT":            {},
	"LO_EXPORT":            {},
	"LO_UNLINK":            {},
	"PG_TERMINATE_BACKEND": {},
	"PG_CANCEL_BACKEND":    {},
	"PG_RELOAD_CONF":       {},
	"PG_ROTATE_LOGFILE":    {},
	"DBLINK":               {},
	"DBLINK_EXEC":          {},
	"DBLINK_CONNECT":       {},
	"DBLINK_CONNECT_U":     {},
	"DBLINK_SEND_QUERY":    {},
	"DBLINK_GET_RESULTS":   {},
}

// ValidateReadOnlyQuery ensures sql is a single, non-empty SELECT or WITH statement
// containing no mutating keywords or dangerous functions outside string literals.
func ValidateReadOnlyQuery(sql string) error {
	if len(sql) > maxQueryBytes {
		return fmt.Errorf("query exceeds maximum allowed length of %d bytes", maxQueryBytes)
	}
	if strings.IndexByte(sql, 0) >= 0 {
		return errors.New("NUL bytes are not allowed in query")
	}

	s := &sqlScanner{src: sql}
	var firstWord string
	var prevPrevWord, prevWord string
	sawSemicolon := false

	for s.pos < len(s.src) {
		if err := s.skipSpaceAndComments(); err != nil {
			return err
		}
		if s.pos >= len(s.src) {
			break
		}

		if sawSemicolon {
			return errors.New("multi-statement queries are not allowed")
		}

		r, size := utf8.DecodeRuneInString(s.src[s.pos:])
		switch {
		case r == ';':
			sawSemicolon = true
			prevPrevWord, prevWord = "", ""
			s.pos += size
		case r == '\'':
			prevPrevWord, prevWord = "", ""
			if err := s.skipSingleQuoted(); err != nil {
				return err
			}
		case r == '"':
			prevPrevWord, prevWord = "", ""
			ident, err := s.readDoubleQuoted()
			if err != nil {
				return err
			}
			if _, blocked := blockedFunctions[strings.ToUpper(ident)]; blocked {
				return fmt.Errorf("privileged or dangerous function %q is not allowed", ident)
			}
		case r == '$':
			prevPrevWord, prevWord = "", ""
			if tag, ok := s.matchDollarTag(); ok {
				if err := s.skipDollarQuoted(tag); err != nil {
					return err
				}
			} else {
				s.pos += size
			}
		case isWordStart(r):
			word := strings.ToUpper(s.readWord())
			if firstWord == "" {
				firstWord = word
				if firstWord != "SELECT" && firstWord != "WITH" {
					return fmt.Errorf("only SELECT and WITH statements are allowed, got %q", firstWord)
				}
			}
			if _, blocked := blockedKeywords[word]; blocked {
				return fmt.Errorf("mutating or privileged keyword %q is not allowed", word)
			}
			if _, blocked := blockedFunctions[word]; blocked {
				return fmt.Errorf("privileged or dangerous function %q is not allowed", word)
			}
			if (prevWord == "FOR" && word == "SHARE") || (prevPrevWord == "FOR" && prevWord == "KEY" && word == "SHARE") {
				return errors.New("locking clause FOR SHARE / FOR KEY SHARE is not allowed")
			}
			// PostgreSQL C-style escape string literal E'...' / e'...': backslash escapes must be honored
			// so \' cannot desync quote tracking.
			if word == "E" && s.pos < len(s.src) && s.src[s.pos] == '\'' {
				prevPrevWord, prevWord = "", ""
				if err := s.skipEscapeSingleQuoted(); err != nil {
					return err
				}
				continue
			}
			prevPrevWord, prevWord = prevWord, word
		default:
			prevPrevWord, prevWord = "", ""
			s.pos += size
		}
	}

	if firstWord == "" {
		return errors.New("query is empty")
	}
	return nil
}

// ClampLimit bounds a requested row limit to [1, maxRows], defaulting to maxRows when requested <= 0.
func ClampLimit(requested, maxRows int) int {
	if maxRows <= 0 {
		return 1
	}
	if requested <= 0 || requested > maxRows {
		return maxRows
	}
	return requested
}

// RedactRows replaces cell values in-place with "[REDACTED]" for any column whose
// lowercase name contains one of the configured redact patterns.
func RedactRows(columns []string, rows [][]any, patterns []string) {
	if len(columns) == 0 || len(rows) == 0 || len(patterns) == 0 {
		return
	}

	redactCol := make([]bool, len(columns))
	anyMatched := false
	for i, col := range columns {
		lower := strings.ToLower(col)
		for _, rawPat := range patterns {
			pat := strings.ToLower(strings.TrimSpace(rawPat))
			if pat != "" && strings.Contains(lower, pat) {
				redactCol[i] = true
				anyMatched = true
				break
			}
		}
	}
	if !anyMatched {
		return
	}

	for _, row := range rows {
		for i := 0; i < len(row) && i < len(redactCol); i++ {
			if redactCol[i] {
				row[i] = redactedValue
			}
		}
	}
}

type sqlScanner struct {
	src string
	pos int
}

func (s *sqlScanner) skipSpaceAndComments() error {
	for s.pos < len(s.src) {
		r, size := utf8.DecodeRuneInString(s.src[s.pos:])
		if unicode.IsSpace(r) {
			s.pos += size
			continue
		}
		if strings.HasPrefix(s.src[s.pos:], "--") {
			s.pos += 2
			for s.pos < len(s.src) && s.src[s.pos] != '\n' && s.src[s.pos] != '\r' {
				s.pos++
			}
			continue
		}
		if strings.HasPrefix(s.src[s.pos:], "/*") {
			if err := s.skipBlockComment(); err != nil {
				return err
			}
			continue
		}
		break
	}
	return nil
}

func (s *sqlScanner) skipBlockComment() error {
	s.pos += 2
	depth := 1
	for s.pos < len(s.src) && depth > 0 {
		if strings.HasPrefix(s.src[s.pos:], "/*") {
			depth++
			s.pos += 2
			continue
		}
		if strings.HasPrefix(s.src[s.pos:], "*/") {
			depth--
			s.pos += 2
			continue
		}
		s.pos++
	}
	if depth > 0 {
		return errors.New("unterminated block comment")
	}
	return nil
}

func (s *sqlScanner) skipSingleQuoted() error {
	s.pos++ // opening '
	for s.pos < len(s.src) {
		if s.src[s.pos] == '\'' {
			s.pos++
			if s.pos < len(s.src) && s.src[s.pos] == '\'' {
				s.pos++ // escaped ''
				continue
			}
			return nil
		}
		s.pos++
	}
	return errors.New("unterminated string literal")
}

func (s *sqlScanner) skipEscapeSingleQuoted() error {
	s.pos++ // opening '
	for s.pos < len(s.src) {
		ch := s.src[s.pos]
		if ch == '\\' {
			s.pos++ // skip backslash
			if s.pos >= len(s.src) {
				return errors.New("unterminated string literal")
			}
			_, size := utf8.DecodeRuneInString(s.src[s.pos:])
			s.pos += size
			continue
		}
		if ch == '\'' {
			s.pos++
			if s.pos < len(s.src) && s.src[s.pos] == '\'' {
				s.pos++ // escaped ''
				continue
			}
			return nil
		}
		s.pos++
	}
	return errors.New("unterminated string literal")
}

func (s *sqlScanner) readDoubleQuoted() (string, error) {
	s.pos++ // opening "
	var b strings.Builder
	for s.pos < len(s.src) {
		if s.src[s.pos] == '"' {
			s.pos++
			if s.pos < len(s.src) && s.src[s.pos] == '"' {
				b.WriteByte('"')
				s.pos++ // escaped ""
				continue
			}
			return b.String(), nil
		}
		b.WriteByte(s.src[s.pos])
		s.pos++
	}
	return "", errors.New("unterminated quoted identifier")
}

func (s *sqlScanner) matchDollarTag() (string, bool) {
	if s.pos >= len(s.src) || s.src[s.pos] != '$' {
		return "", false
	}
	i := s.pos + 1
	if i < len(s.src) && s.src[i] == '$' {
		return "$$", true
	}
	if i >= len(s.src) {
		return "", false
	}
	r, size := utf8.DecodeRuneInString(s.src[i:])
	if !isDollarTagStart(r) {
		return "", false
	}
	i += size
	for i < len(s.src) {
		if s.src[i] == '$' {
			return s.src[s.pos : i+1], true
		}
		r, size := utf8.DecodeRuneInString(s.src[i:])
		if !isDollarTagPart(r) {
			return "", false
		}
		i += size
	}
	return "", false
}

func (s *sqlScanner) skipDollarQuoted(tag string) error {
	s.pos += len(tag)
	idx := strings.Index(s.src[s.pos:], tag)
	if idx < 0 {
		return fmt.Errorf("unterminated dollar-quoted string %s", tag)
	}
	s.pos += idx + len(tag)
	return nil
}

func (s *sqlScanner) readWord() string {
	start := s.pos
	for s.pos < len(s.src) {
		r, size := utf8.DecodeRuneInString(s.src[s.pos:])
		if !isWordPart(r) {
			break
		}
		s.pos += size
	}
	return s.src[start:s.pos]
}

func isDollarTagStart(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_' || (r >= 0x80 && r != utf8.RuneError && !unicode.IsSpace(r))
}

func isDollarTagPart(r rune) bool {
	return isDollarTagStart(r) || (r >= '0' && r <= '9')
}

func isWordStart(r rune) bool {
	return isDollarTagStart(r)
}

func isWordPart(r rune) bool {
	return isWordStart(r) || (r >= '0' && r <= '9') || r == '$'
}
