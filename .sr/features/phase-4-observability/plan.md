# phase-4-observability — Plan

## Approach

Design B (G1): `map $http_x_request_id $req_id` keeps a client id matching `^[A-Za-z0-9._-]{1,64}$`, otherwise `$request_id`. `$req_id` is forwarded by the proxy snippet (`proxy_set_header X-Request-ID $req_id;` plus `proxy_hide_header X-Request-ID` so a backend can't add a second one) and returned by a new `snippets/response-headers.conf` included in every location that has an `add_header` (the inheritance trap makes a server-level header vanish — probed). A `log_format gatekeeper escape=json` replaces `combined`; `access_log … if=$loggable` skips `/health`. `stub_status` listens on `127.0.0.1:8082` only. The orders fallback server turns its access log off. `make trace ID=` and `make status` wrap the lookups. See `design.md`.

Probes run before this plan (temporary `zz-plan-probe.conf`, removed): `$request_id` = 32 hex; the map keeps a 64-char id and replaces 65 chars, spaces and a duplicated header; a server-level `add_header` disappears in a location with its own; a 413 keeps the location's `add_header`; unset upstream variables log as `""` under `escape=json`; `stub_status` is built in and serves on 127.0.0.1:8082.

Pattern to follow: Phases 1–3 smoke suite (`route`/`mustRoute`, `backendHeaders`, `logLinesWith`, `writeConf`, `retryServer`, `unusedIP`, `restoreStack`, `withServiceEnv`, `runMake`), and Phase 2's "every location includes the snippet" config test.

## Affected files

| File | Action | Reason |
|---|---|---|
| `nginx/nginx.conf` | Modify | `map $http_x_request_id $req_id`, `map $uri $loggable`, `log_format gatekeeper escape=json`, `access_log … gatekeeper if=$loggable` (F-1, F-2) |
| `nginx/snippets/response-headers.conf` | Create | `add_header X-Request-ID $req_id always;` (F-1) |
| `nginx/snippets/proxy-headers.conf` | Modify | `proxy_set_header X-Request-ID $req_id;`, `proxy_hide_header X-Request-ID;` (F-1) |
| `nginx/conf.d/default.conf` | Modify | include the response snippet in all 10 locations (F-1) |
| `nginx/conf.d/admin.conf` | Modify | include the response snippet (F-1) |
| `nginx/conf.d/orders-fallback.conf` | Modify | `access_log off;` (F-2) |
| `nginx/conf.d/status.conf` | Create | `listen 127.0.0.1:8082; location = /nginx_status { stub_status; }` (F-4) |
| `Makefile` | Modify | `trace` and `status` targets (F-3, F-4) |
| `README.md` | Modify | Observability section, roadmap (F-3) |
| `services/smoke/request_id_test.go` | Create | F-1 |
| `services/smoke/request_id_lifecycle_test.go` | Create | F-1 (JSON 502) |
| `services/smoke/access_log_test.go` | Create | F-2 |
| `services/smoke/access_log_lifecycle_test.go` | Create | F-2 (retry, degraded) |
| `services/smoke/trace_lifecycle_test.go` | Create | F-3 |
| `services/smoke/stub_status_test.go` | Create | F-4 |
| `services/smoke/readme_routes_test.go` | Modify | roadmap check: Phase 3 ☑, Phase 4 ◐ (F-3) |

## Dependencies & order

```
F-1 ──▶ F-2 ──▶ F-3
F-4 (independent)
```

- F-1 first: `$req_id` is what the log format records.
- F-2 before F-3: tracing and "find the slow service" read the JSON log.
- F-4 is independent; implemented last so README (F-3) can describe it — F-3's README line mentions stub_status, so F-4's config must exist before F-3 is gated, or F-3 goes after F-4. Order: F-1 → F-2 → F-4 → F-3.

## Risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Locking an item's tests before its code exists breaks other items' full-suite gates (Phase 3 lesson) | High | Medium | lock, then implement the same item immediately; never lock two items ahead |
| `docker compose logs` window catches lines from earlier tests | Medium | Low | every assertion matches a unique request id / marker |
| JSON log lines interleaved with nginx notice lines in `docker compose logs gateway` | High | Low | parse only lines starting with `{`; error log lines are plain text |
| A backend adding its own `X-Request-ID` → two headers to the client | Low | Medium | `proxy_hide_header X-Request-ID` in the proxy snippet; test asserts exactly one |
| `access_log if=` with a `map` on `$uri` misses `/health?x=1` | Low | Low | `$uri` excludes the query string; test both |
| Slow-service test timing on a loaded laptop | Low | Low | DELAY 1500ms vs users' < 0.5s: wide margin |

## Out of scope

- nginx-prometheus-exporter, Prometheus, Grafana (README shows how to add them).
- Log shipping/rotation, `traceparent` / OpenTelemetry headers.
- Changing the services' log format (they already log `request_id=`).
- Rate limiting (Phase 5), cache status in logs (Phase 6 adds `$upstream_cache_status`).
