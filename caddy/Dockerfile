# caddy/Dockerfile
#
# Builds Caddy with the DuckDNS DNS provider plugin.
#
# The DuckDNS plugin allows Caddy to use the DNS-01 ACME challenge to obtain
# Let's Encrypt certificates. This is the preferred method for homelab setups
# because it works without exposing port 80 — only port 443 needs to be
# forwarded on your router.
#
# Official Caddy builder image uses xcaddy to compile with extra modules.
# See: https://caddyserver.com/docs/build#xcaddy

FROM caddy:2-builder AS builder

RUN xcaddy build \
    --with github.com/caddy-dns/duckdns

FROM caddy:2-alpine

COPY --from=builder /usr/bin/caddy /usr/bin/caddy