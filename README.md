# Gatekeeper

A hands-on lab for learning **Nginx** by building a small API gateway in front of a set of Go microservices.

The services are intentionally simple. The point of this project is the gateway: every phase adds one Nginx capability (routing, load balancing, rate limiting, caching, TLS, auth, realtime, tuning) and verifies it with real traffic, including deliberately breaking it to see how it fails.

## Architecture

```
                    ┌──────────────────────────────────────┐
  client ──HTTP(S)─▶│              Nginx (gateway)         │
                    │  routing · LB · rate limit · cache   │
                    │  TLS · auth_request · WS/SSE · logs  │
                    └──┬──────────┬──────────┬──────────┬──┘
                       │          │          │          │
                 /     │ /api/users│/api/orders│/api/products
                       ▼          ▼          ▼          ▼
                  frontend   user-svc   order-svc×3  product-svc
                  (static)                              │
                                 auth-svc ◀── auth_request (subrequest)
```

## Tech stack

| Part | Choice |
|---|---|
| Gateway | Nginx (open source, official Docker image) |
| Services | Go (standard library `net/http`; WebSocket and gRPC libraries only from Phase 9) |
| Orchestration | Docker Compose |
| Load testing | k6, `wrk`, `curl` |
| TLS (local) | mkcert |

## Layout

```
gatekeeper/
├── docker-compose.yml
├── Makefile                 # up, down, reload, test, logs
├── nginx/                   # mounted at /etc/nginx/gatekeeper (nginx -c .../nginx.conf)
│   ├── nginx.conf           # main context: workers, events, http
│   ├── conf.d/              # one file per virtual server
│   └── snippets/            # reusable blocks (proxy headers, ssl, cors)
├── services/                # one Go module, one Dockerfile (ARG SERVICE)
│   ├── go.mod
│   ├── Dockerfile
│   ├── internal/
│   │   └── echo/            # shared handler: /health, JSON echo, chaos, request log
│   ├── cmd/
│   │   ├── user-service/
│   │   ├── order-service/
│   │   ├── product-service/
│   │   └── auth-service/
│   └── smoke/               # stack tests run by `make test`
├── frontend/                # static files served directly by Nginx (SPA shell + assets/)
├── admin/                   # admin.localhost content, outside the main root
├── loadtest/                # k6 scripts
└── certs/                   # local certificates (git-ignored)
```

## Getting started

Requires Docker (with Compose) and Go 1.23+. Only the gateway is published, on `localhost:8080`; the services are reachable only inside the compose network.

```bash
make up        # build and start everything
make test      # smoke test the stack (needs `make up` first; briefly restarts user-service only)
make test-lifecycle  # make test + the tests that stop/recreate the stack
make reload    # nginx -t && nginx -s reload (no downtime)
make logs      # tail the gateway access log
make down      # stop and remove all containers
```

Make a backend slow or flaky (never affects `/health`):

```bash
DELAY_MS=500 FAIL_RATE=0.2 docker compose up -d order-service
docker compose up -d order-service    # back to normal
```

## Routes

Every location sets an `X-Route` response header, so `curl -sI <url> | grep -i x-route` shows which one won. The smoke suite checks every row below against the live gateway.

How Nginx picks a location: an exact `=` match wins at once. Otherwise it remembers the longest matching prefix; if that prefix is `^~`, it wins. If not, the first regex (`~`, `~*`) that matches, in file order, wins. Only when no regex matches does the remembered longest prefix win.

| Host | URL | Winning location (X-Route) | Answered by |
|---|---|---|---|
| localhost:8080 | /api/users/42 | `users` — `^~ /api/users/` | user-service, sees `/42` |
| localhost:8080 | /api/users/ | `users` — `^~ /api/users/` | user-service, sees `/` |
| localhost:8080 | /api/users | `users` — `^~ /api/users/` (needs the slash) | nginx: 301 to `/api/users/` (automatic for a `proxy_pass` prefix ending in `/`) |
| localhost:8080 | /api/orders/7 | `orders` — `^~ /api/orders/` | order-service, sees `/7` |
| localhost:8080 | /api/products/7 | `products` — `^~ /api/products/` | product-service, sees `/7` |
| localhost:8080 | /api/products/logo.png | `products` — `^~` beats the asset regex | product-service, sees `/logo.png` |
| localhost:8080 | /api/usersX | `api-404` — `^~ /api/` (no slash after `users`) | nginx: JSON 404 |
| localhost:8080 | /api/unknown | `api-404` — `^~ /api/` | nginx: JSON 404 |
| localhost:8080 | /health | `health` — `= /health` beats `/` | nginx: `{"status":"ok","from":"gateway"}` |
| localhost:8080 | /healthz | `spa` — exact match needs the exact URI | nginx: `index.html` |
| localhost:8080 | /assets/app.css | `static` — regex `\.(css\|js\|png…)$` beats `/` | nginx: the file |
| localhost:8080 | /assets/missing.png | `static` — regex beats `/` | nginx: 404 from `try_files $uri =404`, never the shell |
| localhost:8080 | /settings | `spa` — no regex matches, so the longest prefix `/` wins | nginx: `index.html` (SPA fallback) |
| localhost:8080 | / | `spa` — `/` | nginx: `index.html` |
| admin.localhost | / | `admin` — admin server, `/` | nginx: `admin/index.html` |
| admin.localhost | /api/users/ | `admin` — the admin server has no API routes | nginx: 404 |
| unknown.test | / | `spa` — no `server_name` matches → `default_server` | nginx: `index.html` |

