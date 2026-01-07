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
            database_path /data/works.db
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

Run with your Caddyfile and database:

```bash
docker run -p 8080:8080 \
  -v ./Caddyfile:/etc/caddy/Caddyfile:ro \
  -v ./data:/data:ro \
  ghcr.io/mskyttner/caddy-html-duckdb:main
```

### Container Volumes

| Path | Description |
|------|-------------|
| `/etc/caddy/Caddyfile` | Caddy configuration file |
| `/data` | Database and data files |
| `/config` | Caddy auto-saved config (optional) |

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
