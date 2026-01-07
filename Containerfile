# Build stage
FROM docker.io/library/golang:1.22-bookworm AS builder

WORKDIR /src

# Install build dependencies for CGO/DuckDB
RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    && rm -rf /var/lib/apt/lists/*

# Copy go mod files first for better caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build with CGO enabled for DuckDB
RUN CGO_ENABLED=1 go build -ldflags="-s -w" -o /caddy ./cmd/caddy

# Runtime stage
FROM docker.io/library/debian:bookworm-slim

# Install runtime dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Create non-root user
RUN useradd -r -u 1000 -s /sbin/nologin caddy

# Copy binary from builder
COPY --from=builder /caddy /usr/bin/caddy

# Set ownership and permissions
RUN chmod +x /usr/bin/caddy

# Create data directory
RUN mkdir -p /data && chown caddy:caddy /data

USER caddy

WORKDIR /data

EXPOSE 80 443 2019

ENTRYPOINT ["/usr/bin/caddy"]
CMD ["run", "--config", "/etc/caddy/Caddyfile"]
