// Package caddyhtmlduckdb provides a Caddy HTTP handler that serves HTML content from DuckDB tables.
package caddyhtmlduckdb

import (
	"bufio"
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	_ "github.com/duckdb/duckdb-go/v2"
	"go.uber.org/zap"
)

func init() {
	caddy.RegisterModule(HTMLFromDuckDB{})
	httpcaddyfile.RegisterHandlerDirective("html_from_duckdb", parseHTMLFromDuckDB)
	httpcaddyfile.RegisterDirectiveOrder("html_from_duckdb", httpcaddyfile.After, "file_server")
}

// parseHTMLFromDuckDB unmarshals Caddyfile tokens into a handler.
func parseHTMLFromDuckDB(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	var m HTMLFromDuckDB
	// Let your existing UnmarshalCaddyfile do the work
	err := m.UnmarshalCaddyfile(h.Dispenser)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// HTMLFromDuckDB is a Caddy HTTP handler that serves HTML content from a DuckDB table.
type HTMLFromDuckDB struct {
	// DatabasePath is the path to the DuckDB database file.
	// Use ":memory:" for in-memory database.
	DatabasePath string `json:"database_path,omitempty"`

	// Table is the name of the table containing HTML content.
	Table string `json:"table"`

	// HTMLColumn is the name of the column containing HTML content.
	// Default: "html"
	HTMLColumn string `json:"html_column,omitempty"`

	// IDColumn is the name of the ID column to match against.
	// Default: "id"
	IDColumn string `json:"id_column,omitempty"`

	// IDParam is the URL parameter name to extract the ID from.
	// If not set, the ID is extracted from the URL path.
	// Default: extracts from path (e.g., /page/123 -> 123)
	IDParam string `json:"id_param,omitempty"`

	// WhereClause allows additional SQL WHERE conditions.
	// The ID condition is always added automatically.
	// Example: "status = 'published' AND deleted_at IS NULL"
	WhereClause string `json:"where_clause,omitempty"`

	// NotFoundRedirect is an optional URL to redirect to when content is not found.
	// If not set, returns 404 status.
	NotFoundRedirect string `json:"not_found_redirect,omitempty"`

	// CacheControl sets the Cache-Control header for successful responses.
	// Example: "public, max-age=3600"
	CacheControl string `json:"cache_control,omitempty"`

	// ReadOnly opens the database in read-only mode.
	// Default: true
	ReadOnly *bool `json:"read_only,omitempty"`

	// ConnectionPoolSize sets the maximum number of open connections.
	// Default: 10
	ConnectionPoolSize int `json:"connection_pool_size,omitempty"`

	// QueryTimeout sets the maximum time for query execution.
	// Default: 5s
	QueryTimeout string `json:"query_timeout,omitempty"`

	// IndexEnabled enables serving an index page when no ID is provided.
	// The index is rendered by calling a DuckDB table macro.
	// Default: false
	IndexEnabled bool `json:"index_enabled,omitempty"`

	// IndexMacro is the name of the DuckDB table macro that renders the index page.
	// The macro should accept (page, base_path) parameters and return a single html column.
	// Default: "render_index"
	IndexMacro string `json:"index_macro,omitempty"`

	// SearchEnabled enables a search endpoint using a DuckDB table macro.
	// Default: false
	SearchEnabled bool `json:"search_enabled,omitempty"`

	// SearchMacro is the name of the DuckDB table macro that renders search results.
	// The macro should accept (term, base_path) parameters and return a single html column.
	// Default: "render_search"
	SearchMacro string `json:"search_macro,omitempty"`

	// SearchParam is the query parameter name for search terms.
	// Default: "q"
	SearchParam string `json:"search_param,omitempty"`

	// BasePath is the base URL path for generating links in index and search results.
	// If not set, it's derived from the route.
	BasePath string `json:"base_path,omitempty"`

	// InitSQLFile is the path to a SQL file containing initialization commands.
	// Commands are executed after opening the database connection.
	// Useful for loading extensions (LOAD tera;) and setting configuration.
	// Supports multiline statements, single-line (--) and block (/* */) comments.
	InitSQLFile string `json:"init_sql_file,omitempty"`

	db      *sql.DB
	timeout time.Duration
	logger  *zap.Logger
}

// CaddyModule returns the Caddy module information.
func (HTMLFromDuckDB) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers.html_from_duckdb",
		New: func() caddy.Module { return new(HTMLFromDuckDB) },
	}
}

