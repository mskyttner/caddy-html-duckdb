# Build stage
FROM docker.io/library/golang:1.23-bookworm AS builder

# Allow Go to download required toolchain
ENV GOTOOLCHAIN=auto

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

# Create non-root user with home directory
RUN useradd -r -u 1000 -m -d /home/caddy -s /sbin/nologin caddy

# Copy binary from builder
COPY --from=builder /caddy /usr/bin/caddy

# Set ownership and permissions
RUN chmod +x /usr/bin/caddy

# Create directories for Caddy
RUN mkdir -p /data /config /etc/caddy /srv \
    && chown -R caddy:caddy /data /config /home/caddy /srv

# Set Caddy environment variables
ENV XDG_CONFIG_HOME=/config
ENV XDG_DATA_HOME=/data

# /srv is for user database files (can be read-only)
# /data is for Caddy internal storage (must be writable)
VOLUME ["/data", "/config", "/srv"]

USER caddy

WORKDIR /data

EXPOSE 80 443 2019

ENTRYPOINT ["/usr/bin/caddy"]
CMD ["run", "--config", "/etc/caddy/Caddyfile"]
