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

## Load balancing

Orders runs as three named replicas (`order-service`, `order-service-2`, `order-service-3`), one `server … resolve` line each in `upstream orders`. They run the same binary: the echo's `service` is always `order-service`, and `hostname` (the container id) tells them apart.

```bash
for i in $(seq 1 12); do curl -s localhost:8080/api/orders/ | jq -r .hostname; done | sort | uniq -c
DELAY_MS=800 docker compose up -d order-service-2     # make one replica slow
```

Measured on this stack; numbers are typical single-run values (the smoke suite reproduces every row's direction, not the exact figures):

| Algorithm | Behaviour measured here | Pick it when |
|---|---|---|
| round-robin (default) | 30 sequential requests → 10/10/10. With one replica 800ms slow, it still gets 33% of traffic: mean latency 271ms | replicas are identical and requests cost about the same |
| `least_conn` (shipped for orders) | same slow replica gets 8% of traffic: mean latency 68ms. Sequential requests look like round-robin (every replica has 0 active, ties go round-robin) | request cost varies or a replica can turn slow; orders is allowed 15s, so a stuck replica must not keep its share |
| `ip_hash` | all 30 requests from the host land on one replica: every request comes from the same client IP | you need rough stickiness and clients come from many different networks: IPv4 is hashed on its first three octets, so a whole /24 (or everyone behind one NAT or load balancer) sticks to one server, and adding or removing a server remaps clients |

p95 doesn't separate the two here: `least_conn` still sends ~8% to the slow replica, and anything above 5% puts its 800ms into the p95.

**When a replica fails.**
- **Failure detection:** each server has `max_fails=1 fail_timeout=10s` (nginx's defaults, written down). One failure takes a replica out for 10s.
- **Stopping a replica rarely shows up as errors.** With `resolve`, its name leaves Docker DNS and nginx drops it within ~2s. In that window a GET that hits its old address waits out the 1s connect timeout, then nginx retries it on another replica.
- **A GET can still fail when a replica stops.** Measured with one sequential client: 0 or 1 failure per stopped replica. The failing request logs a connect timeout on the old address, then `no live upstreams` for its retry, although other replicas are up. We believe nginx refuses to pick a retry peer once the resolver has changed the upstream's server list mid-request. With concurrent traffic, every request waiting on the old address at that moment can fail the same way. Avoiding it fully would mean dropping `resolve`, which brings back Phase 1's swapped-IP problem.
- **Retry rules** (`nginx/snippets/upstream-retry.conf`, included by the orders location):
  - `proxy_next_upstream error timeout http_502 http_503`, with one retry (`tries 2`).
  - All tries share orders' 15s budget (`proxy_next_upstream_timeout 15s`).
- **What actually gets retried on orders:** fast failures only — a failed connect, or a replica answering 502/503. A GET that waits out orders' 15s read timeout has used the whole 15s retry budget, so the client gets a 504 with no retry. (The smoke test shows a read-timeout retry on a temporary server with a 1s read timeout.)
- **Retries and writes:** a POST is retried only if it never reached a server (a failed connect sent nothing). Once a POST was sent, it is never retried: the slow replica still runs it once, and the client gets a 504.
- **Recovery takes up to 10s.** A replica that failed while going down stays ejected for `fail_timeout` (10s) after it comes back, even on the same IP. Until then its traffic goes to the other replicas, or to the backup if none are left.

**Backup.** When all three replicas are out, the upstream's `server 127.0.0.1:8081 backup;` answers from inside the gateway (`nginx/conf.d/orders-fallback.conf`):
- **GET / HEAD:** `200 {"degraded":true,"service":"orders-fallback"}` with `X-Degraded: true` and `Cache-Control: no-store`.
- **Any write:** `503`. A success answer would read as "order created" while nothing was written.

**Connections.** `keepalive 32` on every upstream keeps idle connections to the services open, so 40 sequential requests reuse a handful of connections instead of opening 40. It only works because the proxy snippet speaks HTTP/1.1 with an empty `Connection` header.

**Scaling.** A replica added with `--scale` gets traffic within ~2s, with no reload: `resolve` re-reads Docker DNS.

```bash
docker compose up -d --no-deps --scale order-service-3=2 order-service-3   # 4 hostnames answer
docker compose up -d --no-deps --scale order-service-3=1 order-service-3   # back to 3, no failed request
```

## Roadmap

| Phase | Topic | Status |
|---|---|---|
| 0 | Foundation: repo skeleton, Go services, Nginx anatomy | ☑ |
| 1 | Static files & routing | ☑ |
| 2 | Reverse proxy deep dive: headers, timeouts, buffering | ☑ |
| 3 | Load balancing & failover | ◐ |
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
