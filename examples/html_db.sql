install webbed from community;
install tera from community;

load webbed;
load tera;
load fts;

-- retrieve data to expose as searchable, browsable index

attach '/home/markus/repos/kthcorpus/data-raw/katharsis.db' as k;

create or replace table publications as (
    from k.publications
);

detach k;

-- table macro to expose each record in the data as a html page

create or replace macro render_html(id) as table(

  with o as (
    from (
      from publications
      where pid = id
      limit 1
    )
    select 
        id: pid, * exclude (pid), 
        Journal: null, 
        ArticleId: null,
        FreeFulltext: null,
        ResearchSubjects: null,
        Projects: null,
        Credits: null,
        Programme: null,
        Term: null,
        FridaLevel: null,
        Uppsok: null,
        ThesisLevel: null,
        PartOfThesis: null,
        DefencePlace: null,
        DefenceLanguage: null,
        DefenceDate: null,
        Urls: null,
  ),

  r as (
    from o
    select
      id, 
      o, 
      html: tera_render('record.html', o, template_path := 'templates/*.html')
  )

  from r 
  select
    id,
    html
);

/*

-- test locally
copy (
    from render_html(id := 1037586)
    select html
) 
to
'/tmp/tmp.html' (format csv, header false, quote '')
;

.sh xdg-open /tmp/tmp.html
*/

-- utility scalar function to extract text from html content
create or replace macro html_to_text(html) as (

    with h as (
        select a: parse_html(html)
    ),

    b as (
        from h
        select 
            s: unnest(html_to_duck_blocks(a))
    )

    from b
    select 
        text: trim(array_to_string(list(s.content), ' ')),
);

-- render full search index data (html for all records)

load tera;

create or replace table html as (

  with records as (
    from (
      from publications
      select id: PID, * exclude (PID),
      -- columns not yet populated with data but referenced in 
      -- record template
        Journal: null, 
        ArticleId: null,
        FreeFulltext: null,
        ResearchSubjects: null,
        Projects: null,
        Credits: null,
        Programme: null,
        Term: null,
        FridaLevel: null,
        Uppsok: null,
        ThesisLevel: null,
        PartOfThesis: null,
        DefencePlace: null,
        DefenceLanguage: null,
        DefenceDate: null,
        Urls: null,      
    ) c
    select
        i: c 
    ),

    o as (
        from records
        select 
            i,
            id: i.id, 
            html: tera_render('record.html', i, template_path := 'templates/*.html'),
    )

    from o 
    select
        id, 
        html
);

-- data to power the fts search functionality....
-- only the clean text from the html is used in the search
create or replace table c_head as (

    with t as (
        from html
        select 
            id: first(id), 
            h: first(html)
        group by id
        limit 100000
    )

    from t
    select 
        id, 
        text: html_to_text(h)
);

create or replace table c_tail as (

    with t as (
        from html
        select 
            id: first(id), 
            h: first(html)
        group by id
        offset 100000
    )

    from t
    select 
        id, 
        text: html_to_text(h)
);

create or replace table html as (
    from c_head
    union all 
    from c_tail
);

pragma create_fts_index(
    'html_clean', 'id', 'text', overwrite = 1
);

-- a table function which can be queried like 
-- "select * from html_search('search term')"
create or replace macro html_search(term := '') as table (
    from (
        from (
            from html_clean 
            select 
                id, 
                s: fts_main_html_clean.match_bm25(
                    id, 
                    term
                ),
                blurb: left(text, 50) || '...',
        )
        select
            id,
            blurb,
            maxs: max(s) over(),
            mins: min(s) over(),--where
            score: ((s - mins) / nullif((maxs - mins), 0))::decimal(3, 2), 
        order by score desc
    )
    select
        id, blurb, score
    where
        score is not null
    limit 20 
);

-- a table function which uses html_search to return HTML fragments
create or replace macro render_search(term := '', base_path := 'works') as table (

    with s as (
        from html_search(term)
    ),

    items as (
        select
            data: json_object(
                'query', term,
                'base_path', base_path,
                'total_results', (from s select count(*)),
                'items', (from s select json_group_array(json_object(
                    'id', id, 
                    'title', blurb, 
                    'snippet', null, 
                    'score', score
                    ))
                )
            )
    )

    from items
    select
        html: tera_render(
            'search.html', 
            data, 
            template_path := 'templates/*.html'
        )
);

/*
-- test rendering results locally

copy (
    from render_search('cats')
) to '/tmp/tmp.html' (format csv, quote '', header false)
;

.sh xdg-open /tmp/tmp.html
*/

-- table macro / pager for any table, using params ...
-- t for the table and per_page for the page length
create or replace macro html_index(t, per_page := 10) as table (

    with pages as (
        from query_table(t)
        select
            row_no: row_number() over (order by id),
            page_length: per_page::int,
            page_count: ceil((from html select count(*))::float / page_length)::int,
            page_no: (((row_no - 1) // page_length) + 1)::int,
            page_offset: (page_no  - 1) * page_length,
            *,
    )

    from pages
    select
        page_prev: case when page_no > 1 then page_no - 1 else null end,
        page_next: case when page_no < page_count then page_no + 1 else null end,
        *
);

-- table function to display paged content for the index page
create or replace macro render_index(page := 1, base_path := 'works', search_endpoint := 'search') as table (

    with pages as (
        from html_index('html_clean')
        select
            data: json_object(
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
                    select json_group_array(json_object(
                        'id', id,
                        'title', left(text, 63) || ' ...'
                    ))
            ))
        where page_no = (coalesce(page, 1))
        group by page_no, base_path
        order by page_no        
    )

    from pages
    select
        html: tera_render(
            'index.html',
            data,
            template_path := 'templates/*.html'
        )
    
);

/*
-- test locally
copy (
    from render_index()
) to '/tmp/tmp.html' (format csv, header false, quote '')
;

.sh xdg-open /tmp/tmp.html
*/

