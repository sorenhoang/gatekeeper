# phase-0-foundation — Design

## Problem

`make up` brings up, from a clean clone: an Nginx gateway on host `:8080` serving `frontend/`, and four Go services (`user`, `order`, `product`, `auth`) reachable only inside the compose network. No routing yet.

Each service: `GET /health` → `200 {"status":"ok"}`; any other path → JSON echo with `service`, `hostname`, `port`, `method`, `path`, `host`, `remote_addr`, `headers`. Env knobs `DELAY_MS` and `FAIL_RATE` (0–1, returns 500) apply to everything except `/health`. Every request is logged to stdout as one line including `X-Request-Id`.

`Makefile`: `up`, `down`, `reload`, `logs`, `test`. `make test` runs `scripts/smoke.sh`, a curl assertion script that every later phase appends to.

Constraints that are not in `docs/phases/00-foundation.md` but drive the design:
- Services diverge later on the roadmap (P8 auth `/login` `/verify`, P9 WS/SSE/gRPC, P10 `order-service-v2`).
- P4/P6 count backend hits from service logs → request logging with request id is a P0 deliverable.
- P8 relies on services being unreachable from the host → no published service ports.
- The official Debian nginx image lacks `ps`/`ss` (needed by P0/P10 experiments); editors that replace files break single-file bind mounts on reload.
- `make reload` runs `nginx -t` first, which hides the "bad config, old config keeps serving" experiment; the doc must call `nginx -s reload` directly for that.

## Approaches

### A — Four independent services
- **Summary:** `services/<svc>/{go.mod,main.go,Dockerfile}` ×4, as the README tree shows.
- **How:** each compose service builds its own context.
- **Pros:** fully independent; matches the published README.
- **Cons:** delay/fail/log middleware copied four times from day one; drift between copies.
- **Best when:** different teams own the services.

### B — One env-parameterized binary
- **Summary:** `services/echo/main.go`, built once, run four times with `SERVICE_NAME`/`PORT`.
- **How:** one compose `build:` reused via `image:`; env selects the name.
- **Pros:** smallest diff today.
- **Cons:** P8/P9 force either `if name == "auth"` branches inside one binary or a refactor into layout C.
- **Best when:** the services would never diverge.

### C — One Go module, shared `internal/echo`, `cmd/<svc>/main.go`
- **Summary:** `services/go.mod`, `services/internal/echo` (handler + middleware), `services/cmd/{user,order,product,auth}-service/main.go`, one `Dockerfile` with `ARG SERVICE`.
- **How:** each `main.go` (~10 lines) mounts `echo.Handler(name)` and later adds its own routes; compose passes `args: SERVICE=...`.
- **Pros:** shared behaviour written once; no refactor when a service grows unique endpoints; single `go build ./cmd/$SERVICE`.
- **Cons:** four tiny `main.go` files and a build arg that P0 alone does not need.
- **Best when:** divergence is already scheduled (it is).

## Supporting decisions

- **Nginx config mount:** mount `./nginx` → `/etc/nginx/gatekeeper:ro` and run `nginx -c /etc/nginx/gatekeeper/nginx.conf -g 'daemon off;'`. One directory mount, no stale inodes, image `mime.types` untouched. `nginx.conf` includes `gatekeeper/conf.d/*.conf` and `gatekeeper/snippets/*.conf`.
- **Image:** `nginx:1.27-alpine` pinned (busybox `ps`/`wget`, `nginx-debug`, ≥1.27.3 for P3 `resolve`).
- **Chaos knobs:** env, read at startup, fail fast on invalid values; changing them is `DELAY_MS=5000 docker compose up -d order-service`. No per-request overrides (they leak through the gateway path/args and pollute P6 cache keys).
- **Service build:** `golang:1.23-alpine` → `gcr.io/distroless/static`. No compose healthchecks (P3 teaches failure handling in Nginx; distroless has no curl). `restart: "no"` so dead containers stay visibly dead.
- **Graceful shutdown** on SIGTERM in the Go services to avoid the 10 s compose kill timeout on `make down`.
- **Frontend:** `frontend/index.html` mounted at `/usr/share/nginx/html:ro` is the P0 welcome page.
- **Verification:** `scripts/smoke.sh` with `check <url> <status> [header-regex]`; P0 asserts `/` → 200 and each service's `/health` via `docker compose exec gateway wget -qO- <svc>:8080/health`. Harness `verify` covers Go (`gofmt`, `vet`, `test`) and `docker compose config -q`; Nginx config is checked by `make test` because `nginx -t` needs the compose network from P2 on.
- **README:** update the planned tree to layout C and drop the "standard library only" promise to "stdlib, except WebSocket and gRPC in P9".

## Edge cases

- `r.Host` and `r.RemoteAddr` are not in `r.Header`; echo them explicitly (P2 depends on both).
- Go canonicalizes header names (`X-Request-Id`); use `r.Header.Get`, and grep case-insensitively in later phases.
- `hostname` is the 12-char container id; fine for P3 `uniq -c`.
- `FAIL_RATE` outside 0–1 or non-numeric → exit non-zero at startup, never silently 0.
- `/health` must never be delayed or failed.
- Host `:8080` in use → compose fails loudly; `GATEWAY_PORT` override via `.env.example` only if needed.
- Apple Silicon: both base images are multi-arch; no `platform:`.
- Break-it experiment must use `docker compose exec gateway nginx -s reload`, not `make reload`.

## Recommendation

**C.** The divergence of the services is on the roadmap, not speculative, so the ~30 extra lines now remove a guaranteed refactor at P8/P9.

## Decision

C — one Go module, shared `internal/echo`, `cmd/<svc>/main.go`, one Dockerfile with `ARG SERVICE`. Harness enabled on `main`. User's words: "Yes, on main (Recommended)" / "C: one module, cmd/<svc> (Recommended)".
