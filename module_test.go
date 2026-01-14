package caddyhtmlduckdb

import (
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	_ "github.com/duckdb/duckdb-go/v2"
	"go.uber.org/zap"
)

func TestGenerateETag(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "simple content",
			content: "<html><body>Hello</body></html>",
			want:    `"` + md5Hash("<html><body>Hello</body></html>") + `"`,
		},
		{
			name:    "empty content",
			content: "",
			want:    `"` + md5Hash("") + `"`,
		},
		{
			name:    "content with unicode",
			content: "<html><body>Héllo Wörld</body></html>",
			want:    `"` + md5Hash("<html><body>Héllo Wörld</body></html>") + `"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash := md5.Sum([]byte(tt.content))
			etag := `"` + hex.EncodeToString(hash[:]) + `"`
			if etag != tt.want {
				t.Errorf("generateETag() = %v, want %v", etag, tt.want)
			}
		})
	}
}

func TestIfNoneMatchParsing(t *testing.T) {
	targetETag := `"abc123"`

	tests := []struct {
		name        string
		ifNoneMatch string
		shouldMatch bool
	}{
		{
			name:        "exact match",
			ifNoneMatch: `"abc123"`,
			shouldMatch: true,
		},
		{
			name:        "no match",
			ifNoneMatch: `"different"`,
			shouldMatch: false,
		},
		{
			name:        "wildcard",
			ifNoneMatch: `*`,
			shouldMatch: true,
		},
		{
			name:        "multiple ETags - match first",
			ifNoneMatch: `"abc123", "def456", "ghi789"`,
			shouldMatch: true,
		},
		{
			name:        "multiple ETags - match middle",
			ifNoneMatch: `"xxx", "abc123", "yyy"`,
			shouldMatch: true,
		},
		{
			name:        "multiple ETags - match last",
			ifNoneMatch: `"xxx", "yyy", "abc123"`,
			shouldMatch: true,
		},
		{
			name:        "multiple ETags - no match",
			ifNoneMatch: `"xxx", "yyy", "zzz"`,
			shouldMatch: false,
		},
		{
			name:        "empty header",
			ifNoneMatch: "",
			shouldMatch: false,
		},
		{
			name:        "whitespace around ETags",
			ifNoneMatch: `  "abc123"  `,
			shouldMatch: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matched := etagMatches(tt.ifNoneMatch, targetETag)
			if matched != tt.shouldMatch {
				t.Errorf("etagMatches(%q, %q) = %v, want %v",
					tt.ifNoneMatch, targetETag, matched, tt.shouldMatch)
			}
		})
	}
}

// etagMatches checks if the If-None-Match header matches the given ETag.
// This mirrors the logic in ServeHTTP for testing purposes.
func etagMatches(ifNoneMatch, etag string) bool {
	if ifNoneMatch == "" {
		return false
	}
	if ifNoneMatch == "*" {
		return true
	}
	for _, m := range strings.Split(ifNoneMatch, ",") {
		if strings.TrimSpace(m) == etag {
			return true
		}
	}
	return false
}

func md5Hash(s string) string {
	hash := md5.Sum([]byte(s))
	return hex.EncodeToString(hash[:])
}

func TestSanitizeIdentifier(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "simple identifier",
			input: "html",
			want:  "html",
		},
		{
			name:  "with underscore",
			input: "html_column",
			want:  "html_column",
		},
		{
			name:  "with numbers",
			input: "column1",
			want:  "column1",
		},
		{
			name:  "SQL injection attempt",
			input: "html; DROP TABLE users;--",
			want:  "htmlDROPTABLEusers",
		},
		{
			name:  "special characters",
			input: "col-name.test",
			want:  "colnametest",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeIdentifier(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeIdentifier(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestServeHTTP_ETag(t *testing.T) {
	// Create in-memory DuckDB database with test data
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	// Create test table
	_, err = db.Exec(`CREATE TABLE html (id VARCHAR, html VARCHAR)`)
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}

	testHTML := "<html><body>Test Content</body></html>"
	_, err = db.Exec(`INSERT INTO html VALUES ('test-id', ?)`, testHTML)
	if err != nil {
		t.Fatalf("failed to insert test data: %v", err)
	}

	// Calculate expected ETag
	expectedHash := md5.Sum([]byte(testHTML))
	expectedETag := `"` + hex.EncodeToString(expectedHash[:]) + `"`

	// Create handler with nop logger for tests
	handler := &HTMLFromDuckDB{
		Table:      "html",
		HTMLColumn: "html",
		IDColumn:   "id",
		db:         db,
		logger:     zap.NewNop(),
	}

	t.Run("returns ETag header", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/page/test-id", nil)
		rec := httptest.NewRecorder()

		err := handler.ServeHTTP(rec, req, emptyNextHandler())
		if err != nil {
			t.Fatalf("ServeHTTP error: %v", err)
		}

		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
		}

		etag := rec.Header().Get("ETag")
		if etag != expectedETag {
			t.Errorf("ETag = %q, want %q", etag, expectedETag)
		}

		contentLength := rec.Header().Get("Content-Length")
		if contentLength == "" {
			t.Error("Content-Length header missing")
		}
	})

	t.Run("returns 304 for matching ETag", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/page/test-id", nil)
		req.Header.Set("If-None-Match", expectedETag)
		rec := httptest.NewRecorder()

		err := handler.ServeHTTP(rec, req, emptyNextHandler())
		if err != nil {
			t.Fatalf("ServeHTTP error: %v", err)
		}

		if rec.Code != http.StatusNotModified {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusNotModified)
		}

		if rec.Body.Len() != 0 {
			t.Errorf("body should be empty for 304, got %d bytes", rec.Body.Len())
		}
	})

	t.Run("returns 304 for wildcard", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/page/test-id", nil)
		req.Header.Set("If-None-Match", "*")
		rec := httptest.NewRecorder()

		err := handler.ServeHTTP(rec, req, emptyNextHandler())
		if err != nil {
			t.Fatalf("ServeHTTP error: %v", err)
		}

		if rec.Code != http.StatusNotModified {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusNotModified)
		}
	})

	t.Run("returns 304 for multiple ETags with match", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/page/test-id", nil)
		req.Header.Set("If-None-Match", `"wrong1", `+expectedETag+`, "wrong2"`)
		rec := httptest.NewRecorder()

		err := handler.ServeHTTP(rec, req, emptyNextHandler())
		if err != nil {
			t.Fatalf("ServeHTTP error: %v", err)
		}

		if rec.Code != http.StatusNotModified {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusNotModified)
		}
	})

	t.Run("returns 200 for non-matching ETag", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/page/test-id", nil)
		req.Header.Set("If-None-Match", `"non-matching-etag"`)
		rec := httptest.NewRecorder()

		err := handler.ServeHTTP(rec, req, emptyNextHandler())
		if err != nil {
			t.Fatalf("ServeHTTP error: %v", err)
		}

		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
		}
	})
}

func emptyNextHandler() caddyhttp.Handler {
	return caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		return nil
	})
}

func TestServeHTTP_IndexRouting(t *testing.T) {
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	// Create test table and mock index macro
	_, err = db.Exec(`CREATE TABLE html (id VARCHAR, html VARCHAR)`)
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}

	// Create a simple mock macro that returns HTML
	_, err = db.Exec(`
		CREATE OR REPLACE MACRO render_index(page := 1, base_path := '') AS TABLE
		SELECT '<html>Index Page ' || page || '</html>' AS html
	`)
	if err != nil {
		t.Fatalf("failed to create mock macro: %v", err)
	}

	handler := &HTMLFromDuckDB{
		Table:        "html",
		HTMLColumn:   "html",
		IDColumn:     "id",
		IndexEnabled: true,
		IndexMacro:   "render_index",
		SearchParam:  "q",
		db:           db,
		logger:       zap.NewNop(),
	}

	t.Run("serves index page when no ID and index enabled", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/works/", nil)
		rec := httptest.NewRecorder()

		err := handler.ServeHTTP(rec, req, emptyNextHandler())
		if err != nil {
			t.Fatalf("ServeHTTP error: %v", err)
		}

		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
		}

		body := rec.Body.String()
		if !strings.Contains(body, "Index Page") {
			t.Errorf("body should contain 'Index Page', got %q", body)
		}
	})

	t.Run("serves index page with page parameter", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/works/?page=2", nil)
		rec := httptest.NewRecorder()

		err := handler.ServeHTTP(rec, req, emptyNextHandler())
		if err != nil {
			t.Fatalf("ServeHTTP error: %v", err)
		}

		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
		}

		body := rec.Body.String()
		if !strings.Contains(body, "Index Page 2") {
			t.Errorf("body should contain 'Index Page 2', got %q", body)
		}
	})
}

func TestServeHTTP_SearchRouting(t *testing.T) {
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	// Create test table and mock search macro
	_, err = db.Exec(`CREATE TABLE html (id VARCHAR, html VARCHAR)`)
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}

	// Create a simple mock macro that returns search results
	_, err = db.Exec(`
		CREATE OR REPLACE MACRO render_search(term := '', base_path := '') AS TABLE
		SELECT '<ul>Results for: ' || term || '</ul>' AS html
	`)
	if err != nil {
		t.Fatalf("failed to create mock macro: %v", err)
	}

	handler := &HTMLFromDuckDB{
		Table:         "html",
		HTMLColumn:    "html",
		IDColumn:      "id",
		SearchEnabled: true,
		SearchMacro:   "render_search",
		SearchParam:   "q",
		db:            db,
		logger:        zap.NewNop(),
	}

	t.Run("serves search results when search param present", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/works/?q=test", nil)
		rec := httptest.NewRecorder()

		err := handler.ServeHTTP(rec, req, emptyNextHandler())
		if err != nil {
			t.Fatalf("ServeHTTP error: %v", err)
		}

		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
		}

		body := rec.Body.String()
		if !strings.Contains(body, "Results for: test") {
			t.Errorf("body should contain 'Results for: test', got %q", body)
		}

		// Search results should have no-cache header
		cacheControl := rec.Header().Get("Cache-Control")
		if cacheControl != "no-cache" {
			t.Errorf("Cache-Control = %q, want 'no-cache'", cacheControl)
		}
	})

	t.Run("truncates long search queries", func(t *testing.T) {
		longQuery := strings.Repeat("a", 250)
		req := httptest.NewRequest(http.MethodGet, "/works/?q="+longQuery, nil)
		rec := httptest.NewRecorder()

		err := handler.ServeHTTP(rec, req, emptyNextHandler())
		if err != nil {
			t.Fatalf("ServeHTTP error: %v", err)
		}

		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
		}

		// The query should be truncated to 200 chars
		body := rec.Body.String()
		if strings.Contains(body, longQuery) {
			t.Error("body should not contain full long query (should be truncated)")
		}
	})
}

func TestParseSQLStatements(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "simple statements",
			input:    "SELECT 1; SELECT 2;",
			expected: []string{"SELECT 1", "SELECT 2"},
		},
		{
			name:     "multiline statement",
			input:    "CREATE TABLE foo (\n  id INT,\n  name VARCHAR\n);",
			expected: []string{"CREATE TABLE foo (\n  id INT,\n  name VARCHAR\n)"},
		},
		{
			name:     "single line comment",
			input:    "SELECT 1; -- this is a comment\nSELECT 2;",
			expected: []string{"SELECT 1", "SELECT 2"},
		},
		{
			name:     "block comment",
			input:    "SELECT /* inline comment */ 1; SELECT 2;",
			expected: []string{"SELECT   1", "SELECT 2"},
		},
		{
			name:     "multiline block comment",
			input:    "SELECT 1;\n/* this is\na multiline\ncomment */\nSELECT 2;",
			expected: []string{"SELECT 1", "SELECT 2"},
		},
		{
			name:     "semicolon in single quoted string",
			input:    "SELECT 'hello; world'; SELECT 2;",
			expected: []string{"SELECT 'hello; world'", "SELECT 2"},
		},
		{
			name:     "semicolon in double quoted string",
			input:    `SELECT "hello; world"; SELECT 2;`,
			expected: []string{`SELECT "hello; world"`, "SELECT 2"},
		},
		{
			name:     "complex multiline with comments and strings",
			input:    "-- Load extensions\nLOAD tera;\n/* Configure\n   settings */\nSET search_path = 'my;path';\nSELECT 1;",
			expected: []string{"LOAD tera", "SET search_path = 'my;path'", "SELECT 1"},
		},
		{
			name:     "no trailing semicolon",
			input:    "SELECT 1; SELECT 2",
			expected: []string{"SELECT 1", "SELECT 2"},
		},
		{
			name:     "empty input",
			input:    "",
			expected: []string{},
		},
		{
			name:     "only comments",
			input:    "-- just a comment\n/* another comment */",
			expected: []string{},
		},
		{
			name: "DuckDB macro with multiline",
			input: `CREATE OR REPLACE MACRO render_index(page := 1) AS TABLE
SELECT html FROM (
    SELECT '<html>Page ' || page || '</html>' AS html
);`,
			expected: []string{`CREATE OR REPLACE MACRO render_index(page := 1) AS TABLE
SELECT html FROM (
    SELECT '<html>Page ' || page || '</html>' AS html
)`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseSQLStatements(tt.input)

			if len(got) != len(tt.expected) {
				t.Errorf("parseSQLStatements() returned %d statements, want %d\ngot: %v\nwant: %v",
					len(got), len(tt.expected), got, tt.expected)
				return
			}

			for i := range got {
				// Normalize whitespace for comparison
				gotNorm := strings.Join(strings.Fields(got[i]), " ")
				expNorm := strings.Join(strings.Fields(tt.expected[i]), " ")
				if gotNorm != expNorm {
					t.Errorf("statement %d mismatch:\ngot:  %q\nwant: %q", i, got[i], tt.expected[i])
				}
			}
		})
	}
}

func TestEscapeSQLString(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "no quotes",
			input: "hello world",
			want:  "hello world",
		},
		{
			name:  "single quote",
			input: "it's a test",
			want:  "it''s a test",
		},
		{
			name:  "multiple quotes",
			input: "it's Bob's test",
			want:  "it''s Bob''s test",
		},
		{
			name:  "SQL injection attempt",
			input: "'; DROP TABLE users; --",
			want:  "''; DROP TABLE users; --",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := escapeSQLString(tt.input)
			if got != tt.want {
				t.Errorf("escapeSQLString(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestTruncateForLog(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{
			name:   "short string unchanged",
			input:  "SELECT 1",
			maxLen: 100,
			want:   "SELECT 1",
		},
		{
			name:   "long string truncated",
			input:  "SELECT * FROM very_long_table_name WHERE condition = 'value'",
			maxLen: 20,
			want:   "SELECT * FROM very_l...",
		},
		{
			name:   "normalizes whitespace",
			input:  "SELECT\n  *\n  FROM\n  table",
			maxLen: 100,
			want:   "SELECT * FROM table",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateForLog(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncateForLog() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestServeHTTP_RecordMacro(t *testing.T) {
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	// Create a source data table (not pre-rendered HTML)
	_, err = db.Exec(`CREATE TABLE publications (pid VARCHAR, title VARCHAR, abstract VARCHAR)`)
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}

	_, err = db.Exec(`INSERT INTO publications VALUES ('12345', 'Test Publication', 'This is an abstract.')`)
	if err != nil {
		t.Fatalf("failed to insert test data: %v", err)
	}

	// Create a record macro that renders HTML on-the-fly
	_, err = db.Exec(`
		CREATE OR REPLACE MACRO render_record(id := '') AS TABLE
		SELECT '<html><h1>' || title || '</h1><p>' || abstract || '</p></html>' AS html
		FROM publications
		WHERE pid = id
	`)
	if err != nil {
		t.Fatalf("failed to create render_record macro: %v", err)
	}

	handler := &HTMLFromDuckDB{
		RecordMacro: "render_record",
		HTMLColumn:  "html",
		db:          db,
		logger:      zap.NewNop(),
	}

	t.Run("serves content via record macro", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/works/12345", nil)
		rec := httptest.NewRecorder()

		err := handler.ServeHTTP(rec, req, emptyNextHandler())
		if err != nil {
			t.Fatalf("ServeHTTP error: %v", err)
		}

		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
		}

		body := rec.Body.String()
		if !strings.Contains(body, "Test Publication") {
			t.Errorf("body should contain 'Test Publication', got %q", body)
		}
		if !strings.Contains(body, "This is an abstract") {
			t.Errorf("body should contain 'This is an abstract', got %q", body)
		}
	})

	t.Run("returns 404 for non-existent record", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/works/nonexistent", nil)
		rec := httptest.NewRecorder()

		err := handler.ServeHTTP(rec, req, emptyNextHandler())
		if err == nil {
			t.Fatal("expected error for non-existent record")
		}

		// The error should be a 404
		httpErr, ok := err.(caddyhttp.HandlerError)
		if !ok {
			t.Fatalf("expected caddyhttp.HandlerError, got %T", err)
		}
		if httpErr.StatusCode != http.StatusNotFound {
			t.Errorf("status = %d, want %d", httpErr.StatusCode, http.StatusNotFound)
		}
	})

	t.Run("handles special characters in ID", func(t *testing.T) {
		// Insert a record with a special ID
		_, err = db.Exec(`INSERT INTO publications VALUES ('test''s-id', 'Special Title', 'Special abstract.')`)
		if err != nil {
			t.Fatalf("failed to insert test data: %v", err)
		}

		req := httptest.NewRequest(http.MethodGet, "/works/test's-id", nil)
		rec := httptest.NewRecorder()

		err := handler.ServeHTTP(rec, req, emptyNextHandler())
		if err != nil {
			t.Fatalf("ServeHTTP error: %v", err)
		}

		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
		}

		body := rec.Body.String()
		if !strings.Contains(body, "Special Title") {
			t.Errorf("body should contain 'Special Title', got %q", body)
		}
	})

	t.Run("ETag works with record macro", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/works/12345", nil)
		rec := httptest.NewRecorder()

		err := handler.ServeHTTP(rec, req, emptyNextHandler())
		if err != nil {
			t.Fatalf("ServeHTTP error: %v", err)
		}

		etag := rec.Header().Get("ETag")
		if etag == "" {
			t.Error("ETag header missing")
		}

		// Make second request with If-None-Match
		req2 := httptest.NewRequest(http.MethodGet, "/works/12345", nil)
		req2.Header.Set("If-None-Match", etag)
		rec2 := httptest.NewRecorder()

		err = handler.ServeHTTP(rec2, req2, emptyNextHandler())
		if err != nil {
			t.Fatalf("ServeHTTP error: %v", err)
		}

		if rec2.Code != http.StatusNotModified {
			t.Errorf("status = %d, want %d", rec2.Code, http.StatusNotModified)
		}
	})
}

func TestServeHTTP_Health(t *testing.T) {
	// Create in-memory DuckDB database with test data
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	// Create test table
	_, err = db.Exec(`CREATE TABLE html (id VARCHAR, html VARCHAR)`)
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}

	// Create test macros
	_, err = db.Exec(`
		CREATE OR REPLACE MACRO render_index(page := 1, base_path := '') AS TABLE
		SELECT '<html>Index Page ' || page || '</html>' AS html
	`)
	if err != nil {
		t.Fatalf("failed to create index macro: %v", err)
	}

	_, err = db.Exec(`
		CREATE OR REPLACE MACRO render_search(term := '', base_path := '') AS TABLE
		SELECT '<html>Search: ' || term || '</html>' AS html
	`)
	if err != nil {
		t.Fatalf("failed to create search macro: %v", err)
	}

	t.Run("returns healthy status when all checks pass", func(t *testing.T) {
		handler := &HTMLFromDuckDB{
			db:            db,
			Table:         "html",
			HTMLColumn:    "html",
			IDColumn:      "id",
			HealthEnabled: true,
			HealthPath:    "_health",
			logger:        zap.NewNop(),
		}

		req := httptest.NewRequest(http.MethodGet, "/_health", nil)
		rec := httptest.NewRecorder()

		err := handler.ServeHTTP(rec, req, emptyNextHandler())
		if err != nil {
			t.Fatalf("ServeHTTP error: %v", err)
		}

		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
		}

		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want %q", ct, "application/json")
		}

		body := rec.Body.String()
		if !strings.Contains(body, `"status":"healthy"`) {
			t.Errorf("response should contain healthy status, got %q", body)
		}
		if !strings.Contains(body, `"database"`) {
			t.Errorf("response should contain database check, got %q", body)
		}
		if !strings.Contains(body, `"table"`) {
			t.Errorf("response should contain table check, got %q", body)
		}
	})

	t.Run("includes macro checks when enabled", func(t *testing.T) {
		handler := &HTMLFromDuckDB{
			db:            db,
			Table:         "html",
			HTMLColumn:    "html",
			IDColumn:      "id",
			HealthEnabled: true,
			HealthPath:    "_health",
			IndexEnabled:  true,
			IndexMacro:    "render_index",
			SearchEnabled: true,
			SearchMacro:   "render_search",
			logger:        zap.NewNop(),
		}

		req := httptest.NewRequest(http.MethodGet, "/_health", nil)
		rec := httptest.NewRecorder()

		err := handler.ServeHTTP(rec, req, emptyNextHandler())
		if err != nil {
			t.Fatalf("ServeHTTP error: %v", err)
		}

		body := rec.Body.String()
		if !strings.Contains(body, `"index_macro"`) {
			t.Errorf("response should contain index_macro check, got %q", body)
		}
		if !strings.Contains(body, `"search_macro"`) {
			t.Errorf("response should contain search_macro check, got %q", body)
		}
	})

	t.Run("returns unhealthy when macro missing", func(t *testing.T) {
		handler := &HTMLFromDuckDB{
			db:            db,
			Table:         "html",
			HTMLColumn:    "html",
			IDColumn:      "id",
			HealthEnabled: true,
			HealthPath:    "_health",
			IndexEnabled:  true,
			IndexMacro:    "nonexistent_macro",
			logger:        zap.NewNop(),
		}

		req := httptest.NewRequest(http.MethodGet, "/_health", nil)
		rec := httptest.NewRecorder()

		err := handler.ServeHTTP(rec, req, emptyNextHandler())
		if err != nil {
			t.Fatalf("ServeHTTP error: %v", err)
		}

		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
		}

		body := rec.Body.String()
		if !strings.Contains(body, `"status":"unhealthy"`) {
			t.Errorf("response should contain unhealthy status, got %q", body)
		}
		if !strings.Contains(body, `"macro not found"`) {
			t.Errorf("response should contain error message, got %q", body)
		}
	})

	t.Run("includes pool stats when detailed enabled", func(t *testing.T) {
		handler := &HTMLFromDuckDB{
			db:             db,
			Table:          "html",
			HTMLColumn:     "html",
			IDColumn:       "id",
			HealthEnabled:  true,
			HealthPath:     "_health",
			HealthDetailed: true,
			logger:         zap.NewNop(),
		}

		req := httptest.NewRequest(http.MethodGet, "/_health", nil)
		rec := httptest.NewRecorder()

		err := handler.ServeHTTP(rec, req, emptyNextHandler())
		if err != nil {
			t.Fatalf("ServeHTTP error: %v", err)
		}

		body := rec.Body.String()
		if !strings.Contains(body, `"pool"`) {
			t.Errorf("response should contain pool stats, got %q", body)
		}
		if !strings.Contains(body, `"open_connections"`) {
			t.Errorf("response should contain open_connections, got %q", body)
		}
	})

	t.Run("respects base_path for health endpoint", func(t *testing.T) {
		handler := &HTMLFromDuckDB{
			db:            db,
			Table:         "html",
			HTMLColumn:    "html",
			IDColumn:      "id",
			BasePath:      "/works",
			HealthEnabled: true,
			HealthPath:    "_health",
			logger:        zap.NewNop(),
		}

		// Request without base_path should not match
		req := httptest.NewRequest(http.MethodGet, "/_health", nil)
		rec := httptest.NewRecorder()

		err := handler.ServeHTTP(rec, req, emptyNextHandler())
		// This should return error since /_health doesn't match /works/_health
		if err == nil {
			t.Error("expected error for non-matching health path")
		}

		// Request with base_path should match
		req2 := httptest.NewRequest(http.MethodGet, "/works/_health", nil)
		rec2 := httptest.NewRecorder()

		err = handler.ServeHTTP(rec2, req2, emptyNextHandler())
		if err != nil {
			t.Fatalf("ServeHTTP error: %v", err)
		}

		if rec2.Code != http.StatusOK {
			t.Errorf("status = %d, want %d", rec2.Code, http.StatusOK)
		}
	})

	t.Run("does not serve health when disabled", func(t *testing.T) {
		handler := &HTMLFromDuckDB{
			db:            db,
			Table:         "html",
			HTMLColumn:    "html",
			IDColumn:      "id",
			HealthEnabled: false,
			HealthPath:    "_health",
			logger:        zap.NewNop(),
		}

		req := httptest.NewRequest(http.MethodGet, "/_health", nil)
		rec := httptest.NewRecorder()

		err := handler.ServeHTTP(rec, req, emptyNextHandler())
		// Should return error (400 Bad Request for missing ID) since health is disabled
		if err == nil {
			t.Error("expected error when health is disabled")
		}
	})
}

func TestServeHTTP_TableMacro(t *testing.T) {
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	// Create test table
	_, err = db.Exec(`CREATE TABLE html (id VARCHAR, html VARCHAR)`)
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}

	// Create a table macro that returns multiple columns
	_, err = db.Exec(`
		CREATE OR REPLACE MACRO render_chart(max_items := 10, base_path := '') AS TABLE
		SELECT
			'Item ' || i as name,
			i * 10 as value,
			repeat('█', i) as chart
		FROM range(1, max_items + 1) t(i)
	`)
	if err != nil {
		t.Fatalf("failed to create table macro: %v", err)
	}

	handler := &HTMLFromDuckDB{
		Table:      "html",
		HTMLColumn: "html",
		IDColumn:   "id",
		TableMacro: "render_chart",
		TablePath:  "_chart",
		db:         db,
		logger:     zap.NewNop(),
	}

	t.Run("serves table from macro", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/_chart", nil)
		rec := httptest.NewRecorder()

		err := handler.ServeHTTP(rec, req, emptyNextHandler())
		if err != nil {
			t.Fatalf("ServeHTTP error: %v", err)
		}

		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
		}

		body := rec.Body.String()
		if !strings.Contains(body, `<pre class="duckbox">`) {
			t.Errorf("body should contain <pre class=\"duckbox\">, got %q", body)
		}
		if !strings.Contains(body, "name") {
			t.Errorf("body should contain column name 'name', got %q", body)
		}
		if !strings.Contains(body, "value") {
			t.Errorf("body should contain column name 'value', got %q", body)
		}
		if !strings.Contains(body, "Item 1") {
			t.Errorf("body should contain 'Item 1', got %q", body)
		}
	})

	t.Run("passes query params to macro", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/_chart?max_items=3", nil)
		rec := httptest.NewRecorder()

		err := handler.ServeHTTP(rec, req, emptyNextHandler())
		if err != nil {
			t.Fatalf("ServeHTTP error: %v", err)
		}

		body := rec.Body.String()
		// With max_items=3, should have Item 1, 2, 3 but not Item 4
		if !strings.Contains(body, "Item 3") {
			t.Errorf("body should contain 'Item 3', got %q", body)
		}
		if strings.Contains(body, "Item 4") {
			t.Errorf("body should NOT contain 'Item 4' with max_items=3, got %q", body)
		}
	})

	t.Run("sets correct headers", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/_chart", nil)
		rec := httptest.NewRecorder()

		err := handler.ServeHTTP(rec, req, emptyNextHandler())
		if err != nil {
			t.Fatalf("ServeHTTP error: %v", err)
		}

		if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
			t.Errorf("Content-Type = %q, want %q", ct, "text/html; charset=utf-8")
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("Cache-Control = %q, want %q", cc, "no-cache")
		}
	})

	t.Run("respects base_path for table endpoint", func(t *testing.T) {
		handlerWithBase := &HTMLFromDuckDB{
			Table:      "html",
			HTMLColumn: "html",
			IDColumn:   "id",
			TableMacro: "render_chart",
			TablePath:  "_chart",
			BasePath:   "/works",
			db:         db,
			logger:     zap.NewNop(),
		}

		// Request without base_path should not match
		req := httptest.NewRequest(http.MethodGet, "/_chart", nil)
		rec := httptest.NewRecorder()

		err := handlerWithBase.ServeHTTP(rec, req, emptyNextHandler())
		// Should return error since /_chart doesn't match /works/_chart
		if err == nil {
			t.Error("expected error for non-matching table path")
		}

		// Request with base_path should match
		req2 := httptest.NewRequest(http.MethodGet, "/works/_chart", nil)
		rec2 := httptest.NewRecorder()

		err = handlerWithBase.ServeHTTP(rec2, req2, emptyNextHandler())
		if err != nil {
			t.Fatalf("ServeHTTP error: %v", err)
		}

		if rec2.Code != http.StatusOK {
			t.Errorf("status = %d, want %d", rec2.Code, http.StatusOK)
		}
	})
}

func TestServeHTTP_TableMacro_Alignment(t *testing.T) {
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	// Create test table
	_, err = db.Exec(`CREATE TABLE html (id VARCHAR, html VARCHAR)`)
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}

	// Create a macro with mixed types
	_, err = db.Exec(`
		CREATE OR REPLACE MACRO test_types(base_path := '') AS TABLE
		SELECT
			'text' as string_col,
			42 as int_col,
			3.14 as float_col
	`)
	if err != nil {
		t.Fatalf("failed to create macro: %v", err)
	}

	handler := &HTMLFromDuckDB{
		Table:      "html",
		HTMLColumn: "html",
		IDColumn:   "id",
		TableMacro: "test_types",
		TablePath:  "_types",
		db:         db,
		logger:     zap.NewNop(),
	}

	t.Run("formats table with correct structure", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/_types", nil)
		rec := httptest.NewRecorder()

		err := handler.ServeHTTP(rec, req, emptyNextHandler())
		if err != nil {
			t.Fatalf("ServeHTTP error: %v", err)
		}

		body := rec.Body.String()
		// Should contain all column names
		if !strings.Contains(body, "string_col") {
			t.Errorf("body should contain 'string_col', got %q", body)
		}
		if !strings.Contains(body, "int_col") {
			t.Errorf("body should contain 'int_col', got %q", body)
		}
		if !strings.Contains(body, "float_col") {
			t.Errorf("body should contain 'float_col', got %q", body)
		}
		// Should contain values
		if !strings.Contains(body, "text") {
			t.Errorf("body should contain 'text', got %q", body)
		}
		if !strings.Contains(body, "42") {
			t.Errorf("body should contain '42', got %q", body)
		}
	})
}

func TestServeHTTP_TableMacro_Health(t *testing.T) {
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	// Create test table
	_, err = db.Exec(`CREATE TABLE html (id VARCHAR, html VARCHAR)`)
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}

	// Create a table macro
	_, err = db.Exec(`
		CREATE OR REPLACE MACRO render_chart(max_items := 10, base_path := '') AS TABLE
		SELECT 'test' as name, 1 as value
	`)
	if err != nil {
		t.Fatalf("failed to create macro: %v", err)
	}

	t.Run("includes table_macro in health check", func(t *testing.T) {
		handler := &HTMLFromDuckDB{
			Table:         "html",
			HTMLColumn:    "html",
			IDColumn:      "id",
			TableMacro:    "render_chart",
			TablePath:     "_chart",
			HealthEnabled: true,
			HealthPath:    "_health",
			db:            db,
			logger:        zap.NewNop(),
		}

		req := httptest.NewRequest(http.MethodGet, "/_health", nil)
		rec := httptest.NewRecorder()

		err := handler.ServeHTTP(rec, req, emptyNextHandler())
		if err != nil {
			t.Fatalf("ServeHTTP error: %v", err)
		}

		body := rec.Body.String()
		if !strings.Contains(body, `"table_macro"`) {
			t.Errorf("response should contain table_macro check, got %q", body)
		}
		if !strings.Contains(body, `"render_chart"`) {
			t.Errorf("response should contain macro name, got %q", body)
		}
	})

	t.Run("returns unhealthy when table_macro missing", func(t *testing.T) {
		handler := &HTMLFromDuckDB{
			Table:         "html",
			HTMLColumn:    "html",
			IDColumn:      "id",
			TableMacro:    "nonexistent_macro",
			TablePath:     "_chart",
			HealthEnabled: true,
			HealthPath:    "_health",
			db:            db,
			logger:        zap.NewNop(),
		}

		req := httptest.NewRequest(http.MethodGet, "/_health", nil)
		rec := httptest.NewRecorder()

		err := handler.ServeHTTP(rec, req, emptyNextHandler())
		if err != nil {
			t.Fatalf("ServeHTTP error: %v", err)
		}

		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
		}

		body := rec.Body.String()
		if !strings.Contains(body, `"status":"unhealthy"`) {
			t.Errorf("response should contain unhealthy status, got %q", body)
		}
	})
}
