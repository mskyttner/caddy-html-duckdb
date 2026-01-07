#! make

build:
	CGO_ENABLED=1 go build -ldflags="-s -w" -o caddy ./cmd/caddy
