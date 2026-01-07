# caddy-html-duckdb

[![CI](https://github.com/mskyttner/caddy-html-duckdb/actions/workflows/ci.yml/badge.svg)](https://github.com/mskyttner/caddy-html-duckdb/actions/workflows/ci.yml)

This caddy module exposes a duckdb table with a column containing rendered html at a route like "/works/{id}".

A table with HTML rendered from a template can be created in duckdb using the tera extension with sql like this:

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

## Container Image

Pull the container image from GitHub Container Registry:

```bash
docker pull ghcr.io/mskyttner/caddy-html-duckdb:main
```

Run with your Caddyfile and database:

```bash
docker run -p 8080:80 \
  -v ./Caddyfile:/etc/caddy/Caddyfile:ro \
  -v ./data:/data:ro \
  ghcr.io/mskyttner/caddy-html-duckdb:main
```

## Building

Use the Makefile, with `make build`.

The following attempts at building with xcaddy failed:

```
CGO_ENABLED=1 xcaddy build --with $(pwd)/caddy-html-duckdb --output ./caddy
go get github.com/caddyserver/caddy/v2@latest
go mod edit -replace=github.com/mskyttner/caddy-html-duckdb=/home/markus/repos/caddy-html-duckdb
CGO_ENABLED=1 xcaddy build --output ./caddy
go get github.com/caddyserver/caddy/v2@latest
go mod edit -replace=github.com/mskyttner/caddy-html-duckdb=/home/markus/repos/caddy-html-duckdb
xcaddy build --output ./caddy-with-duckdb
./caddy-with-duckdb run
 
# xcaddy build --with github.com/mskyttner/caddy-html-duckdb --output ./caddy-with-duckdb
# xcaddy build --with /home/markus/repos/caddy-html-duckdb --output ./caddy-with-duckdb
```