// Provision sets up the handler.
func (h *HTMLFromDuckDB) Provision(ctx caddy.Context) error {
	h.logger = ctx.Logger(h)

	// Set defaults
	if h.HTMLColumn == "" {
		h.HTMLColumn = "html"
	}
	if h.IDColumn == "" {
		h.IDColumn = "id"
	}
	if h.ReadOnly == nil {
		readOnly := true
		h.ReadOnly = &readOnly
	}
	if h.ConnectionPoolSize == 0 {
		h.ConnectionPoolSize = 10
	}
	if h.QueryTimeout == "" {
		h.QueryTimeout = "5s"
	}
	if h.IndexMacro == "" {
		h.IndexMacro = "render_index"
	}
	if h.SearchMacro == "" {
		h.SearchMacro = "render_search"
	}
	if h.SearchParam == "" {
		h.SearchParam = "q"
	}

	// Parse timeout
	var err error
	h.timeout, err = time.ParseDuration(h.QueryTimeout)
	if err != nil {
		return fmt.Errorf("invalid query_timeout: %v", err)
	}

	// Validate required fields
	if h.Table == "" {
		return fmt.Errorf("table name is required")
	}

	// Build connection string
	connStr := h.DatabasePath
	if connStr == "" {
		connStr = ":memory:"
	}

	// Add connection parameters
	params := []string{}
	if *h.ReadOnly {
		params = append(params, "access_mode=READ_ONLY")
	}
	if len(params) > 0 {
		connStr += "?" + strings.Join(params, "&")
	}

	// Open database connection
	h.db, err = sql.Open("duckdb", connStr)
	if err != nil {
		return fmt.Errorf("failed to open database: %v", err)
	}

	// Configure connection pool
	h.db.SetMaxOpenConns(h.ConnectionPoolSize)
	h.db.SetMaxIdleConns(h.ConnectionPoolSize / 2)
	h.db.SetConnMaxLifetime(time.Hour)

	// Test connection
	if err := h.db.Ping(); err != nil {
		h.db.Close()
		return fmt.Errorf("failed to ping database: %v", err)
	}

	// Execute init SQL file if specified
	if h.InitSQLFile != "" {
		if err := h.executeInitSQL(); err != nil {
			h.db.Close()
			return fmt.Errorf("failed to execute init SQL file: %v", err)
		}
	}

	h.logger.Info("HTML from DuckDB handler provisioned",
		zap.String("database", connStr),
		zap.String("table", h.Table),
		zap.Bool("read_only", *h.ReadOnly),
		zap.Bool("index_enabled", h.IndexEnabled),
		zap.Bool("search_enabled", h.SearchEnabled))

	return nil
}

// Cleanup closes the database connection.
func (h *HTMLFromDuckDB) Cleanup() error {
	if h.db != nil {
		return h.db.Close()
	}
	return nil
}

// executeInitSQL reads and executes SQL statements from the init file.
func (h *HTMLFromDuckDB) executeInitSQL() error {
	file, err := os.Open(h.InitSQLFile)
	if err != nil {
		return fmt.Errorf("failed to open init SQL file %s: %v", h.InitSQLFile, err)
	}
	defer file.Close()

	h.logger.Info("executing init SQL file", zap.String("file", h.InitSQLFile))

	// Read entire file
	var content strings.Builder
	scanner := bufio.NewScanner(file)
	// Increase buffer size for large files
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)
	for scanner.Scan() {
		content.WriteString(scanner.Text())
		content.WriteString("\n")
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("failed to read init SQL file: %v", err)
	}

	// Parse and execute statements
	statements := parseSQLStatements(content.String())
	for i, stmt := range statements {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}

		h.logger.Debug("executing init SQL statement",
			zap.Int("index", i+1),
			zap.String("statement", truncateForLog(stmt, 100)))

		if _, err := h.db.Exec(stmt); err != nil {
			return fmt.Errorf("failed to execute statement %d: %v\nStatement: %s", i+1, err, truncateForLog(stmt, 200))
		}
	}

	h.logger.Info("init SQL file executed successfully",
		zap.String("file", h.InitSQLFile),
		zap.Int("statements", len(statements)))

	return nil
}