## Proxying

Every proxied location includes `nginx/snippets/proxy-headers.conf`; no `proxy_set_header` lives anywhere else, because one in a location would drop all the inherited ones.

What a backend receives (`curl -s localhost:8080/api/users/ | jq '.host, .headers'`):

| Header | Value | Why |
|---|---|---|
| `Host`, `X-Forwarded-Host` | the client's host (`localhost`) | not the upstream name (`users`) |
| `X-Forwarded-Proto` | `http` | `https` from Phase 7 |
| `X-Forwarded-For`, `X-Real-IP` | the TCP peer of the gateway, one address | the gateway is the first hop: a client-sent value is **overwritten**, never appended |
| `Forwarded` | removed | same reason |
| `Connection` | empty, over HTTP/1.1 | ready for Phase 3's upstream keepalive |

Read timeouts per route (the longest silence allowed from the backend): users 2s, products 5s, orders 15s; connect 1s everywhere. Errors nginx itself generates come back as JSON, e.g. `{"error":"gateway timeout","status":504,"route":"users"}`; a backend's own error passes through unchanged.

Request bodies above 1 MB get a 413 before any backend is contacted; bodies above 16 KB are written to a temporary file first (`a client request body is buffered to a temporary file` in the gateway log).

## Why a 502 or a 504

Read the gateway error log (`docker compose logs gateway | grep error`). Each row is reproduced by the smoke suite.

| Cause | Status | Error-log phrase | How to reproduce |
|---|---|---|---|
| No address: the service's container is gone, so Docker DNS has no answer | 502 | `no live upstreams` | `docker compose stop user-service`, wait 3s, then `curl -i localhost:8080/api/users/` |
| Stale address: the IP nginx holds belongs to no container (the first ~2s after a stop, before nginx re-resolves) | 504 | `upstream timed out (110: Operation timed out) while connecting to upstream` | `docker compose stop user-service` and `curl` straight away (a race with the 2s re-resolve; the smoke test proxies to an unused IP to get it every time). Nothing refuses the connect, so it waits out `proxy_connect_timeout` (1s) |
| Connection refused: the container is up but nothing listens on that port | 502 | `connect() failed (111: Connection refused)` | point a `proxy_pass` at `user-service:9999` and reload |
| Timed out: the backend accepted the request but stayed silent past `proxy_read_timeout` | 504 | `upstream timed out (110: Operation timed out) while reading response header from upstream` | `DELAY_MS=3000 docker compose up -d user-service`, then `curl -i localhost:8080/api/users/x` |

## Roadmap

| Phase | Topic | Status |
|---|---|---|
| 0 | Foundation: repo skeleton, Go services, Nginx anatomy | ☑ |
| 1 | Static files & routing | ☑ |
| 2 | Reverse proxy deep dive: headers, timeouts, buffering | ◐ |
| 3 | Load balancing & failover | ☐ |
| 4 | Observability: logs, request ID, metrics | ☐ |
| 5 | Rate limiting & protection | ☐ |
| 6 | Caching | ☐ |
| 7 | TLS & HTTP/2 | ☐ |
| 8 | Auth at the gateway & CORS | ☐ |
| 9 | Realtime & gRPC: WebSocket, SSE, gRPC proxy | ☐ |
| 10 | Performance tuning & zero-downtime | ☐ |
| 11 | Stretch: njs, Kubernetes Ingress, Nginx vs Envoy | ☐ |

Detailed phase notes live in `docs/` (kept local, not committed).

## Learning method

Every phase follows the same loop:

1. **Build** the feature with the smallest config that works.
2. **Verify** it with `curl -v`, logs, or a load test.
3. **Break it** on purpose (wrong config, dead upstream, traffic spike) and observe.
4. **Fix and explain** why the fix works.
