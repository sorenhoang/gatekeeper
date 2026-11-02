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
| TLS (local) | openssl (mkcert optional) |

## Layout

```
gatekeeper/
├── docker-compose.yml
├── Makefile                 # certs, up, down, reload, test, logs
├── scripts/certs.sh         # lab CA + gateway certificate (make certs)
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
└── certs/                   # made by make certs, git-ignored, mounted read-only
```

## Getting started

Requires Docker (with Compose) and Go 1.23+. Only the gateway is published: HTTPS on `localhost:8443` (the app) and plain HTTP on `localhost:8080` (redirects to HTTPS, plus `/health`); the services are reachable only inside the compose network. `make up` creates the lab certificates on first run (`certs/`, never committed); point curl at the lab CA once per shell with `export CURL_CA_BUNDLE=$PWD/certs/ca.pem` and the examples below work as written (checked with macOS curl 8.7; if your curl ignores the variable, add `--cacert certs/ca.pem`).

```bash
make certs     # lab CA + gateway certificate in certs/ (make up runs it)
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
| localhost:8443 | /api/users/42 | `users` — `^~ /api/users/` | user-service, sees `/42` |
| localhost:8443 | /api/users/ | `users` — `^~ /api/users/` | user-service, sees `/` |
| localhost:8443 | /api/users | `users` — `^~ /api/users/` (needs the slash) | nginx: 301 to `/api/users/` (automatic for a `proxy_pass` prefix ending in `/`) |
| localhost:8443 | /api/orders/7 | `orders` — `^~ /api/orders/` | order-service, sees `/7` |
| localhost:8443 | /api/products/7 | `products` — `^~ /api/products/` | product-service, sees `/7` |
| localhost:8443 | /api/products/logo.png | `products` — `^~` beats the asset regex | product-service, sees `/logo.png` |
| localhost:8443 | /api/usersX | `api-404` — `^~ /api/` (no slash after `users`) | nginx: JSON 404 |
| localhost:8443 | /api/unknown | `api-404` — `^~ /api/` | nginx: JSON 404 |
| localhost:8443 | /health | `health` — `= /health` beats `/` | nginx: `{"status":"ok","from":"gateway"}` |
| localhost:8443 | /healthz | `spa` — exact match needs the exact URI | nginx: `index.html` |
| localhost:8443 | /assets/app.css | `static` — regex `\.(css\|js\|png…)$` beats `/` | nginx: the file |
| localhost:8443 | /assets/missing.png | `static` — regex beats `/` | nginx: 404 from `try_files $uri =404`, never the shell |
| localhost:8443 | /settings | `spa` — no regex matches, so the longest prefix `/` wins | nginx: `index.html` (SPA fallback) |
| localhost:8443 | / | `spa` — `/` | nginx: `index.html` |
| admin.localhost | / | `admin` — admin server, `/` | nginx: `admin/index.html` |
| admin.localhost | /api/users/ | `admin` — the admin server has no API routes | nginx: 404 |
| unknown.test | / | `spa` — no `server_name` matches → `default_server` | nginx: `index.html` |

## Proxying

Every proxied location includes `nginx/snippets/proxy-headers.conf`; no `proxy_set_header` lives anywhere else, because one in a location would drop all the inherited ones.

What a backend receives (`curl -s https://localhost:8443/api/users/ | jq '.host, .headers'`):

| Header | Value | Why |
|---|---|---|
| `Host`, `X-Forwarded-Host` | the client's host (`localhost`) | not the upstream name (`users`) |
| `X-Forwarded-Proto` | `https` | the app is HTTPS-only since Phase 7 |
| `X-Forwarded-For`, `X-Real-IP` | the TCP peer of the gateway, one address | the gateway is the first hop: a client-sent value is **overwritten**, never appended |
| `Forwarded` | removed | same reason |
| `Connection` | empty, over HTTP/1.1 | ready for Phase 3's upstream keepalive |

Read timeouts per route (the longest silence allowed from the backend): users 2s, products 5s, orders 15s; connect 1s everywhere. Errors nginx itself generates come back as JSON, e.g. `{"error":"gateway timeout","status":504,"route":"users"}`; a backend's own error passes through unchanged.

Request bodies above 1 MB get a 413 before any backend is contacted; bodies above 16 KB are written to a temporary file first (`a client request body is buffered to a temporary file` in the gateway log).

## Why a 502 or a 504

Read the gateway error log (`docker compose logs gateway | grep error`). Each row is reproduced by the smoke suite.