// parseSQLStatements splits SQL content into individual statements.
// Handles multiline statements, string literals, and both comment styles.
func parseSQLStatements(content string) []string {
	var statements []string
	var current strings.Builder
	var inSingleQuote, inDoubleQuote, inLineComment, inBlockComment bool
	var prev rune

	for i, r := range content {
		// Handle line comments (--)
		if !inSingleQuote && !inDoubleQuote && !inBlockComment && r == '-' && prev == '-' {
			inLineComment = true
			// Remove the first '-' we already added
			s := current.String()
			if len(s) > 0 {
				current.Reset()
				current.WriteString(s[:len(s)-1])
			}
			prev = r
			continue
		}

		// End line comment on newline
		if inLineComment && r == '\n' {
			inLineComment = false
			current.WriteRune(' ') // Replace comment with space
			prev = r
			continue
		}

		// Skip chars in line comment
		if inLineComment {
			prev = r
			continue
		}

		// Handle block comments (/* */)
		if !inSingleQuote && !inDoubleQuote && !inBlockComment && r == '*' && prev == '/' {
			inBlockComment = true
			// Remove the '/' we already added
			s := current.String()
			if len(s) > 0 {
				current.Reset()
				current.WriteString(s[:len(s)-1])
			}
			prev = r
			continue
		}

		// End block comment
		if inBlockComment && r == '/' && prev == '*' {
			inBlockComment = false
			current.WriteRune(' ') // Replace comment with space
			prev = r
			continue
		}

		// Skip chars in block comment
		if inBlockComment {
			prev = r
			continue
		}

		// Handle string literals
		if r == '\'' && !inDoubleQuote && prev != '\\' {
			inSingleQuote = !inSingleQuote
		}
		if r == '"' && !inSingleQuote && prev != '\\' {
			inDoubleQuote = !inDoubleQuote
		}

		// Statement terminator
		if r == ';' && !inSingleQuote && !inDoubleQuote {
			stmt := strings.TrimSpace(current.String())
			if stmt != "" {
				statements = append(statements, stmt)
			}
			current.Reset()
			prev = r
			continue
		}

		current.WriteRune(r)
		prev = r

		// Peek ahead for -- and /* detection
		_ = i
	}

	// Add final statement if no trailing semicolon
	if stmt := strings.TrimSpace(current.String()); stmt != "" {
		statements = append(statements, stmt)
	}

	return statements
}

