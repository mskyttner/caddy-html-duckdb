// Package caddyhtmlduckdb provides a Caddy HTTP handler that serves HTML content from DuckDB tables.
package caddyhtmlduckdb

import (
	"bufio"
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"encoding/json"
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
	"github.com/olekukonko/tablewriter"
	"github.com/olekukonko/tablewriter/renderer"
	"github.com/olekukonko/tablewriter/tw"
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

	// RecordMacro is the name of a DuckDB table macro for rendering individual records.
	// When set, the handler queries using: SELECT html FROM macro_name(id := 'value')
	// instead of: SELECT html FROM table WHERE id = 'value'
	// This enables on-the-fly rendering using Tera templates.
	// The macro should accept an id parameter and return a single html column.
	RecordMacro string `json:"record_macro,omitempty"`

	// TableMacro is the name of a DuckDB table macro for rendering tabular data.
	// The macro returns multiple columns which are formatted as an ASCII table.
	// URL query parameters are passed to the macro by name.
	TableMacro string `json:"table_macro,omitempty"`

	// TablePath is the endpoint path for the table macro, relative to BasePath.
	// Default: "_table"
	TablePath string `json:"table_path,omitempty"`

	// HealthEnabled enables a health check endpoint.
	// Default: false
	HealthEnabled bool `json:"health_enabled,omitempty"`

	// HealthPath is the path for the health check endpoint, relative to BasePath.
	// Default: "_health"
	HealthPath string `json:"health_path,omitempty"`

	// HealthDetailed includes connection pool stats and latencies in the response.
	// Default: false
	HealthDetailed bool `json:"health_detailed,omitempty"`

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
	if h.TablePath == "" {
		h.TablePath = "_table"
	}
	if h.HealthPath == "" {
		h.HealthPath = "_health"
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
		zap.Bool("search_enabled", h.SearchEnabled),
		zap.Bool("health_enabled", h.HealthEnabled))

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
	// Check for health endpoint first
	if h.HealthEnabled {
		healthPath := "/" + h.HealthPath
		if h.BasePath != "" {
			healthPath = h.BasePath + "/" + h.HealthPath
		}
		if r.URL.Path == healthPath {
			return h.serveHealth(w, r)
		}
	}

	// Check for table endpoint
	if h.TableMacro != "" {
		tablePath := "/" + h.TablePath
		if h.BasePath != "" {
			tablePath = h.BasePath + "/" + h.TablePath
		}
		if strings.HasPrefix(r.URL.Path, tablePath) {
			return h.serveTable(w, r)
		}
	}

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
	var query string
	var useParams bool

	if h.RecordMacro != "" {
		// Use table macro: SELECT html FROM macro_name(id := 'escaped_value')
		// DuckDB table macros don't support parameterized queries
		query = fmt.Sprintf("SELECT %s FROM %s(id := '%s')",
			sanitizeIdentifier(h.HTMLColumn),
			sanitizeIdentifier(h.RecordMacro),
			escapeSQLString(id))
		useParams = false
	} else {
		// Traditional table query with parameterized ID
		query = fmt.Sprintf("SELECT %s FROM %s WHERE %s = ?",
			sanitizeIdentifier(h.HTMLColumn),
			sanitizeIdentifier(h.Table),
			sanitizeIdentifier(h.IDColumn))
		useParams = true

		if h.WhereClause != "" {
			query += fmt.Sprintf(" AND (%s)", h.WhereClause)
		}
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
	var err error
	if useParams {
		err = h.db.QueryRowContext(ctx, query, id).Scan(&html)
	} else {
		err = h.db.QueryRowContext(ctx, query).Scan(&html)
	}
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
	// Note: DuckDB table macros don't support ? parameter placeholders,
	// so we use string interpolation with proper escaping
	query := fmt.Sprintf("SELECT html FROM %s(page := %d, base_path := '%s')",
		sanitizeIdentifier(h.IndexMacro),
		pageNum,
		escapeSQLString(basePath))

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
	err := h.db.QueryRowContext(ctx, query).Scan(&html)
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
func (h *HTMLFromDuckDB) serveSearch(w http.ResponseWriter, r *http.Request, searchTerm string) error {
	// Sanitize search query
	searchTerm = strings.TrimSpace(searchTerm)
	if len(searchTerm) > 200 {
		searchTerm = searchTerm[:200]
	}

	// Derive base path from request if not configured
	basePath := h.BasePath
	if basePath == "" {
		basePath = strings.TrimSuffix(r.URL.Path, "/")
		// Remove /search suffix if present
		basePath = strings.TrimSuffix(basePath, "/search")
	}

	// Call the DuckDB macro
	// Note: DuckDB table macros don't support ? parameter placeholders,
	// so we use string interpolation with proper escaping
	query := fmt.Sprintf("SELECT html FROM %s(term := '%s', base_path := '%s')",
		sanitizeIdentifier(h.SearchMacro),
		escapeSQLString(searchTerm),
		escapeSQLString(basePath))

	h.logger.Debug("executing search macro",
		zap.String("macro", h.SearchMacro),
		zap.String("term", searchTerm),
		zap.String("base_path", basePath))

	ctx := r.Context()
	if h.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, h.timeout)
		defer cancel()
	}

	var html string
	err := h.db.QueryRowContext(ctx, query).Scan(&html)
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

// serveTable serves tabular data from a DuckDB macro, formatted as an ASCII table.
func (h *HTMLFromDuckDB) serveTable(w http.ResponseWriter, r *http.Request) error {
	// Extract query params
	params := r.URL.Query()

	// Build macro call with all params
	var paramParts []string
	for key, values := range params {
		if len(values) > 0 {
			// Sanitize parameter name
			sanitizedKey := sanitizeIdentifier(key)
			if sanitizedKey == "" {
				continue
			}
			// Try to parse as int, otherwise treat as string
			if _, err := strconv.Atoi(values[0]); err == nil {
				paramParts = append(paramParts, fmt.Sprintf("%s := %s",
					sanitizedKey, values[0]))
			} else {
				paramParts = append(paramParts, fmt.Sprintf("%s := '%s'",
					sanitizedKey, escapeSQLString(values[0])))
			}
		}
	}

	// Add base_path if not already provided
	if params.Get("base_path") == "" {
		basePath := h.BasePath
		if basePath == "" {
			basePath = strings.TrimSuffix(r.URL.Path, "/")
		}
		paramParts = append(paramParts, fmt.Sprintf("base_path := '%s'", escapeSQLString(basePath)))
	}

	query := fmt.Sprintf("SELECT * FROM %s(%s)",
		sanitizeIdentifier(h.TableMacro),
		strings.Join(paramParts, ", "))

	h.logger.Debug("executing table macro",
		zap.String("macro", h.TableMacro),
		zap.String("query", query))

	// Execute with timeout
	ctx := r.Context()
	if h.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, h.timeout)
		defer cancel()
	}

	rows, err := h.db.QueryContext(ctx, query)
	if err != nil {
		h.logger.Error("table macro failed", zap.Error(err))
		return caddyhttp.Error(http.StatusInternalServerError, err)
	}
	defer rows.Close()

	// Format with tablewriter
	html, err := h.formatTable(rows)
	if err != nil {
		h.logger.Error("table formatting failed", zap.Error(err))
		return caddyhttp.Error(http.StatusInternalServerError, err)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(html)))
	w.Header().Set("Cache-Control", "no-cache")

	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte(html)); err != nil {
		h.logger.Error("failed to write response", zap.Error(err))
		return err
	}

	h.logger.Debug("served table",
		zap.String("macro", h.TableMacro),
		zap.Int("size", len(html)))

	return nil
}

