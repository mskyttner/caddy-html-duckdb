// Package caddyhtmlduckdb provides a Caddy HTTP handler that serves HTML content from DuckDB tables.
package caddyhtmlduckdb

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"time"
	"context"

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

	h.logger.Info("HTML from DuckDB handler provisioned",
		zap.String("database", connStr),
		zap.String("table", h.Table),
		zap.Bool("read_only", *h.ReadOnly))

	return nil
}

// Cleanup closes the database connection.
func (h *HTMLFromDuckDB) Cleanup() error {
	if h.db != nil {
		return h.db.Close()
	}
	return nil
}

// ServeHTTP serves HTML content from DuckDB.
func (h *HTMLFromDuckDB) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	// Extract ID from URL
	var id string
	if h.IDParam != "" {
		// Get from query parameter
		id = r.URL.Query().Get(h.IDParam)
	} else {
		// Get from path (last segment)
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) > 0 {
			id = parts[len(parts)-1]
		}
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

	// Set headers
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
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
