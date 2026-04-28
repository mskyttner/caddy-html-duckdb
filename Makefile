#! make

.PHONY: build clean test fmt

build:
	CGO_ENABLED=1 go build -ldflags="-s -w" -o caddy ./cmd/caddy

clean:
	rm -f caddy caddy-with-duckdb caddy-working
	go clean -cache -testcache

test:
	CGO_ENABLED=1 go test -v ./...

test-container:
	docker run --rm -p 8090:8080 \
		-e DATABASE_PATH=rendered_works.db \
		-e TABLE=html \
		-e ID_COLUMN=id \
		-e INDEX_ENABLED=true \
		-e ROUTE_PATH='/works/*' \
		-e READ_ONLY=false \
		-e INIT_SQL_COMMANDS_FILE=.duckdbrc \
		-e SEARCH_ENABLED=true \
		-v ./data:/srv ghcr.io/mskyttner/caddy-html-duckdb:main

fmt:
	go fmt ./...