// formatTable formats SQL rows as an ASCII table wrapped in HTML pre tags.
func (h *HTMLFromDuckDB) formatTable(rows *sql.Rows) (string, error) {
	cols, err := rows.ColumnTypes()
	if err != nil {
		return "", err
	}

	colNames := make([]string, len(cols))
	alignments := make([]tw.Align, len(cols))
	for i, col := range cols {
		colNames[i] = col.Name()
		// Right-align numeric types
		switch col.DatabaseTypeName() {
		case "INTEGER", "BIGINT", "DOUBLE", "FLOAT", "DECIMAL", "HUGEINT", "SMALLINT", "TINYINT", "UBIGINT", "UINTEGER", "USMALLINT", "UTINYINT":
			alignments[i] = tw.AlignRight
		default:
			alignments[i] = tw.AlignLeft
		}
	}

	var buf strings.Builder
	buf.WriteString(`<pre class="duckbox">`)
	buf.WriteString("\n")

	// Create table with borderless renderer
	table := tablewriter.NewTable(&buf,
		tablewriter.WithRenderer(renderer.NewBlueprint(tw.Rendition{
			Borders: tw.BorderNone,
			Settings: tw.Settings{
            			Separators: tw.Separators{
                			BetweenRows:    tw.Off,
                			BetweenColumns: tw.Off,                  // no inner separators
            			},
            			Lines: tw.Lines{
                			ShowHeaderLine: tw.On,                   // blank line after header
                			ShowFooterLine: tw.Off,
            			},
        		},
		})),
		tablewriter.WithConfig(tablewriter.Config{
			Header: tw.CellConfig{
				Alignment: tw.CellAlignment{
					Global: tw.AlignLeft,
				},
				Formatting: tw.CellFormatting{
					AutoFormat: tw.Off,
				},
			},
			Row: tw.CellConfig{
				Alignment: tw.CellAlignment{
					PerColumn: alignments,
				},
			},
		}),
	)

	// Convert string slice to any slice for Header
	headerAny := make([]any, len(colNames))
	for i, v := range colNames {
		headerAny[i] = v
	}
	table.Header(headerAny...)

	// Add blank line between header and data rows
	emptyRow := make([]string, len(cols))
	table.Append(emptyRow)

	// Scan rows
	values := make([]interface{}, len(cols))
	valuePtrs := make([]interface{}, len(cols))
	for i := range values {
		valuePtrs[i] = &values[i]
	}

	for rows.Next() {
		if err := rows.Scan(valuePtrs...); err != nil {
			return "", err
		}

		row := make([]string, len(cols))
		for i, v := range values {
			if v == nil {
				row[i] = ""
			} else {
				row[i] = fmt.Sprintf("%v", v)
			}
		}
		table.Append(row)
	}

	if err := rows.Err(); err != nil {
		return "", err
	}

	table.Render()
	buf.WriteString(`</pre>`)

	return buf.String(), nil
}

