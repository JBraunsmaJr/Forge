# HTTPS and Reverse Proxy Guide

This guide explains how to enable HTTPS for your Forge instance, whether you're using the built-in Caddy configuration or an existing reverse proxy in your homelab.

---

## Architecture Overview

For a fully functional HTTPS setup, only the **Scheduler** needs to be reachable over TLS. 

1.  **Scheduler (Web UI/API/WebSockets)**: Served on port `443`.

All traffic, including Web UI, REST API, and Debug Terminal WebSockets, routes through the scheduler. The scheduler then proxies WebSocket traffic to the appropriate agent via an internal connection. This "reverse connection" model ensures that agents do not need to expose any ports to the public internet or the browser.

---

## Option 1: Using your Existing Reverse Proxy (Recommended)

If you already have a Caddy, Nginx, or Traefik instance running in your homelab, you should use it to handle TLS termination.

### Caddy Configuration

Add the following to your `Caddyfile`:

```caddy
# forge.lab.yourAwesomeDomain.net
forge.WHATEVER-SUB-DOMAIN.YOUR-DOMAIN-HERE {
    # 1. gRPC traffic (Agents)
    @grpc {
        header Content-Type application/grpc*
    }
    reverse_proxy @grpc h2c://<forge-host-ip>:50051

    # 2. Proxy all other traffic to the Forge Scheduler
    reverse_proxy <forge-host-ip>:8080 {
        # Critical: prevent buffering for real-time log streams (SSE)
        flush_interval -1
    }
}
```

### Required Environment Variables (.env)

When using an external proxy, update your `.env` file so Forge knows its public URL:

```bash
# The public URL of your scheduler (used by Web UI and CLI)
FORGE_BASE_URL=https://forge.WHATEVER-SUB-DOMAIN.YOUR-DOMAIN-HERE

# The URL agents use to connect to the scheduler
# If the agent is on a different network, use the public HTTPS URL:
FORGE_SCHEDULER_URL=https://forge.WHATEVER-SUB-DOMAIN.YOUR-DOMAIN-HERE
```

Since WebSockets are proxied through the scheduler, you do **not** need to configure `FORGE_AGENT_WS_ADDR`.

---

## Agent Configuration for HTTPS

If your scheduler is served over HTTPS, agents must connect using secure protocols.

1.  **Set `FORGE_SCHEDULER_URL`**: Use the `https://` prefix.
2.  **gRPC over TLS**: Forge agents automatically detect `https://` in the scheduler URL and switch to gRPC over TLS. By default, they will connect to port `443` of your domain.
3.  **Proxy Configuration**: Your reverse proxy must be configured to forward gRPC traffic (usually identified by `Content-Type: application/grpc`) to the scheduler's gRPC port (default `50051`).

Example for a remote agent:
```bash
FORGE_SCHEDULER_URL=https://forge.WHATEVER-SUB-DOMAIN.YOUR-DOMAIN-HERE
FORGE_API_TOKEN=your-agent-token
```

---

## Option 2: Using the Project's Caddy (Cloudflare)

If you want to use the Caddy stack included in this project with Cloudflare DNS-01 challenge (no port 80 opening required):

1.  **Generate a Cloudflare API Token**: Go to your Cloudflare profile → API Tokens → Create Token → Use "Edit zone DNS" template.
2.  **Configure `.env`**:
    ```bash
    FORGE_DOMAIN=forge.WHATEVER-SUB-DOMAIN.YOUR-DOMAIN-HERE
    CLOUDFLARE_API_TOKEN=your-token-here
    ACME_EMAIL=your-email@example.com
    ```
3.  **Start with the Cloudflare compose file**:
    ```bash
    docker compose -f compose.yml -f compose.cloudflare.yml up -d
    ```

### Automatic Configuration

The included `compose.cloudflare.yml` and `compose.https.yml` files automatically configure the built-in Caddy with the correct DNS provider and token mapping. You do **not** need to edit the `Caddyfile` unless you have custom requirements.

---

### Cloudflare & gRPC
If you are using Cloudflare, ensure that **gRPC** is enabled in the Cloudflare Dashboard:
1. Go to **Network**.
2. Enable the **gRPC** toggle.

Without this, Cloudflare may terminate gRPC streams or fail to route them correctly.

### gRPC Keepalives
Forge uses gRPC keepalives to prevent proxies (like Cloudflare or Caddy) from closing idle connections. The default settings are:
- **Ping interval**: 10 seconds
- **Ping timeout**: 5 seconds

If you still experience disconnected agents, check your proxy's idle timeout settings. In Caddy, gRPC streams should stay open as long as the backend supports them.

---

## Troubleshooting

### Agent says "dialing gRPC" but never connects
1.  **Check gRPC on Proxy**: Ensure your reverse proxy is forwarding to the gRPC port (`50051`) and not the HTTP port (`8080`).
2.  **Cloudflare gRPC**: Ensure the gRPC toggle is ON in the Cloudflare Network settings.
3.  **h2c**: Ensure Caddy is using `h2c://` for the gRPC backend. This is required because the scheduler's gRPC port does not use TLS internally.
4.  **Registration Logs**: Check the scheduler logs. You should see `[grpc] new connection attempt from ...`. If you don't see this, the traffic is not reaching the scheduler.

### Debug Terminal "Connection Refused"
If you can see logs but the "Debug" terminal fails to connect:
1.  Check that your reverse proxy allows WebSocket upgrades.
2.  In Caddy, this works by default. In Nginx, you need `proxy_set_header Upgrade $http_upgrade;`.
3.  Ensure the browser console doesn't show mixed content errors (connecting to `ws://` from an `https://` page). Forge handles this automatically if `FORGE_BASE_URL` is set to `https`.

### SSE Logs Not Streaming
If logs only appear in "chunks" or after a job finishes:
1.  Ensure your proxy has buffering disabled. 
2.  In Caddy, use `flush_interval -1` in the `reverse_proxy` block.
3.  In Nginx, use `proxy_buffering off;`.

### Artifacts "Blocked Mixed-Content"
If you can download artifacts but "viewing" them fails with a mixed-content error in the browser console:
1.  **Check `FORGE_BASE_URL`**: Ensure it is set to your public `https://` URL. Forge uses this to generate download links.
2.  **S3/Minio Direct vs Proxy**: 
    - If `FORGE_S3_PUBLIC_URL` is set to an `http://` address (like `http://<hostname>:9000`), the browser will block it when the dashboard is on `https://`.
    - **Recommended Fix**: Unset `FORGE_S3_PUBLIC_URL`. When this is unset, Forge automatically proxies artifact downloads through the scheduler. This ensures the browser only sees requests to your main `https://` domain.
    - **Alternative**: Enable HTTPS for your Minio instance and set `FORGE_S3_PUBLIC_URL` to the `https://` address.
3.  **Relative Path Fix**: Recent versions of Forge UI automatically convert same-host URLs to relative paths to avoid mixed content. Ensure you have rebuilt your images with `docker compose build --no-cache`.
