# phase-1-routing — Plan

## Approach

Design B (G1): one `upstream` per API service with `zone` + `server <svc>:8080 resolve`, and `resolver 127.0.0.11 valid=5s` in `http`, so Docker DNS is re-queried and recreated/reordered services are picked up without a reload. Prefixes are stripped by the trailing-slash `proxy_pass http://<upstream>/` form. Every location sets `add_header X-Route <name> always;` so tests (and the learner) can see which location won. Admin is its own server block and its own directory outside the main root. See `design.md`.

Pattern to follow: phase 0's smoke suite (`services/smoke/`, build tag `smoke`, helpers `get`, `compose`, `mustCompose`, `writeConf`, `waitGatewayServing`). Tests that stop, remove or recreate containers go behind `smoke,lifecycle` (`make test-lifecycle`, also run by the harness gate) and restore the stack with `make up` in `t.Cleanup`.

## Affected files

| File | Action | Reason |
|---|---|---|
| `nginx/nginx.conf` | Modify | `resolver 127.0.0.11 valid=5s ipv6=off;` in `http` (F-1) |
| `nginx/conf.d/upstreams.conf` | Create | `upstream users/orders/products` with `zone` + `resolve` (F-1) |
| `nginx/conf.d/default.conf` | Modify | API locations, `= /health`, `/api/` JSON 404, SPA fallback, asset regex, `X-Route` (F-1..F-3) |
| `nginx/conf.d/admin.conf` | Create | `server_name admin.localhost`, root `/usr/share/nginx/admin` (F-4) |
| `admin/index.html` | Create | admin page, outside `frontend/` (F-4) |
| `frontend/assets/app.css` | Create | a real asset for the static/regex location (F-3) |
| `frontend/index.html` | Modify | link `assets/app.css` (F-3) |
| `docker-compose.yml` | Modify | mount `./admin:/usr/share/nginx/admin:ro` (F-4) |
| `services/smoke/routing_test.go` | Create | F-1 routing table tests |
| `services/smoke/routing_lifecycle_test.go` | Create | F-1 swap / stopped-service / restart tests (`lifecycle` tag) |
| `services/smoke/gateway_routes_test.go` | Create | F-2, F-3 tests |
| `services/smoke/admin_test.go` | Create | F-4 tests |
| `services/smoke/readme_routes_test.go` | Create | F-5: parse README route table, assert `X-Route` per row |
| `services/smoke/gateway_test.go` | Modify | `/nope → 404` becomes `/nope → 200 index.html` (agreed at G1) |
| `README.md` | Modify | Phase 1 route table, roadmap status (F-5) |

## Dependencies & order

```
F-1 ──▶ F-2 ──▶ F-3 ──┐
                      ├──▶ F-5
        F-4 ──────────┘
```

- F-1 first: the upstreams and `X-Route` convention everything else follows.
- F-2 before F-3: `/api/` and `= /health` must exist before the SPA catch-all, or `/api/usersX` and `/health` tests would see `index.html`.
- F-4 independent of F-1..F-3 (own server block); sequenced after F-3 to keep one `docker-compose.yml` edit at a time.
- F-5 last: the table documents the locations F-1..F-4 created.

## Risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| `resolve` + `zone` behaviour differs from docs in 1.27.5 (e.g. `nginx -t` still fails on an unresolvable name) | Low | High | F-1 tests `nginx -t`, `make reload` and gateway start with a service missing; fall back to escalation if it fails |
| Swap test flaky: Docker may not swap IPs every time | Medium | Medium | test asserts correctness ("each route reaches its own service within 10s"), not that a swap happened; log whether the IPs actually swapped |
| `valid=5s` window → asserted routes wrong for a few seconds after a swap | High | Low | poll up to 10s before asserting |
| Docker DNS answers AAAA / ipv6 causes resolve errors | Low | Medium | `ipv6=off` on `resolver` |
| `add_header` in a location silently drops server-level headers | Medium | Low | set `X-Route` (and `Cache-Control`) per location; tests assert both headers where they matter |
| Regex asset location shadows `/api/...` paths ending in `.js` | Medium | Medium | `/api/` locations use `^~` or the regex is ordered/scoped so APIs win; F-5 table includes `/api/products/x.png` |
| Phase-0 test change hides a real regression | Low | Low | rewritten test still asserts status and exact body |
| Lifecycle tests leave a service stopped | Medium | Medium | `t.Cleanup` runs `make up` whatever happens (phase-0 pattern) |

## Out of scope

- Proxy headers (`Host`, `X-Forwarded-*`), timeouts, buffering, JSON 502/504 pages (P2). Backends see `Host: users` until then.
- Replicas, LB algorithms, `keepalive` (P3).
- Request id generation and log formats (P4).
- Auth on the admin server and protected routes (P8); auth-service routes.
- `alias` in the committed config: it appears only in the break-it notes.
