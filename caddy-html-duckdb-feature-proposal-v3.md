# Feature Proposal: Index Page with Full-Text Search for caddy-html-duckdb

## Implementation Status

| Component | Status | Version |
|-----------|--------|---------|
| Caddy module config (`index_enabled`, `index_macro`) | ✅ Completed | v1.1.0 |
| Caddy module config (`search_enabled`, `search_macro`, `search_param`) | ✅ Completed | v1.1.0 |
| Environment variables for container | ✅ Completed | v1.1.0 |
| Go routing logic (`serveIndex`, `serveSearch`) | ✅ Completed | v1.1.0 |
| Container image with new features | ✅ Completed | v1.1.0 |
| Init SQL file support (`init_sql_file`, `INIT_SQL_COMMANDS_FILE`) | ✅ Completed | v1.2.0 |
| Multiline SQL parsing (comments, string literals) | ✅ Completed | v1.2.0 |
| Troubleshooting documentation (permissions) | ✅ Completed | v1.2.0 |
| Fix DuckDB table macro parameter handling | ✅ Completed | v1.3.0 |
| Record macro for on-the-fly rendering (`record_macro`, `RECORD_MACRO`) | ✅ Completed | v1.3.0 |
| DuckDB table macros (examples) | 📋 User responsibility | - |
| Tera templates (examples) | 📋 User responsibility | - |
| FTS index setup | 📋 User responsibility | - |

---

## Overview

The `caddy-html-duckdb` module currently serves HTML content from a DuckDB table when a specific ID is provided in the URL path (e.g., `/works/doc123`). This proposal adds two complementary features:

1. **Index Page**: When no ID is provided, display a paginated listing of all available pages
2. **Full-Text Search**: Add a search box powered by DuckDB's FTS extension with HTMX for dynamic results

The key design principle: **all rendering logic lives in DuckDB as table macros**. The Go module simply calls these macros and returns the resulting HTML.

---

## Architecture

```
┌─────────────┐     ┌─────────────────┐     ┌─────────────────────────────┐
│   Browser   │────▶│  Caddy Module   │────▶│          DuckDB             │
│             │     │    (Go code)    │     │                             │
│  /works/    │     │                 │     │  FROM render_index(page=1)  │
│  /works/123 │     │  Simple query   │     │  FROM render_search('dogs') │
│  /search?q= │     │  execution      │     │                             │
└─────────────┘     └─────────────────┘     └─────────────────────────────┘
       ▲                    │                            │
       │                    │                            │
       └────────────────────┴────────────────────────────┘
                         HTML response
```

---

## Database Setup

### Required Extensions

```sql
INSTALL webbed FROM community;
INSTALL tera FROM community;

LOAD webbed;
LOAD tera;
LOAD fts;
```

### Table Macros

The database provides these table macros that return rendered HTML:

| Macro | Purpose | Example |
|-------|---------|---------|
| `render_index(page, base_path, search_endpoint)` | Paginated index page | `FROM render_index(page := 2)` |
| `render_search(term, base_path)` | Search results fragment | `FROM render_search('dogs')` |
| `html_search(term)` | Raw search results (no rendering) | `FROM html_search('climate')` |

### Complete Database Setup Script