// HealthResponse represents the JSON structure of a health check response.
type HealthResponse struct {
	Status string                  `json:"status"`
	Checks map[string]*CheckResult `json:"checks"`
	Pool   *PoolStats              `json:"pool,omitempty"`
}

// CheckResult represents the result of a single health check.
type CheckResult struct {
	Status    string `json:"status"`
	Name      string `json:"name,omitempty"`
	LatencyMs int64  `json:"latency_ms,omitempty"`
	Error     string `json:"error,omitempty"`
}

// PoolStats represents database connection pool statistics.
type PoolStats struct {
	OpenConnections int `json:"open_connections"`
	InUse           int `json:"in_use"`
	Idle            int `json:"idle"`
}

// serveHealth serves the health check endpoint.
func (h *HTMLFromDuckDB) serveHealth(w http.ResponseWriter, r *http.Request) error {
	response := HealthResponse{
		Status: "healthy",
		Checks: make(map[string]*CheckResult),
	}

	allHealthy := true

	// Check database connectivity
	dbCheck := h.checkDatabase(r.Context())
	response.Checks["database"] = dbCheck
	if dbCheck.Status != "ok" {
		allHealthy = false
	}

	// Check table accessibility
	tableCheck := h.checkTable(r.Context())
	response.Checks["table"] = tableCheck
	if tableCheck.Status != "ok" {
		allHealthy = false
	}

	// Check index macro if enabled
	if h.IndexEnabled {
		indexCheck := h.checkMacro(r.Context(), h.IndexMacro, "index_macro")
		response.Checks["index_macro"] = indexCheck
		if indexCheck.Status != "ok" {
			allHealthy = false
		}
	}

	// Check search macro if enabled
	if h.SearchEnabled {
		searchCheck := h.checkMacro(r.Context(), h.SearchMacro, "search_macro")
		response.Checks["search_macro"] = searchCheck
		if searchCheck.Status != "ok" {
			allHealthy = false
		}
	}

	// Check record macro if configured
	if h.RecordMacro != "" {
		recordCheck := h.checkMacro(r.Context(), h.RecordMacro, "record_macro")
		response.Checks["record_macro"] = recordCheck
		if recordCheck.Status != "ok" {
			allHealthy = false
		}
	}

	// Check table macro if configured
	if h.TableMacro != "" {
		tableCheck := h.checkMacro(r.Context(), h.TableMacro, "table_macro")
		response.Checks["table_macro"] = tableCheck
		if tableCheck.Status != "ok" {
			allHealthy = false
		}
	}

	// Add pool stats if detailed mode is enabled
	if h.HealthDetailed {
		stats := h.db.Stats()
		response.Pool = &PoolStats{
			OpenConnections: stats.OpenConnections,
			InUse:           stats.InUse,
			Idle:            stats.Idle,
		}
	}

	if !allHealthy {
		response.Status = "unhealthy"
	}

	// Determine HTTP status code
	statusCode := http.StatusOK
	if !allHealthy {
		statusCode = http.StatusServiceUnavailable
	}

	// Marshal response
	jsonResponse, err := json.Marshal(response)
	if err != nil {
		h.logger.Error("failed to marshal health response", zap.Error(err))
		return caddyhttp.Error(http.StatusInternalServerError, err)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(jsonResponse)))
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.WriteHeader(statusCode)

	if _, err := w.Write(jsonResponse); err != nil {
		h.logger.Error("failed to write health response", zap.Error(err))
		return err
	}

	h.logger.Debug("served health check",
		zap.String("status", response.Status),
		zap.Int("status_code", statusCode))

	return nil
}

