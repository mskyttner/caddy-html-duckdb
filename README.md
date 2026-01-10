# caddy-html-duckdb

[![CI](https://github.com/mskyttner/caddy-html-duckdb/actions/workflows/ci.yml/badge.svg)](https://github.com/mskyttner/caddy-html-duckdb/actions/workflows/ci.yml)

A Caddy module that serves HTML content from a DuckDB table. Useful for serving pre-rendered HTML pages stored in DuckDB at routes like `/works/{id}`.

## Configuration

### Caddyfile Directives

```caddyfile
html_from_duckdb {
    database_path <path>           # Path to DuckDB file (default: ":memory:")
    table <name>                   # Table name (required)
    html_column <name>             # Column with HTML content (default: "html")
    id_column <name>               # Column for ID lookup (default: "id")
    id_param <name>                # Query parameter for ID (default: use URL path)
    where_clause <sql>             # Additional WHERE conditions
    not_found_redirect <url>       # Redirect URL when content not found
    cache_control <value>          # Cache-Control header value
    read_only <bool>               # Open database read-only (default: true)
    connection_pool_size <int>     # Max connections (default: 10)
    query_timeout <duration>       # Query timeout (default: "5s")
    index_enabled <bool>           # Enable index page (default: false)
    index_macro <name>             # DuckDB macro for index page (default: "render_index")
    search_enabled <bool>          # Enable search endpoint (default: false)
    search_macro <name>            # DuckDB macro for search results (default: "render_search")
    search_param <name>            # Query parameter for search (default: "q")
    init_sql_file <path>           # SQL file to execute on startup (optional)
    record_macro <name>            # DuckDB macro for on-the-fly record rendering (optional)
}
```

### Example Caddyfile

```caddyfile
:8080 {
    # Enable request logging
    log {
        output stdout
        format console
    }

    route /works/* {
        html_from_duckdb {
            database_path works.db
            table html
            html_column html
            id_column pid
            cache_control "public, max-age=3600"
        }
    }
}
```

### Logging

Caddy doesn't log HTTP requests by default. Add a `log` directive to enable request logging:

```caddyfile
log {
    output stdout
    format console    # or "json" for structured logs
    level INFO        # DEBUG, INFO, WARN, ERROR
}
```

Use `level DEBUG` to see query logs from the html_from_duckdb handler.

## Container Image

Pull the container image from GitHub Container Registry:

```bash
docker pull ghcr.io/mskyttner/caddy-html-duckdb:main
```

### Using Environment Variables (no Caddyfile needed)

The container includes a default configuration that can be customized via environment variables:

```bash
docker run -p 8080:8080 \
  -e DATABASE_PATH=works.db \
  -e TABLE=html \
  -e ID_COLUMN=pid \
  -v ./mydata:/srv:ro \
  ghcr.io/mskyttner/caddy-html-duckdb:main
```

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8080` | Server port |
| `DATABASE_PATH` | `data.db` | Path to DuckDB file |
| `TABLE` | `html` | Table name |
| `HTML_COLUMN` | `html` | Column with HTML content |
| `ID_COLUMN` | `id` | Column for ID lookup |
| `ROUTE_PATH` | `/*` | URL route pattern |
| `READ_ONLY` | `true` | Open database read-only |
| `CONNECTION_POOL_SIZE` | `10` | Max connections |
| `QUERY_TIMEOUT` | `5s` | Query timeout |
| `INDEX_ENABLED` | `false` | Enable index page |
| `INDEX_MACRO` | `render_index` | DuckDB macro for index page |
| `SEARCH_ENABLED` | `false` | Enable search endpoint |
| `SEARCH_MACRO` | `render_search` | DuckDB macro for search results |
| `SEARCH_PARAM` | `q` | Query parameter for search |
| `INIT_SQL_COMMANDS_FILE` | (none) | SQL file to execute on startup |
| `RECORD_MACRO` | (none) | DuckDB macro for on-the-fly record rendering |
| `LOG_FORMAT` | `console` | Log format (`console` or `json`) |
| `LOG_LEVEL` | `INFO` | Log level (`DEBUG`, `INFO`, `WARN`, `ERROR`) |

### Using a Custom Caddyfile

For advanced configuration, mount your own Caddyfile:

```bash
docker run -p 8080:8080 \
  -v ./Caddyfile:/etc/caddy/Caddyfile:ro \
  -v ./mydata:/srv:ro \
  ghcr.io/mskyttner/caddy-html-duckdb:main
```

### Container Volumes

| Path | Description | Mode |
|------|-------------|------|
| `/etc/caddy/Caddyfile` | Caddy configuration file | read-only |
| `/srv` | Your database files (workdir) | read-only OK |
| `/data` | Caddy internal storage (TLS, locks) | read-write |
| `/config` | Caddy auto-saved config | read-write |

**Note:** Mount your database files to `/srv` (not `/data`). Caddy needs `/data` for its internal storage (TLS certificates, locks).

## Creating HTML Tables

A table with HTML rendered from a template can be created in DuckDB using the tera extension:

```sql
-- install tera from community;

load tera;

create or replace table html as (
  with render as (
    from (
      from pub
      select i: pub
    ) c
    select
      i,
      pid: i.PID,
      html: tera_render('works_template.html', i, template_path := './*_template.html')
  )

  from render
    select
      pid,
      html
);
```

## Building

Use the Makefile:

```bash
make build    # Build binary
make test     # Run tests
make fmt      # Format code
make clean    # Clean build artifacts
```

## Features

- Serves HTML content from DuckDB tables
- ETag support for HTTP caching (returns 304 Not Modified)
- Configurable cache headers
- Connection pooling
- Query timeouts
- SQL injection protection for identifiers
- Index page support via DuckDB table macros
- Full-text search support via DuckDB table macros
- Initialization SQL file for loading extensions and configuration
- On-the-fly record rendering via DuckDB table macros

## Index and Search

When enabled, the module can serve index pages and search results by calling DuckDB table macros.

### Index Page

When `index_enabled` is `true` and a request has no document ID, the module calls the `index_macro` (default: `render_index`):

```sql
CREATE OR REPLACE MACRO render_index(page := 1, base_path := '') AS TABLE
SELECT html FROM (
    -- Your index page generation logic here
    SELECT '<html>Page ' || page || '</html>' AS html
);
```

The macro receives:
- `page`: Page number from `?page=N` query parameter (default: 1)
- `base_path`: URL path for generating links

### Search

When `search_enabled` is `true` and the search parameter (default: `q`) is present, the module calls the `search_macro` (default: `render_search`):

```sql
CREATE OR REPLACE MACRO render_search(term := '', base_path := '') AS TABLE
SELECT html FROM (
    -- Your search logic here
    SELECT '<ul>Results for: ' || term || '</ul>' AS html
);
```

The macro receives:
- `term`: Search query (truncated to 200 characters for safety)
- `base_path`: URL path for generating links

Search results are served with `Cache-Control: no-cache` header.

## Record Macro (On-the-fly Rendering)

Instead of serving pre-rendered HTML from a table, you can use a DuckDB table macro to render pages on-the-fly. This is useful when you want to use Tera templates without pre-rendering all pages.

### Configuration

Set the `record_macro` directive (or `RECORD_MACRO` environment variable) to the name of your rendering macro:

```caddyfile
html_from_duckdb {
    database_path works.db
    record_macro render_record
    html_column html
    cache_control "public, max-age=3600"
}
```

When `record_macro` is set, the handler queries using:
```sql
SELECT html FROM render_record(id := 'requested_id')
```

Instead of the traditional table query:
```sql
SELECT html FROM table WHERE id = 'requested_id'
```

### Example Macro

Create a table macro that renders HTML using Tera templates:

```sql
CREATE OR REPLACE MACRO render_record(id := '') AS TABLE
SELECT tera_render(
    'works_template.html',
    pub,
    template_path := 'templates/*'
) AS html
FROM publications
WHERE pid = id;
```

### Usage with Container

```bash
docker run -p 8080:8080 \
  -e DATABASE_PATH=works.db \
  -e RECORD_MACRO=render_record \
  -e INIT_SQL_COMMANDS_FILE=init.sql \
  -v ./mydata:/srv:ro \
  ghcr.io/mskyttner/caddy-html-duckdb:main
```

### Comparison

| Feature | Table-based (`table`) | Macro-based (`record_macro`) |
|---------|----------------------|------------------------------|
| Query | `SELECT html FROM table WHERE id = ?` | `SELECT html FROM macro(id := 'x')` |
| Storage | Pre-rendered HTML in table | Source data + templates |
| Rendering | At build time | On each request |
| Flexibility | Static content | Dynamic (template changes apply immediately) |
| Performance | Faster (no rendering) | Slower (rendering per request) |

**Note:** When `record_macro` is set, the `table`, `id_column`, and `where_clause` directives are ignored for individual record queries. Index and search still use their respective macros.

## Initialization SQL File

The `init_sql_file` directive (or `INIT_SQL_COMMANDS_FILE` environment variable) allows you to execute SQL commands when the database connection is established. This is useful for:

- Loading DuckDB extensions (`LOAD tera;`, `LOAD fts;`)
- Setting configuration (`SET autoinstall_known_extensions=1;`)
- Creating views, macros, or temporary tables

### Example init.sql

```sql
-- Load required extensions
SET autoinstall_known_extensions = 1;
SET autoload_known_extensions = 1;

LOAD tera;
LOAD fts;

/* Create a search macro that uses
   full-text search */
CREATE OR REPLACE MACRO render_search(term := '', base_path := '') AS TABLE
SELECT html FROM (
    SELECT '<ul>Results for: ' || term || '</ul>' AS html
);
```

### Features

- **Multiline statements**: Statements can span multiple lines
- **Comments**: Both single-line (`--`) and block (`/* */`) comments are supported
- **String literals**: Semicolons inside quoted strings are handled correctly
- **Error reporting**: Failed statements report the statement number and content

### Usage with Container

```bash
docker run -p 8080:8080 \
  -e DATABASE_PATH=works.db \
  -e INIT_SQL_COMMANDS_FILE=init.sql \
  -e INDEX_ENABLED=true \
  -e SEARCH_ENABLED=true \
  -v ./mydata:/srv:ro \
  ghcr.io/mskyttner/caddy-html-duckdb:main
```

Place your `init.sql` file in the mounted `/srv` directory alongside your database.

## Troubleshooting

### Permission Denied with Rootless Podman/Docker

When using rootless Podman or Docker, you may encounter permission errors:

```
Cannot open file "/srv/test.db.wal": Permission denied
```

**Cause:** Rootless containers run as a non-root user (UID 1000). The container user needs write access to create DuckDB's WAL (Write-Ahead Log) files.

**Solutions:**

1. **For read-only databases** (recommended for production):
   ```bash
   # Pre-create macros on host, then mount read-only
   duckdb works.db < init.sql
   docker run -e READ_ONLY=true -v ./data:/srv:ro ...
   ```

2. **For writable databases** (development/testing):
   ```bash
   # Ensure directory is writable by container user
   chmod 777 ./mydata
   docker run -e READ_ONLY=false -v ./mydata:/srv ...
   ```

| Scenario | `READ_ONLY` | Volume Mount | Notes |
|----------|-------------|--------------|-------|
| Production | `true` (default) | `:ro` | Pre-create macros in database |
| Init SQL with CREATE | `false` | writable | Directory must be writable |
| Development | `false` | writable | Allows runtime modifications |