```sql
INSTALL webbed FROM community;
INSTALL tera FROM community;

LOAD webbed;
LOAD tera;
LOAD fts;

-- Extract text from HTML content using webbed extension
CREATE OR REPLACE MACRO html_to_text(html) AS (
    WITH h AS (
        SELECT a: parse_html(html)
    ),
    b AS (
        FROM h
        SELECT s: unnest(html_to_duck_blocks(a))
    )
    FROM b
    SELECT text: trim(array_to_string(list(s.content), ' '))
);

-- Create clean text table for FTS indexing
CREATE OR REPLACE TABLE html_clean AS (
    FROM html 
    SELECT 
        id, 
        text: html_to_text(html)
);

-- Build full-text search index
PRAGMA create_fts_index('html_clean', 'id', 'text', overwrite = 1);

-- Table macro: search with normalized scores
CREATE OR REPLACE MACRO html_search(term := '') AS TABLE (
    FROM (
        FROM (
            FROM html_clean 
            SELECT 
                id, 
                s: fts_main_html_clean.match_bm25(id, term),
                blurb: left(text, 50) || '...'
        )
        SELECT
            id,
            blurb,
            maxs: max(s) OVER(),
            mins: min(s) OVER(),
            score: ((s - mins) / nullif((maxs - mins), 0))::decimal(3, 2)
        ORDER BY score DESC
    )
    SELECT id, blurb, score
    WHERE score IS NOT NULL
    LIMIT 20 
);

-- Table macro: render search results as HTML fragment (for HTMX)
CREATE OR REPLACE MACRO render_search(term := '', base_path := 'works') AS TABLE (
    WITH s AS (
        FROM html_search(term)
    ),
    items AS (
        SELECT data: json_object(
            'query', term,
            'base_path', base_path,
            'total_results', (FROM s SELECT count(*)),
            'items', (FROM s SELECT json_group_array(json_object(
                'id', id, 
                'title', blurb, 
                'snippet', null, 
                'score', score
            )))
        )
    )
    FROM items
    SELECT html: tera_render(
        'search_results_template.html', 
        data, 
        template_path := 'templates/*.html'
    )
);

-- Helper macro: paginate any table
CREATE OR REPLACE MACRO html_index(t, per_page := 10) AS TABLE (
    WITH pages AS (
        FROM query_table(t)
        SELECT
            row_no: row_number() OVER (ORDER BY id),
            page_length: per_page::int,
            page_count: ceil((FROM html SELECT count(*))::float / page_length)::int,
            page_no: (((row_no - 1) // page_length) + 1)::int,
            page_offset: (page_no - 1) * page_length,
            *
    )
    FROM pages
    SELECT
        page_prev: CASE WHEN page_no > 1 THEN page_no - 1 ELSE null END,
        page_next: CASE WHEN page_no < page_count THEN page_no + 1 ELSE null END,
        *
);

-- Table macro: render paginated index page as full HTML document
CREATE OR REPLACE MACRO render_index(page := 1, base_path := 'works', search_endpoint := 'works/search') AS TABLE (
    WITH pages AS (
        FROM html_index('html_clean')
        SELECT data: json_object(
            'title', 'Available Works',
            'base_path', base_path,
            'search_enabled', true,
            'search_endpoint', search_endpoint,
            'pagination', true,
            'current_page', first(page_no),
            'total_pages', first(page_count),
            'prev_page', first(page_prev),
            'next_page', first(page_next),
            'items', (
                SELECT json_group_array(json_object(
                    'id', id,
                    'title', left(text, 63) || ' ...'
                ))
            )
        )
        WHERE page_no = coalesce(page, 1)
        GROUP BY page_no, base_path
        ORDER BY page_no        
    )
    FROM pages
    SELECT html: tera_render(
        'index_template.html',
        data,
        template_path := 'templates/*.html'
    )
);
```

---

## Caddy Module Configuration

### Caddyfile Directives

```caddyfile
html_from_duckdb {
    database_path <path>              # Path to DuckDB file
    table <name>                      # Table with HTML content
    html_column <name>                # Column containing HTML
    id_column <name>                  # Column for ID lookup
    
    # Index page settings
    index_enabled <bool>              # Enable index page (default: false)
    index_macro <name>                # Macro name (default: "render_index")
    index_base_path <path>            # Base path for links (default: from route)
    
    # Search settings  
    search_enabled <bool>             # Enable search endpoint (default: false)
    search_macro <name>               # Macro name (default: "render_search")
    search_param <name>               # Query parameter (default: "q")
}
```

### Example Caddyfile

```caddyfile
:8080 {
    log {
        output stdout
        format console
    }

    route /works/* {
        html_from_duckdb {
            database_path /srv/works.db
            table html
            html_column html
            id_column id
            cache_control "public, max-age=3600"
            
            # Index and search via DuckDB macros
            index_enabled true
            index_macro render_index
            
            search_enabled true
            search_macro render_search
            search_param q
        }
    }
}
```

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `INDEX_ENABLED` | `false` | Enable index page |
| `INDEX_MACRO` | `render_index` | DuckDB macro for index |
| `SEARCH_ENABLED` | `false` | Enable search |
| `SEARCH_MACRO` | `render_search` | DuckDB macro for search |
| `SEARCH_PARAM` | `q` | Query parameter name |

---

## Go Implementation

The Go code becomes very simple - it just needs to detect the request type and call the appropriate macro:

```go
func (h *HtmlFromDuckDB) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
    id := h.extractID(r.URL.Path)
    query := r.URL.Query().Get(h.SearchParam)
    page := r.URL.Query().Get("page")
    
    // Search request (HTMX partial)
    if query != "" && h.SearchEnabled {
        return h.serveSearch(w, r, query)
    }
    
    // Index page request
    if id == "" && h.IndexEnabled {
        return h.serveIndex(w, r, page)
    }
    
    // Individual document request
    if id != "" {
        return h.serveDocument(w, r, id)
    }
    
    return next.ServeHTTP(w, r)
}

func (h *HtmlFromDuckDB) serveIndex(w http.ResponseWriter, r *http.Request, page string) error {
    pageNum := 1
    if p, err := strconv.Atoi(page); err == nil && p > 0 {
        pageNum = p
    }
    
    // Call the DuckDB macro
    sql := fmt.Sprintf("FROM %s(page := ?, base_path := ?) SELECT html", 
        sanitizeIdentifier(h.IndexMacro))
    
    var html string
    err := h.db.QueryRow(sql, pageNum, h.BasePath).Scan(&html)
    if err != nil {
        return caddyhttp.Error(http.StatusInternalServerError, err)
    }
    
    w.Header().Set("Content-Type", "text/html; charset=utf-8")
    w.Header().Set("Cache-Control", h.CacheControl)
    w.Write([]byte(html))
    return nil
}

func (h *HtmlFromDuckDB) serveSearch(w http.ResponseWriter, r *http.Request, query string) error {
    // Basic sanitization
    query = strings.TrimSpace(query)
    if len(query) > 200 {
        query = query[:200]
    }
    
    // Call the DuckDB macro
    sql := fmt.Sprintf("FROM %s(term := ?, base_path := ?) SELECT html",
        sanitizeIdentifier(h.SearchMacro))
    
    var html string
    err := h.db.QueryRow(sql, query, h.BasePath).Scan(&html)
    if err != nil {
        return caddyhttp.Error(http.StatusInternalServerError, err)
    }
    
    // HTMX partial - no caching
    w.Header().Set("Content-Type", "text/html; charset=utf-8")
    w.Header().Set("Cache-Control", "no-cache")
    w.Write([]byte(html))
    return nil
}
```

---

## Tera Templates

Templates are stored in a `templates/` directory and referenced by the DuckDB macros.

### Index Template (`templates/index_template.html`)

```html
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>{{ title | default(value="Index") }}</title>
    <script src="https://unpkg.com/htmx.org@2.0.4"></script>
    <style>
        :root {
            --bg: #fafafa; --fg: #222; --link: #0066cc; 
            --border: #ddd; --muted: #666;
        }
        @media (prefers-color-scheme: dark) {
            :root { 
                --bg: #1a1a1a; --fg: #eee; --link: #6ab0f3; 
                --border: #444; --muted: #999; 
            }
        }
        * { box-sizing: border-box; }
        body { 
            font-family: system-ui, -apple-system, sans-serif; 
            max-width: 800px; margin: 0 auto; padding: 2rem 1rem;
            background: var(--bg); color: var(--fg); line-height: 1.6;
        }
        h1 { margin-bottom: 1.5rem; }
        
        .search-container { margin-bottom: 2rem; }
        .search-box input[type="search"] {
            width: 100%; padding: 0.75rem 1rem; font-size: 1rem;
            border: 1px solid var(--border); border-radius: 6px;
            background: var(--bg); color: var(--fg);
        }
        .search-box input:focus { outline: 2px solid var(--link); outline-offset: -1px; }
        
        .htmx-indicator { display: none; margin-left: 0.5rem; }
        .htmx-request .htmx-indicator { display: inline; }
        .htmx-request #search-results { opacity: 0.6; }
        
        .item-list { list-style: none; padding: 0; margin: 0; }
        .item { padding: 1rem 0; border-bottom: 1px solid var(--border); }
        .item:last-child { border-bottom: none; }
        .item-title { margin: 0; font-size: 1.1rem; }
        .item-title a { color: var(--link); text-decoration: none; }
        .item-title a:hover { text-decoration: underline; }
        .item-meta { font-size: 0.875rem; color: var(--muted); margin-top: 0.25rem; }
        .score { 
            background: var(--link); color: white; 
            padding: 0.125rem 0.5rem; border-radius: 4px; 
            font-size: 0.75rem; margin-left: 0.5rem;
        }
        
        .pagination { 
            display: flex; gap: 1rem; justify-content: center;
            margin-top: 2rem; padding-top: 1rem; 
            border-top: 1px solid var(--border);
        }
        .pagination a { color: var(--link); text-decoration: none; }
        .pagination a:hover { text-decoration: underline; }
        .pagination .current { font-weight: bold; }
        
        .empty-state { text-align: center; padding: 3rem 1rem; color: var(--muted); }
    </style>
</head>
<body>
    <h1>{{ title }}</h1>
    
    {% if search_enabled %}
    <div class="search-container">
        <div class="search-box">
            <input type="search" 
                   name="q" 
                   placeholder="Search content..." 
                   autocomplete="off"
                   hx-get="/{{ search_endpoint }}"
                   hx-target="#search-results"
                   hx-trigger="input changed delay:300ms, search"
                   hx-indicator=".htmx-indicator">
            <span class="htmx-indicator">Searching...</span>
        </div>
    </div>
    {% endif %}
    
    <div id="search-results">
        {% if items and items | length > 0 %}
        <ul class="item-list">
            {% for item in items %}
            <li class="item">
                <h2 class="item-title">
                    <a href="/{{ base_path }}/{{ item.id }}">{{ item.title }}</a>
                </h2>
                <div class="item-meta">ID: {{ item.id }}</div>
            </li>
            {% endfor %}
        </ul>
        {% else %}
        <div class="empty-state">
            <p>No pages available.</p>
        </div>
        {% endif %}
    </div>
    
    {% if pagination and total_pages > 1 %}
    <nav class="pagination">
        {% if prev_page %}
        <a href="/{{ base_path }}/?page={{ prev_page }}">← Previous</a>
        {% endif %}
        
        <span class="current">Page {{ current_page }} of {{ total_pages }}</span>
        
        {% if next_page %}
        <a href="/{{ base_path }}/?page={{ next_page }}">Next →</a>
        {% endif %}
    </nav>
    {% endif %}
</body>
</html>
```