// truncateForLog truncates a string for logging purposes.
func truncateForLog(s string, maxLen int) string {
	// Normalize whitespace for logging
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// ServeHTTP serves HTML content from DuckDB.
func (h *HTMLFromDuckDB) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	// Check for search query first
	searchQuery := r.URL.Query().Get(h.SearchParam)
	if searchQuery != "" && h.SearchEnabled {
		return h.serveSearch(w, r, searchQuery)
	}

	// Extract ID from URL
	var id string
	if h.IDParam != "" {
		// Get from query parameter
		id = r.URL.Query().Get(h.IDParam)
	} else {
		// Get from path (last segment)
		// If path ends with /, treat as index request (no ID)
		if !strings.HasSuffix(r.URL.Path, "/") {
			parts := strings.Split(r.URL.Path, "/")
			if len(parts) > 0 {
				id = parts[len(parts)-1]
			}
		}
	}

	// If no ID and index is enabled, serve index page
	if id == "" && h.IndexEnabled {
		page := r.URL.Query().Get("page")
		return h.serveIndex(w, r, page)
	}

	if id == "" {
		return caddyhttp.Error(http.StatusBadRequest, fmt.Errorf("missing ID parameter"))
	}

	// Build query
	query := fmt.Sprintf("SELECT %s FROM %s WHERE %s = ?",
		sanitizeIdentifier(h.HTMLColumn),
		sanitizeIdentifier(h.Table),
		sanitizeIdentifier(h.IDColumn))

	if h.WhereClause != "" {
		query += fmt.Sprintf(" AND (%s)", h.WhereClause)
	}

	h.logger.Debug("executing query",
		zap.String("query", query),
		zap.String("id", id))

	// Execute query with timeout
	ctx := r.Context()
	if h.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, h.timeout)
		defer cancel()
	}

	var html string
	err := h.db.QueryRowContext(ctx, query, id).Scan(&html)
	if err != nil {
		if err == sql.ErrNoRows {
			h.logger.Debug("content not found", zap.String("id", id))
			if h.NotFoundRedirect != "" {
				http.Redirect(w, r, h.NotFoundRedirect, http.StatusFound)
				return nil
			}
			return caddyhttp.Error(http.StatusNotFound, fmt.Errorf("content not found"))
		}
		h.logger.Error("query failed", zap.Error(err))
		return caddyhttp.Error(http.StatusInternalServerError, err)
	}

	// Generate ETag from content hash
	hash := md5.Sum([]byte(html))
	etag := `"` + hex.EncodeToString(hash[:]) + `"`

	// Check If-None-Match header for conditional requests (RFC 7232)
	if match := r.Header.Get("If-None-Match"); match != "" {
		if match == "*" {
			w.WriteHeader(http.StatusNotModified)
			return nil
		}
		// Handle multiple ETags: "etag1", "etag2", "etag3"
		for _, m := range strings.Split(match, ",") {
			if strings.TrimSpace(m) == etag {
				w.WriteHeader(http.StatusNotModified)
				return nil
			}
		}
	}

	// Set headers
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(html)))
	w.Header().Set("ETag", etag)
	if h.CacheControl != "" {
		w.Header().Set("Cache-Control", h.CacheControl)
	}

	// Write HTML
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte(html)); err != nil {
		h.logger.Error("failed to write response", zap.Error(err))
		return err
	}

	h.logger.Debug("served HTML content",
		zap.String("id", id),
		zap.Int("size", len(html)))

	return nil
}

// serveIndex serves a paginated index page by calling the index macro.
func (h *HTMLFromDuckDB) serveIndex(w http.ResponseWriter, r *http.Request, page string) error {
	pageNum := 1
	if p, err := strconv.Atoi(page); err == nil && p > 0 {
		pageNum = p
	}

	// Derive base path from request if not configured
	basePath := h.BasePath
	if basePath == "" {
		basePath = strings.TrimSuffix(r.URL.Path, "/")
	}

	// Call the DuckDB macro
	query := fmt.Sprintf("SELECT html FROM %s(page := ?, base_path := ?)",
		sanitizeIdentifier(h.IndexMacro))

	h.logger.Debug("executing index macro",
		zap.String("macro", h.IndexMacro),
		zap.Int("page", pageNum),
		zap.String("base_path", basePath))

	ctx := r.Context()
	if h.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, h.timeout)
		defer cancel()
	}

	var html string
	err := h.db.QueryRowContext(ctx, query, pageNum, basePath).Scan(&html)
	if err != nil {
		h.logger.Error("index macro failed", zap.Error(err))
		return caddyhttp.Error(http.StatusInternalServerError, err)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(html)))
	if h.CacheControl != "" {
		w.Header().Set("Cache-Control", h.CacheControl)
	}

	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte(html)); err != nil {
		h.logger.Error("failed to write response", zap.Error(err))
		return err
	}

	h.logger.Debug("served index page",
		zap.Int("page", pageNum),
		zap.Int("size", len(html)))

	return nil
}

// serveSearch serves search results by calling the search macro.
func (h *HTMLFromDuckDB) serveSearch(w http.ResponseWriter, r *http.Request, query string) error {
	// Sanitize search query
	query = strings.TrimSpace(query)
	if len(query) > 200 {
		query = query[:200]
	}

	// Derive base path from request if not configured
	basePath := h.BasePath
	if basePath == "" {
		basePath = strings.TrimSuffix(r.URL.Path, "/")
		// Remove /search suffix if present
		basePath = strings.TrimSuffix(basePath, "/search")
	}

	// Call the DuckDB macro
	sql := fmt.Sprintf("SELECT html FROM %s(term := ?, base_path := ?)",
		sanitizeIdentifier(h.SearchMacro))

	h.logger.Debug("executing search macro",
		zap.String("macro", h.SearchMacro),
		zap.String("query", query),
		zap.String("base_path", basePath))

	ctx := r.Context()
	if h.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, h.timeout)
		defer cancel()
	}

	var html string
	err := h.db.QueryRowContext(ctx, sql, query, basePath).Scan(&html)
	if err != nil {
		h.logger.Error("search macro failed", zap.Error(err))
		return caddyhttp.Error(http.StatusInternalServerError, err)
	}

	// HTMX partial - no caching
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(html)))
	w.Header().Set("Cache-Control", "no-cache")

	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte(html)); err != nil {
		h.logger.Error("failed to write response", zap.Error(err))
		return err
	}

	h.logger.Debug("served search results",
		zap.String("query", query),
		zap.Int("size", len(html)))

	return nil
}

