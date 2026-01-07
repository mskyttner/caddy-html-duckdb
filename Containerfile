FROM docker.io/library/golang:1.23-bookworm AS builder

ENV GOTOOLCHAIN=auto
WORKDIR /src

RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    && rm -rf /var/lib/apt/lists/*

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 go build -ldflags="-s -w" -o /caddy ./cmd/caddy

FROM docker.io/library/debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

RUN useradd -r -u 1000 -m -d /home/caddy -s /sbin/nologin caddy

COPY --from=builder /caddy /usr/bin/caddy
RUN chmod +x /usr/bin/caddy

RUN mkdir -p /data /config /etc/caddy /srv \
    && chown -R caddy:caddy /data /config /home/caddy /srv

ENV XDG_CONFIG_HOME=/config
ENV XDG_DATA_HOME=/data

VOLUME ["/data", "/config", "/srv"]

USER caddy
WORKDIR /srv

EXPOSE 80 443 2019

ENTRYPOINT ["/usr/bin/caddy"]
CMD ["run", "--config", "/etc/caddy/Caddyfile"]