### Search Results Template (`templates/search_results_template.html`)

This is an HTML fragment returned for HTMX to swap in:

```html
{% if query %}
<p style="margin-bottom: 1rem; color: var(--muted);">
    {% if total_results > 0 %}
    Found <strong>{{ total_results }}</strong> results for "{{ query }}"
    {% else %}
    No results for "{{ query }}"
    {% endif %}
</p>
{% endif %}

{% if items and items | length > 0 %}
<ul class="item-list">
    {% for item in items %}
    <li class="item">
        <h2 class="item-title">
            <a href="/{{ base_path }}/{{ item.id }}">{{ item.title }}</a>
            {% if item.score %}
            <span class="score">{{ item.score }}</span>
            {% endif %}
        </h2>
        {% if item.snippet %}
        <p class="item-snippet">{{ item.snippet }}</p>
        {% endif %}
    </li>
    {% endfor %}
</ul>
{% else %}
<div class="empty-state">
    {% if query %}
    <p>No results found. Try different keywords.</p>
    {% else %}
    <p>Start typing to search.</p>
    {% endif %}
</div>
{% endif %}
```

---

## Request Flow

### Index Page: `GET /works/`

```
Browser                    Caddy/Go                     DuckDB
   │                          │                            │
   │  GET /works/             │                            │
   │─────────────────────────▶│                            │
   │                          │  FROM render_index(        │
   │                          │    page := 1,              │
   │                          │    base_path := 'works'    │
   │                          │  ) SELECT html             │
   │                          │───────────────────────────▶│
   │                          │                            │
   │                          │◀───────────────────────────│
   │                          │      <html>...</html>      │
   │◀─────────────────────────│                            │
   │   Full HTML document     │                            │
```

### Search: `GET /works/search?q=dogs`

```
Browser (HTMX)             Caddy/Go                     DuckDB
   │                          │                            │
   │  GET /works/search?q=dogs│                            │
   │─────────────────────────▶│                            │
   │                          │  FROM render_search(       │
   │                          │    term := 'dogs',         │
   │                          │    base_path := 'works'    │
   │                          │  ) SELECT html             │
   │                          │───────────────────────────▶│
   │                          │                            │
   │                          │◀───────────────────────────│
   │                          │    <ul>..results..</ul>    │
   │◀─────────────────────────│                            │
   │   HTML fragment          │                            │
   │   (swapped into DOM)     │                            │
```

---

## URL Routing Summary

| URL Pattern | Handler | DuckDB Query | Response |
|-------------|---------|--------------|----------|
| `GET /works/` | Index | `FROM render_index(page := 1)` | Full HTML page |
| `GET /works/?page=2` | Index | `FROM render_index(page := 2)` | Full HTML page |
| `GET /works/search?q=dogs` | Search | `FROM render_search(term := 'dogs')` | HTML fragment |
| `GET /works/doc123` | Document | `SELECT html FROM html WHERE id = ?` | HTML content |

---

## Benefits of This Approach

| Aspect | Benefit |
|--------|---------|
| **Separation of concerns** | Rendering logic in SQL/Tera, routing in Go |
| **Easy customization** | Change templates without recompiling Go |
| **Testable** | Test macros directly in DuckDB CLI |
| **Flexible** | Different databases can have different macros |
| **Minimal Go code** | Just query execution and HTTP handling |
| **HTMX** | Dynamic search without custom JavaScript |

---

## Testing Locally

You can test the rendering macros directly in the DuckDB CLI:

```sql
-- Test index page
COPY (FROM render_index(page := 1)) 
TO '/tmp/index.html' (FORMAT csv, QUOTE '', HEADER false);

.sh xdg-open /tmp/index.html

-- Test search results
COPY (FROM render_search('dogs')) 
TO '/tmp/search.html' (FORMAT csv, QUOTE '', HEADER false);

.sh xdg-open /tmp/search.html
```

---

## File Structure

```
/srv/
├── works.db                    # DuckDB database with macros
└── templates/
    ├── index_template.html     # Full page template
    └── search_results_template.html  # HTMX fragment template
```

The Go module just needs to know:
1. Path to the database
2. Names of the macros to call
3. Which query parameters map to which macro arguments