// UnmarshalCaddyfile implements caddyfile.Unmarshaler.
func (h *HTMLFromDuckDB) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	for d.Next() {
		for d.NextBlock(0) {
			switch d.Val() {
			case "database_path":
				if !d.NextArg() {
					return d.ArgErr()
				}
				h.DatabasePath = d.Val()

			case "table":
				if !d.NextArg() {
					return d.ArgErr()
				}
				h.Table = d.Val()

			case "html_column":
				if !d.NextArg() {
					return d.ArgErr()
				}
				h.HTMLColumn = d.Val()

			case "id_column":
				if !d.NextArg() {
					return d.ArgErr()
				}
				h.IDColumn = d.Val()

			case "id_param":
				if !d.NextArg() {
					return d.ArgErr()
				}
				h.IDParam = d.Val()

			case "where_clause":
				if !d.NextArg() {
					return d.ArgErr()
				}
				h.WhereClause = d.Val()

			case "not_found_redirect":
				if !d.NextArg() {
					return d.ArgErr()
				}
				h.NotFoundRedirect = d.Val()

			case "cache_control":
				if !d.NextArg() {
					return d.ArgErr()
				}
				h.CacheControl = d.Val()

			case "read_only":
				if !d.NextArg() {
					return d.ArgErr()
				}
				readOnly := d.Val() == "true"
				h.ReadOnly = &readOnly

			case "connection_pool_size":
				if !d.NextArg() {
					return d.ArgErr()
				}
				var err error
				if _, err = fmt.Sscanf(d.Val(), "%d", &h.ConnectionPoolSize); err != nil {
					return d.Errf("invalid connection_pool_size: %v", err)
				}

			case "query_timeout":
				if !d.NextArg() {
					return d.ArgErr()
				}
				h.QueryTimeout = d.Val()

			case "index_enabled":
				if !d.NextArg() {
					return d.ArgErr()
				}
				h.IndexEnabled = d.Val() == "true"

			case "index_macro":
				if !d.NextArg() {
					return d.ArgErr()
				}
				h.IndexMacro = d.Val()

			case "search_enabled":
				if !d.NextArg() {
					return d.ArgErr()
				}
				h.SearchEnabled = d.Val() == "true"

			case "search_macro":
				if !d.NextArg() {
					return d.ArgErr()
				}
				h.SearchMacro = d.Val()

			case "search_param":
				if !d.NextArg() {
					return d.ArgErr()
				}
				h.SearchParam = d.Val()

			case "base_path":
				if !d.NextArg() {
					return d.ArgErr()
				}
				h.BasePath = d.Val()

			case "init_sql_file":
				if !d.NextArg() {
					return d.ArgErr()
				}
				h.InitSQLFile = d.Val()

			default:
				return d.Errf("unrecognized subdirective: %s", d.Val())
			}
		}
	}
	return nil
}

// sanitizeIdentifier prevents SQL injection in table/column names.
// It only allows alphanumeric characters and underscores.
func sanitizeIdentifier(s string) string {
	var result strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			result.WriteRune(r)
		}
	}
	return result.String()
}

// Interface guards
var (
	_ caddy.Provisioner           = (*HTMLFromDuckDB)(nil)
	_ caddy.CleanerUpper          = (*HTMLFromDuckDB)(nil)
	_ caddyhttp.MiddlewareHandler = (*HTMLFromDuckDB)(nil)
	_ caddyfile.Unmarshaler       = (*HTMLFromDuckDB)(nil)
)
