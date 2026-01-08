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