| Cause | Status | Error-log phrase | How to reproduce |
|---|---|---|---|
| No address: the service's container is gone, so Docker DNS has no answer | 502 | `no live upstreams` | `docker compose stop user-service`, wait 3s, then `curl -i https://localhost:8443/api/users/` |
| Stale address: the IP nginx holds belongs to no container (the first ~2s after a stop, before nginx re-resolves) | 504 | `upstream timed out (110: Operation timed out) while connecting to upstream` | `docker compose stop user-service` and `curl` straight away (a race with the 2s re-resolve; the smoke test proxies to an unused IP to get it every time). Nothing refuses the connect, so it waits out `proxy_connect_timeout` (1s) |
| Connection refused: the container is up but nothing listens on that port | 502 | `connect() failed (111: Connection refused)` | point a `proxy_pass` at `user-service:9999` and reload |
| Timed out: the backend accepted the request but stayed silent past `proxy_read_timeout` | 504 | `upstream timed out (110: Operation timed out) while reading response header from upstream` | `DELAY_MS=3000 docker compose up -d user-service`, then `curl -i https://localhost:8443/api/users/x` |

## Load balancing

Orders runs as three named replicas (`order-service`, `order-service-2`, `order-service-3`), one `server … resolve` line each in `upstream orders`. They run the same binary: the echo's `service` is always `order-service`, and `hostname` (the container id) tells them apart.

```bash
for i in $(seq 1 12); do curl -s https://localhost:8443/api/orders/ | jq -r .hostname; done | sort | uniq -c
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

## Observability

**Request id.** Every response from a routed location carries an `X-Request-ID`. Errors nginx raises before it picks a location (`400` for a malformed request, `414` for a URI that's too long) don't. The gateway forwards the same id to the service, and the service logs it as `request_id=…`.
- **Client ids are kept only if well-formed:** an id you send is kept when it matches `^[A-Za-z0-9._-]{1,64}$`.
- **Anything else is replaced** with nginx's 32-hex `$request_id`: spaces, `=`, over 64 chars, or two headers. Such a value could forge fields in the services' space-separated `key=value` logs.

```bash
curl -si -H 'X-Request-ID: my-trace-1' https://localhost:8443/api/users/ | grep -i x-request-id
make trace ID=my-trace-1      # its gateway line and its service line
```

**Access log.** One JSON object per request (`escape=json`, every value a string). The gateway's own `/health` is not logged.
```bash
docker compose logs --no-log-prefix gateway | grep '^{' | tail -1 | jq .
```

| Field | Meaning |
|---|---|
| `time` | request end, ISO 8601 |
| `request_id` | the id above |
| `remote_addr`, `host`, `request`, `user_agent` | who asked for what |
| `status`, `bytes` | what the client got |
| `request_time` | first byte from the client → last byte sent to it: the whole request |
| `route` | the `$route` of the location (`users`, `orders`, `admin`, …) |
| `upstream` | upstream block name (`users`, `orders`, …). Empty when nginx answered itself, and also on the gateway's own JSON 502/504 lines: the `error_page` redirect clears it. Use `route` there |
| `upstream_addr`, `upstream_status` | which server answered, and what it said |
| `upstream_connect_time`, `upstream_header_time`, `upstream_response_time` | time to connect, to the first response byte, and to the whole response from the backend |

Values to expect in the `upstream_*` fields:
- **After a retry:** comma-separated lists, one value per server tried: `"172.19.0.6:8080, 127.0.0.1:8081"`.
- **Requests nginx answers itself** (SPA, static, JSON 404): `""`.
- **Something not measured:** a timing nginx never took (no connect happened, no header arrived) is `"-"`.
- **No server address at all:** when a service is down and nginx had no server to try, `upstream_addr` is the upstream's name (`"users"`) instead of an IP.

**Where the time went.**
- **The backend:** `upstream_response_time` is time spent waiting on the backend(s).
- **Everything else:** `request_time` minus that is client I/O (a slow upload or download) plus nginx's own work.
- **Example:** `request_time` 2s with `upstream_response_time` 0.05s means the service was fast and the client was slow.
- **Finding a slow service:** `DELAY_MS=1500 docker compose up -d product-service`, then read only the gateway log; products stands out at ≥ 1.5s. No service log is needed:
  ```bash
  docker compose logs --no-log-prefix gateway | grep '^{' \
    | jq -r 'select(.upstream_response_time != "") | "\(.route)\t\(.upstream_response_time)"' | sort | uniq -c
  ```
- **Caveat:** with large responses, a slow client can also slow nginx's reads from the backend (response buffering fills up), so treat the split as exact only for small responses.

**Counters.** nginx's `stub_status` page listens on `127.0.0.1:8082` inside the gateway only: not published, not reachable from the compose network. `make status` prints it:
```
Active connections: 2
server accepts handled requests
 120 120 340
