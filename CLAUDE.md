# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

A Caddy module that serves HTML content from DuckDB tables. It enables serving pre-rendered or dynamically-rendered HTML pages at routes like `/works/{id}`, with support for index pages, full-text search, and health checks via DuckDB table macros.

## Build Commands

```bash
make build    # Build binary (CGO_ENABLED=1 required for DuckDB)
make test     # Run tests with verbose output
make fmt      # Format Go code
make clean    # Clean build artifacts and caches
```

Building requires CGO enabled due to DuckDB's native bindings.

## Architecture

### Module Structure

- `module.go` - Main Caddy HTTP handler implementing `caddyhttp.MiddlewareHandler`
- `module_test.go` - Unit tests using in-memory DuckDB
- `cmd/caddy/main.go` - Custom Caddy build entry point that imports the module

### Handler Flow

The `HTMLFromDuckDB` handler processes requests in this order:
1. Health check endpoint (if `health_enabled` and path matches `{base_path}/{health_path}`)
2. Table endpoint (if `table_macro` set and path matches `{base_path}/{table_path}`) - returns ASCII table
3. Search query (if `search_enabled` and `?q=` parameter present) - calls `search_macro`
4. Index page (if `index_enabled` and no ID in path) - calls `index_macro`
5. Individual record lookup:
   - If `record_macro` set: `SELECT html FROM macro(id := 'value')` (on-the-fly rendering)
   - Otherwise: `SELECT html FROM table WHERE id = ?` (pre-rendered lookup)

### DuckDB Table Macros

The module uses DuckDB table macros for dynamic content:
- `render_index(page, base_path)` - Paginated index page
- `render_search(term, base_path)` - Search results (HTMX partial)
- `record_macro(id)` - On-the-fly record rendering with Tera templates
- `table_macro(params...)` - ASCII table output formatted with tablewriter (URL query params passed through)

Macros don't support parameterized queries, so the handler uses `escapeSQLString()` for SQL injection protection.

### Key Implementation Details

- `sanitizeIdentifier()` - Strips non-alphanumeric chars from table/column names
- `escapeSQLString()` - Escapes single quotes for macro parameters
- `parseSQLStatements()` - Parses init SQL file handling comments and string literals
- `formatTable()` - Formats SQL rows as ASCII table using tablewriter (borderless, right-aligned numerics)
- ETag generation uses MD5 hash of HTML content for conditional requests

## Testing

Tests use in-memory DuckDB (`:memory:`) and create tables/macros inline. Run a single test:

```bash
CGO_ENABLED=1 go test -v -run TestServeHTTP_Health ./...
```

## Container Usage

The module is distributed as a container image. Key environment variables map to Caddyfile directives (see README for full list). Mount database files to `/srv`, not `/data` (Caddy uses `/data` for TLS certificates).

For rootless podman, use `userns_mode: keep-id` in compose.yaml.
