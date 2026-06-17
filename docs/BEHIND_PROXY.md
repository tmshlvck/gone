# Running gone behind a reverse proxy (and source-IP gating)

A gone app is a plain `net/http` handler on a `chi` router, so it deploys
two ways:

1. **Standalone** — the app is the thing clients connect to directly
   (typically bound to an internal interface, reachable only from a known
   set of operator networks).
2. **Behind a reverse proxy** — Caddy, nginx, Apache, or HAProxy terminates
   TLS and forwards to the app over localhost.

The two modes need *different* wiring for one reason: **what the app sees as
the client's IP address.** Get this wrong and your access logs show the
proxy instead of the visitor, and a source-IP allow-list either matches the
wrong address or can be bypassed entirely. This doc covers both.

---

## The core fact: `RemoteAddr`

Go puts the peer of the TCP connection in `http.Request.RemoteAddr`, and
that is what `chi`'s `middleware.Logger` logs and what `site.AllowedIPs`
matches:

- **Standalone:** `RemoteAddr` is the real client. Everything just works.
- **Behind a proxy:** `RemoteAddr` is the *proxy* — usually `127.0.0.1`,
  `::1`, or a LAN address. The proxy passes the original client IP in an
  HTTP header (`X-Forwarded-For` / `X-Real-IP`) instead. Unless you read
  that header, every log line and every allow-list check sees the proxy.

`chi` ships the fix: **`middleware.RealIP`** rewrites `RemoteAddr` from the
`X-Real-IP` header, then `X-Forwarded-For` (first entry), then
`True-Client-IP`. Place it *before* `Logger` (and before `site.AllowedIPs`)
and the whole chain sees the real client.

> **Security warning — only enable `RealIP` behind a trusted proxy.**
> `RealIP` trusts those headers unconditionally. If the app is reachable
> directly, any client can send `X-Forwarded-For: 1.2.3.4` and forge its
> apparent address — defeating logging *and* `site.AllowedIPs`. Use it only
> when a proxy you control sits in front **and** that proxy overwrites the
> forwarded headers on ingress (see each config below). Never enable it in
> standalone mode.

---

## Wiring mode 1 — standalone, gated by source IP

No proxy, so no `RealIP`. `site.AllowedIPs` matches the real TCP peer, and
the logger records it too.

```go
mux := chi.NewRouter()
mux.Use(site.AllowedIPs(
    "203.0.113.0/24",   // office network (IPv4 CIDR)
    "2001:db8:42::/48", // office network (IPv6 CIDR)
    "198.51.100.7",     // a single jump host (bare IP == /32)
    "127.0.0.1", "::1", // localhost, for local testing
))
mux.Use(middleware.Logger)
// ... your routes
```

Entries are CIDR prefixes or bare addresses (a bare address is a single
host: `/32` for IPv4, `/128` for IPv6). IPv4 and IPv6 are both supported.
Anything outside the list gets `403 Forbidden`. Matching is a linear scan,
sized for a handful of operator prefixes — see
[`SITE.md`](SITE.md#allowedips--source-ip-allow-list) for the full contract.

---

## Wiring mode 2 — behind a reverse proxy

`RealIP` first (so `RemoteAddr` becomes the real client), then `Logger`, and
optionally `AllowedIPs` for defense in depth on top of any allow-listing the
proxy already does:

```go
mux := chi.NewRouter()
mux.Use(middleware.RealIP)            // RemoteAddr <- real client, from the proxy's header
mux.Use(middleware.Logger)            // now logs the client, not the proxy
mux.Use(site.AllowedIPs("203.0.113.0/24", "2001:db8:42::/48")) // optional, after RealIP
// ... your routes
```

Order matters: `RealIP` **must** precede `Logger` and `AllowedIPs`.

For this to be safe, the proxy must **set the forwarded header from the real
connection and not pass through a client-supplied one** — otherwise a client
could pre-set `X-Forwarded-For` and `RealIP` would believe it. The configs
below all do this.

### Caddy

`reverse_proxy` sets `X-Forwarded-For` / `X-Forwarded-Proto` / `-Host` from
the real connection by default, replacing any client-sent value. Minimal
Caddyfile:

```caddyfile
dashboard.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

`chi`'s `RealIP` reads `X-Forwarded-For`, which Caddy populates — nothing
extra to configure. (If you prefer `X-Real-IP`, add
`header_up X-Real-IP {remote_host}` inside the `reverse_proxy` block.)

### nginx

```nginx
server {
    listen 443 ssl;
    server_name dashboard.example.com;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host              $host;
        proxy_set_header X-Real-IP         $remote_addr;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

`$remote_addr` is the real peer, so `X-Real-IP` is trustworthy (and `RealIP`
prefers it). Note `$proxy_add_x_forwarded_for` *appends* to any inbound
`X-Forwarded-For`; because `RealIP` takes the first entry, prefer the
`X-Real-IP` header nginx sets here, or strip inbound XFF at your edge if the
app is internet-facing through nginx.

### Apache 2 (httpd)

Enable `proxy`, `proxy_http`, and `headers`, then:

```apache
<VirtualHost *:443>
    ServerName dashboard.example.com

    ProxyPreserveHost On
    ProxyPass        / http://127.0.0.1:8080/
    ProxyPassReverse / http://127.0.0.1:8080/

    # Overwrite any client-supplied header with the real peer, so RealIP
    # can trust it. mod_proxy_http also appends X-Forwarded-For itself.
    RequestHeader set X-Real-IP %{REMOTE_ADDR}s
    RequestHeader set X-Forwarded-Proto "https"
</VirtualHost>
```

`RequestHeader set` (not `add`) replaces any inbound `X-Real-IP`, closing the
spoofing gap. `RealIP` reads `X-Real-IP` first.

### HAProxy

```haproxy
frontend https-in
    bind *:443 ssl crt /etc/haproxy/certs/dashboard.pem
    # Drop any client-supplied forwarded headers before we set our own.
    http-request del-header X-Forwarded-For
    http-request del-header X-Real-IP
    default_backend dashboard

backend dashboard
    option forwardfor          # sets X-Forwarded-For to the real client
    http-request set-header X-Real-IP %[src]
    server app1 127.0.0.1:8080
```

Deleting the inbound headers first, then `option forwardfor` +
`set-header X-Real-IP %[src]`, guarantees `RealIP` sees the genuine source.

---

## Quick reference

| Deployment            | `middleware.RealIP`? | `AllowedIPs` matches | Logger shows |
|-----------------------|----------------------|----------------------|--------------|
| Standalone            | **No** (would allow spoofing) | real TCP peer | real client |
| Behind trusted proxy  | **Yes**, first       | real client (via header) | real client |
| Behind proxy, no RealIP | No                 | the proxy IP ❌      | the proxy ❌ |

If you take one thing away: **`RealIP` on iff there's a trusted proxy in
front; never both `AllowedIPs`-without-`RealIP`-behind-a-proxy (gates the
proxy, not the user) nor `RealIP`-while-directly-exposed (spoofable).**