// checkDatabase verifies database connectivity with a ping.
func (h *HTMLFromDuckDB) checkDatabase(ctx context.Context) *CheckResult {
	start := time.Now()

	if h.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, h.timeout)
		defer cancel()
	}

	err := h.db.PingContext(ctx)
	latency := time.Since(start).Milliseconds()

	if err != nil {
		return &CheckResult{
			Status:    "error",
			LatencyMs: latency,
			Error:     err.Error(),
		}
	}

	return &CheckResult{
		Status:    "ok",
		LatencyMs: latency,
	}
}

// checkTable verifies the table is accessible.
func (h *HTMLFromDuckDB) checkTable(ctx context.Context) *CheckResult {
	start := time.Now()

	if h.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, h.timeout)
		defer cancel()
	}

	query := fmt.Sprintf("SELECT 1 FROM %s LIMIT 1", sanitizeIdentifier(h.Table))
	_, err := h.db.ExecContext(ctx, query)
	latency := time.Since(start).Milliseconds()

	if err != nil {
		return &CheckResult{
			Status:    "error",
			Name:      h.Table,
			LatencyMs: latency,
			Error:     err.Error(),
		}
	}

	return &CheckResult{
		Status:    "ok",
		Name:      h.Table,
		LatencyMs: latency,
	}
}

// checkMacro verifies a DuckDB macro exists by querying duckdb_functions().
func (h *HTMLFromDuckDB) checkMacro(ctx context.Context, macroName, checkName string) *CheckResult {
	start := time.Now()

	if h.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, h.timeout)
		defer cancel()
	}

	// Query DuckDB's function catalog to check if macro exists
	query := "SELECT 1 FROM duckdb_functions() WHERE function_name = ? AND function_type = 'table_macro' LIMIT 1"
	var exists int
	err := h.db.QueryRowContext(ctx, query, macroName).Scan(&exists)
	latency := time.Since(start).Milliseconds()

	if err == sql.ErrNoRows {
		return &CheckResult{
			Status:    "error",
			Name:      macroName,
			LatencyMs: latency,
			Error:     "macro not found",
		}
	}
	if err != nil {
		return &CheckResult{
			Status:    "error",
			Name:      macroName,
			LatencyMs: latency,
			Error:     err.Error(),
		}
	}

	return &CheckResult{
		Status:    "ok",
		Name:      macroName,
		LatencyMs: latency,
	}
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
				if d.NextArg() {
					h.InitSQLFile = d.Val()
				}
				// No error if empty - allows {$INIT_SQL_COMMANDS_FILE:} with empty default

			case "record_macro":
				if d.NextArg() {
					h.RecordMacro = d.Val()
				}
				// No error if empty - allows {$RECORD_MACRO:} with empty default

			case "table_macro":
				if d.NextArg() {
					h.TableMacro = d.Val()
				}
				// No error if empty - allows {$TABLE_MACRO:} with empty default

			case "table_path":
				if d.NextArg() {
					h.TablePath = d.Val()
				}
				// No error if empty - allows {$TABLE_PATH:} with empty default

			case "health_enabled":
				if !d.NextArg() {
					return d.ArgErr()
				}
				h.HealthEnabled = d.Val() == "true"

			case "health_path":
				if d.NextArg() {
					h.HealthPath = d.Val()
				}
				// No error if empty - allows {$HEALTH_PATH:} with empty default

			case "health_detailed":
				if !d.NextArg() {
					return d.ArgErr()
				}
				h.HealthDetailed = d.Val() == "true"

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

// escapeSQLString escapes single quotes in a string for safe SQL interpolation.
// This is needed because DuckDB table macros don't support parameterized queries.
func escapeSQLString(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

// Interface guards
var (
	_ caddy.Provisioner           = (*HTMLFromDuckDB)(nil)
	_ caddy.CleanerUpper          = (*HTMLFromDuckDB)(nil)
	_ caddyhttp.MiddlewareHandler = (*HTMLFromDuckDB)(nil)
	_ caddyfile.Unmarshaler       = (*HTMLFromDuckDB)(nil)
)
