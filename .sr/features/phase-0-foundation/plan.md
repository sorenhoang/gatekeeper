# phase-0-foundation — Plan

## Approach

Design C (G1): one Go module at `services/`, shared `internal/echo`, four `cmd/<svc>/main.go`, one Dockerfile with `ARG SERVICE`. Nginx config mounted as a directory at `/etc/nginx/gatekeeper`, `nginx:1.27-alpine` pinned. See `design.md`.

The repo is empty (only `README.md`, `.gitignore`, `.sr/harness.json`), so there is no existing pattern to follow; the Go code follows stdlib idioms (`net/http` ServeMux, `httptest`, `log/slog` not needed — plain `log.Printf` line).

**Test split.** `verify` (gofmt, vet, `go test ./...`, `docker compose config -q`) covers F-1/F-2 and compose syntax. F-3..F-5 need the running stack: their tests live in `services/smoke/smoke_test.go` behind `//go:build smoke`, so plain `go test ./...` skips them and `make test` runs `go test -tags smoke ./smoke/`. This replaces the `scripts/smoke.sh` named in the design: same role, but the harness only recognises `*_test.go` as tests.

## Affected files

| File | Action | Reason |
|---|---|---|
| `services/go.mod` | Create | module `gatekeeper/services`, go 1.23 |
| `services/internal/echo/echo.go` | Create | `/health` + catch-all echo handler (F-1) |
| `services/internal/echo/echo_test.go` | Create | F-1 tests |
| `services/internal/echo/middleware.go` | Create | `Config` from env, delay/fail, request log (F-2) |
| `services/internal/echo/middleware_test.go` | Create | F-2 tests |
| `services/internal/echo/server.go` | Create | `Run(name)`: load config, listen, graceful SIGTERM shutdown (F-3) |
| `services/cmd/{user,order,product,auth}-service/main.go` | Create | `echo.Run("<svc>")` (F-3) |
| `services/Dockerfile` | Create | `golang:1.23-alpine` → `distroless/static`, `ARG SERVICE` (F-3) |
| `docker-compose.yml` | Create | gateway + 4 services, no service ports, `restart: "no"` (F-3, F-4) |
| `nginx/nginx.conf` | Create | main/events/http, includes `gatekeeper/conf.d/*.conf` (F-4) |
| `nginx/conf.d/default.conf` | Create | `listen 80 default_server`, root `/usr/share/nginx/html` (F-4) |
| `nginx/snippets/.gitkeep` | Create | dir referenced by include, used from P2 |
| `frontend/index.html` | Create | welcome page (F-4) |
| `services/smoke/smoke_test.go` | Create | `//go:build smoke` stack tests for F-3..F-5 |
| `Makefile` | Create | `up down reload logs test` (F-5) |
| `README.md` | Modify | layout C tree, Go dependency note for P9 (F-5) |
| `docs/phases/00-foundation.md` | Modify (local, git-ignored) | break-it uses `nginx -s reload`, not `make reload` |

## Dependencies & order

```
F-1 ──▶ F-2 ──▶ F-3 ──┐
                      ├──▶ F-5
        F-4 ──────────┘
```

- F-1 → F-2 sequential (middleware wraps the handler).
- F-4 is independent of F-1..F-3 (gateway serves static files only) and can run in parallel; it needs `docker-compose.yml`, so whichever of F-3/F-4 lands second appends its services.
- F-5 last: `make test` runs the smoke tests of F-3 and F-4.

## Risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Timing assertions (DELAY_MS ≥200ms, health <50ms, shutdown <2s) flaky on a loaded laptop | Medium | Low | lower bound strict, upper bound generous; inject a fake sleep func in unit tests instead of wall clock where possible |
| `FAIL_RATE` randomness makes tests nondeterministic | Medium | Medium | only test 0 and 1 end-to-end; inject the rand source for fractional rates |
| Smoke tests run against a stack that isn't up → confusing failures | High | Low | `TestMain` probes `localhost:8080` once and fails with "run make up first" |
| Port 8080 already used on the host | Low | Medium | compose fails loudly; `GATEWAY_PORT` via `.env.example` if it happens |
| Go 1.27 locally vs `golang:1.23` image drift (`go.mod` directive newer than image) | Medium | Medium | `go.mod` says `go 1.23`; don't run `go mod tidy` with a toolchain directive bump |
| distroless has no shell → can't `exec` into services for debugging | High | Low | debug from the gateway container (`wget`), which is the P0 pattern anyway |

## Out of scope

- Routing `/api/*` to services (P1), proxy headers/timeouts (P2), upstreams (P3).
- Request id generation at the gateway (P4); P0 services only log what they receive.
- Compose healthchecks, restart policies, Prometheus.
- WebSocket, SSE, gRPC, `/login` `/verify` endpoints (P8/P9).
- Any frontend beyond one static `index.html`.