Reading: 0 Writing: 1 Waiting: 1
```
A Prometheus exporter and Grafana are not part of the lab. To add them, run `nginx/nginx-prometheus-exporter` in compose with `--nginx.scrape-uri` pointing at a status listener it can reach.

## Rate limiting

| Limit | Applies to | Keyed by | Rate | Burst |
|---|---|---|---|---|
| login | `= /api/users/login` (and `…/login/`) | client IP | 5 r/s | `burst=10 nodelay` |
| api | `/api/users/`, `/api/orders/`, `/api/products/` (one shared bucket) | a known `X-API-Key`, otherwise client IP | 50 r/s | `burst=100 nodelay` |

Over the limit, the client gets `429 {"error":"too many requests","status":429,"route":"…"}` with `Retry-After: 1`, `X-Route` and `X-Request-ID`. The request never reaches a service. The access log records `"limit_req":"REJECTED"` (`PASSED` normally, `-` where no limit applies).

**A spike against `burst=10 nodelay`** (5 r/s):
- **Requests 1–11:** the 1st request plus 10 burst slots are answered at once.
- **The 11th** is the last burst slot.
- **The 12th** (and the rest) gets 429, because the bucket is full.
- **Refill:** the bucket frees one slot every 200ms, so 1s later 5 more are allowed.

Measured: 50 parallel logins → 11 × 200, 39 × 429.

**Without `nodelay`,** the same 11 are accepted but *queued*. They are released one every 200ms, so the 11th waits ~2s; the 12th still gets 429. `delay=N` mixes the two: the first N pass at once, the rest of the burst is queued.

**Dry run:** `limit_req_dry_run on;` lets everything through, and logs `REJECTED_DRY_RUN` for what would have been rejected. Use it to try a limit on real traffic first. Measured: 50 × 200, with 38–39 would-be rejections (one slot can refill while the spike arrives).

**Keys:**
- **Known keys** (`lab-key-alpha`, `lab-key-beta`, in `nginx.conf`) get their own budget.
- **Any other `X-API-Key` value** counts against the sender's IP. So inventing a new key per request buys nothing, and can't fill the zone's memory.
- **Lab only:** these keys live in a config file in this repo, so anyone who reads it can send them and get a second budget. Everyone using one key also shares that key's bucket. Phase 8 replaces them with real auth.

**Allowlist:** `127.0.0.0/8` and Docker's compose networks `172.16.0.0/12` are never limited.
- **Read from the TCP peer:** `X-Forwarded-For` or `X-Real-IP` can't claim it.
- **Your machine stays limited:** it arrives as `192.168.65.1` (Docker Desktop), so you can watch the limits from your shell.
- **Lab only:** `172.16.0.0/12` is a broad range. On native Linux Docker the host itself is `172.17.0.1`, so your shell would be exempt there and the limit tests would see no 429s. Some real networks use this range too. Docker can also hand out `10.x` or `192.168.x` compose subnets, and IPv6 (`::1`) isn't listed. Narrow it to your actual compose subnet anywhere real.

**What is not limited:**
- **The gateway's own answers:** `/health`, the SPA, static files, `admin.localhost` and `stub_status`.
- **Locations answered by `return`:** `limit_req` runs after nginx's rewrite phase, where `return` already answered. Measured: a `return` location with `limit_req` passed 50/50.

**Slow clients:** `client_header_timeout 5s` cuts off a client that stops sending its headers (no response is sent); `client_body_timeout 10s` does the same for a stalled body. The defaults are 60s.

**One gateway, one counter:** the zones live in this nginx's shared memory. Three gateways would each allow the full rate. Consistent limits across instances need a shared store (Redis), or sticky routing of each client to one gateway.

```bash
seq 1 50 | xargs -P50 -I{} curl -s -o /dev/null -w '%{http_code}\n' https://localhost:8443/api/users/login | sort | uniq -c
curl -si https://localhost:8443/api/users/login | grep -iE 'HTTP|retry-after'
```

## Caching

The gateway keeps a shared cache for `/api/products/*` only (`nginx/snippets/cache-products.conf`, zone `api_cache`, 100m on disk under `/var/cache/nginx/api`). Users and orders are never cached: their answers are per-user or change on every write.

| Rule | Setting | What you see |
|---|---|---|
| A 200 is kept for 30s | `proxy_cache_valid 200 30s` (products location) | first GET `X-Cache-Status: MISS`, repeats `HIT` |
| Key | `$scheme$host$request_uri` | the client's Host is part of the key, so one request with `Host: evil.example` can't plant a body every other client then gets |
| Credentials skip the cache | `proxy_cache_bypass` + `proxy_no_cache` on `Authorization`, `Proxy-Authorization`, `Cookie`, `X-API-Key` | `BYPASS` every time, and nothing is stored |
| A response that sets a cookie is never stored | nginx's default for `Set-Cookie` | `MISS` every time |
| Only GET/HEAD | nginx's default `proxy_cache_methods` | a POST is never a `HIT` |
| Stampede lock | `proxy_cache_lock on`, `proxy_cache_lock_timeout 5s` | 50 concurrent GETs of a cold key reach a slow service once |
| Stale on failure | `proxy_cache_use_stale error timeout updating http_500 http_502 http_503 http_504`, `proxy_cache_background_update on` | with product-service down, a key cached earlier is answered `STALE` (200); one never cached is the JSON 502 |

`X-Cache-Status` is `MISS`, `HIT`, `EXPIRED`, `STALE`, `UPDATING` or `BYPASS`; the access log has the same value in its `cache` field (empty for routes that aren't cached).

```bash
curl -si https://localhost:8443/api/products/demo | grep -i x-cache   # MISS
curl -si https://localhost:8443/api/products/demo | grep -i x-cache   # HIT
curl -si -H 'Authorization: Bearer x' https://localhost:8443/api/products/demo | grep -i x-cache   # BYPASS
```

Things to know:

- The credential list is an allowlist. A backend that reads some other auth header (`X-Auth-Token`, `X-Access-Token`, `Api-Key`, …) needs it added to the `$cache_skip` map in `nginx.conf`, or its personal answers would be shared.
- A HIT is the first requester's answer. The echo body of a HIT still shows that request's `X-Request-Id`, `X-Real-IP` and `User-Agent` (the response header `X-Request-ID` is fresh). That's harmless for a catalogue but is why anything per-user must not be cached.
- Rate limiting runs before the cache: a HIT still counts against the Phase 5 API budget (50 r/s per client), and a 429 has no `X-Cache-Status`.
- The lock waits at most 5s, the same as products' read timeout: if the service hangs, waiters then go to it themselves, so a hung service still sees a stampede.
- The stampede lock only matters when the service is slow: with a fast service, concurrent misses finish before the lock adds anything.

### gzip

`gzip on` for `application/json`, `text/css` and `application/javascript` from 256 bytes (`gzip_min_length 256`), with `gzip_vary on`. A client sending `Accept-Encoding: gzip` gets `Content-Encoding: gzip` and `Vary: Accept-Encoding`; one that doesn't gets plain bytes. `/health` (33 bytes) is never compressed: it would grow.

The cache stores the service's uncompressed body and nginx gzips each answer on the way out, so one entry serves both kinds of client. That relies on the services never gzipping themselves: a backend that did would have its gzip body handed to clients that didn't ask for it, because `Accept-Encoding` is not part of the key.

### Before and after

Measured with product-service slowed to 300ms (`DELAY_MS=300`), ten GETs of one products URL. "Before" sends an `Authorization` header so every GET bypasses the cache (`TestShouldPayTheServiceDelayOnEveryGetWithoutTheCache`); "after" is plain GETs (`TestShouldAnswerRepeatsFromTheCacheWithoutTheServiceDelay`):

| | Latency | Backend hits |
|---|---|---|
| before (cache bypassed: every GET) | 303–310ms each | 10 |
| after (first GET, `MISS`) | 309ms | 1 |
| after (GETs 2–10, `HIT`) | 0.2–0.5ms | 0 |

## TLS & HTTP/2

TLS ends at the gateway; the services stay plain HTTP/1.1 inside the compose network. Since Phase 7 the app is served on **HTTPS only**:

| Port (host → container) | Serves |
|---|---|
| `8443 → 443` | the app, TLS 1.2/1.3, HTTP/2 via ALPN (HTTP/1.1 still works) |
| `8080 → 80` | `/health` in plain HTTP; everything else is a redirect to the same path on `https://$host:8443` |

**Certificates.** `make certs` (run by `make up`) uses openssl to create a lab CA (`certs/ca.pem`, 10 years) and a leaf for `localhost`, `admin.localhost` and `127.0.0.1` (398 days, the longest the browsers accept). Valid files are kept; a leaf within 30 days of expiry is reissued from the same CA. Everything in `certs/` is gitignored. Clients verify with the lab CA: `curl --cacert certs/ca.pem` or `export CURL_CA_BUNDLE=$PWD/certs/ca.pem`; the smoke tests load the same file. For a browser without warnings, either import `certs/ca.pem` into your trust store or use mkcert instead (`mkcert -install` adds its own CA to the system store, which is why the lab doesn't do it for you).

**Redirects.** GET and HEAD get **301**; every other method gets **308**, because a 301 lets clients replay a POST as a GET and drop its body. The target is `https://$host:8443$request_uri`: inside the container the TLS port is 443, so `https://$host$request_uri` would send clients to port 443 on your machine, where nothing listens.

```bash
curl -si localhost:8080/api/users/x?q=1 | grep -iE '^HTTP|location'   # 301 → https://localhost:8443/api/users/x?q=1
curl -si -X POST -d '{}' localhost:8080/api/orders/ | grep -iE '^HTTP|location' # 308, same method and body
curl -s localhost:8443/ | grep -o 'The plain HTTP.*port'                # 400 page: "The plain HTTP request was sent to HTTPS port"
curl -sI https://localhost:8443/api/users/ | head -1                    # HTTP/2 200
curl -sI --http1.1 https://localhost:8443/api/users/ | head -1          # HTTP/1.1 200
```

**HSTS.** Every HTTPS answer from a location (pages, API, the JSON error pages) carries `Strict-Transport-Security: max-age=300`; plain HTTP answers don't (browsers ignore it there), and neither do errors nginx returns before choosing a location, such as the 400 below. The max-age is 5 minutes on purpose: a long one would pin `localhost` to HTTPS in your browser for every other local project you run. No `includeSubDomains`, so `admin.localhost` isn't pinned by the main host. Production would use a year and, once sure, preload.

**Protocol settings** (`nginx/snippets/tls.conf`, included by every 443 server with `http2 on;`):

- `ssl_protocols TLSv1.2 TLSv1.3`: a TLS 1.1 client fails the handshake (Go reports `protocol version not supported`, openssl `alert protocol version`).
- TLS 1.2 ciphers: the ECDHE-RSA AEAD suites of Mozilla's "intermediate" profile; TLS 1.3 chooses its own.
- `ssl_session_cache shared:SSL:10m`, `ssl_session_timeout 1d`, `ssl_session_tickets off`: resumption comes from the server-side cache, not from tickets whose key would never rotate here. Measured with `openssl s_client -connect localhost:8443 -tls1_2 -reconnect`: 5 of 5 reconnects print `Reused`.
- No OCSP stapling: a private CA has no OCSP responder.

**What changed for clients.**

- `X-Forwarded-Proto` is now `https`, so backends build https links.
- The cache key includes `$scheme`, so HTTPS entries are separate.
- Rate limits are per client IP whatever the protocol. Over HTTP/2 one connection carries every request, and each request still counts: the 12th quick login on one connection is a 429.

**Go clients and HTTP/2.** An `http.Transport` with its own `TLSClientConfig` stays on HTTP/1.1 unless `ForceAttemptHTTP2: true` is set; `http.DefaultTransport` already sets it. HTTP/2 setup also appends `h2` to the `tls.Config` it's given, so give each transport its own `Clone()`, or an HTTP/1.1-only transport sharing it will offer h2 it can't speak.

Not in this phase: HTTP/3/QUIC (nginx has the module, but neither the host curl nor the Go standard library can check it), mTLS to the services, and automatic trust-store installation.

## Roadmap

| Phase | Topic | Status |
|---|---|---|
| 0 | Foundation: repo skeleton, Go services, Nginx anatomy | ☑ |
| 1 | Static files & routing | ☑ |
| 2 | Reverse proxy deep dive: headers, timeouts, buffering | ☑ |
| 3 | Load balancing & failover | ☑ |
| 4 | Observability: logs, request ID, metrics | ☑ |
| 5 | Rate limiting & protection | ☑ |
| 6 | Caching | ☑ |
| 7 | TLS & HTTP/2 | ◐ |
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
