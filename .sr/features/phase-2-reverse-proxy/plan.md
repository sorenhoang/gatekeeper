# phase-2-reverse-proxy — Plan

## Approach

Design A (G1): the gateway is the edge, so `X-Forwarded-For` and `X-Real-IP` are overwritten with `$remote_addr`, and `Forwarded` is cleared. All `proxy_set_header` lines live in `nginx/snippets/proxy-headers.conf`, included in every proxied location; nothing is set at server level, so no location can silently lose an inherited set. The snippet also switches upstream traffic to HTTP/1.1 with an empty `Connection` (ready for Phase 3 keepalive). Each API location sets its own `proxy_read_timeout`, plus a `$route` variable that the JSON error handlers (`error_page 502/503/504` → named locations) use for the body and `X-Route`. `proxy_intercept_errors` stays off so backend errors pass through. Buffering stays at nginx defaults; only request-body behaviour is observed. See `design.md`.

Pattern to follow: Phase 1's smoke suite (`services/smoke/`, tag `smoke`; `smoke,lifecycle` for anything that stops, freezes or recreates a container; `restoreStack` → `make up` in cleanup; `route`/`mustRoute`, `writeConf`, `nginxCmd`, `eventually`).

## Affected files

| File | Action | Reason |
|---|---|---|
| `nginx/snippets/proxy-headers.conf` | Create | Host, X-Forwarded-*, X-Real-IP, Forwarded "", HTTP/1.1, Connection "" (F-1) |
| `nginx/conf.d/default.conf` | Modify | include snippet in the 3 API locations, per-route timeouts, `set $route`, `error_page` + named JSON locations (F-1..F-3) |
| `README.md` | Modify | 502/504 cause table, timeouts per route, roadmap Phase 1 ☑ / Phase 2 ◐ (F-5) |
| `services/smoke/proxy_headers_test.go` | Create | F-1 |
| `services/smoke/timeouts_lifecycle_test.go` | Create | F-2 (`lifecycle`) |
| `services/smoke/error_pages_lifecycle_test.go` | Create | F-3 (`lifecycle`) + config check |
| `services/smoke/body_test.go` | Create | F-4 |
| `services/smoke/error_causes_lifecycle_test.go` | Create | F-5 (`lifecycle`), parses the README table |
| `services/smoke/readme_routes_test.go` | Unchanged | roadmap check expects Phase 1 ◐; F-5 updates README → this phase-1 test must change with it (see Risks) |

## Dependencies & order

```
F-1 ──▶ F-2 ──▶ F-3 ──▶ F-5
F-4 (independent; after F-1 so headers are in place)
```

- F-1 first: every proxied location gets the snippet before timeouts and error pages are added to the same locations.
- F-2 before F-3: the 504 JSON page is tested by the timeout F-2 introduces.
- F-5 last: its table documents causes produced by F-2/F-3.

## Risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Phase 1's `TestShouldMarkPhaseZeroDoneAndPhaseOneInProgress` expects Phase 1 ◐; marking it ☑ breaks a merged test | High | Low | F-5 rewrites that check to the new roadmap state, agreed here at G2 |
| The client IP nginx sees through Docker Desktop (`192.168.65.1`) differs per machine | High | Low | F-1 asserts "one valid IP, equal to X-Real-IP, not the forged one", never a literal address |
| Timing assertions flaky on a loaded laptop | Medium | Medium | lower bounds strict, upper bound +1s; delays chosen between route timeouts (3s vs 2s/15s) |
| `DELAY_MS` recreates the container → IP may move | Medium | Low | Phase 1 `resolve` + `make up` reload; tests poll with `eventually` before timing |
| Temp-file log line depends on `client_body_buffer_size` default (16k on 64-bit) | Low | Medium | 200 KB body is far above it; assert per-request via the X-Request-ID in the log line context |
| A temporary "wrong port" server for the refused case left behind by a killed test | Low | Medium | `zz-smoke-*.conf` naming, purged by `make up` and the phase-0 `init()` |
| Backend `500` swallowed by an `error_page 500` someone adds later | Low | Medium | F-3 asserts the service's own 500 body passes through |

## Out of scope

- `real_ip` / `$proxy_add_x_forwarded_for` (break-it experiment only, README note).
- Response buffering, `proxy_buffering off` (Phase 9 SSE).
- `client_max_body_size` changes and slow-client timeouts (Phase 5).
- `X-Request-ID` generation (Phase 4); upstream keepalive pool (Phase 3); TLS / `X-Forwarded-Proto: https` (Phase 7).
- A real 503 source (Phase 5 rate limiting).
