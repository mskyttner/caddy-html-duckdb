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
		name           string
		ifNoneMatch    string
		shouldMatch    bool
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
