#! make

.PHONY: build clean test fmt

build:
	CGO_ENABLED=1 go build -ldflags="-s -w" -o caddy ./cmd/caddy

clean:
	rm -f caddy caddy-with-duckdb caddy-working
	go clean -cache -testcache

test:
	CGO_ENABLED=1 go test -v ./...

fmt:
	go fmt ./...
