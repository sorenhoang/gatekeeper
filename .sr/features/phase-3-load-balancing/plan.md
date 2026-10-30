# phase-3-load-balancing — Plan

## Approach

Design B (G1): three named compose services (`order-service`, `order-service-2`, `order-service-3`) built from the same Dockerfile with `SERVICE=order-service`, listed one `server … resolve max_fails=1 fail_timeout=10s` line each in `upstream orders`, with `least_conn` and `keepalive 32`. The orders location adds `proxy_next_upstream error timeout http_502 http_503`, `proxy_next_upstream_tries 2`, `proxy_next_upstream_timeout 15s`; POST stays non-retried (nginx default). A backup server `127.0.0.1:8081 backup` points at an nginx-internal server that returns a degraded JSON 200. `keepalive 32` is added to the users and products upstreams too. See `design.md`.

Pattern to follow: Phases 1–2 smoke suite (`services/smoke/`, `smoke` / `smoke,lifecycle`, `restoreStack`, `withServiceEnv`, `writeConf` + `tempServer`, `timedGet`, `eventually`, `nginxCmd`). Algorithm comparisons use temporary `zz-smoke-*.conf` upstreams + servers on their own `server_name`, so the shipped upstream is never edited by a test.

## Affected files

| File | Action | Reason |
|---|---|---|
| `docker-compose.yml` | Modify | `order-service-2`, `order-service-3` (YAML anchor), gateway `depends_on` (F-1) |
| `nginx/conf.d/upstreams.conf` | Modify | `least_conn`, 3 servers with `max_fails`/`fail_timeout`, backup, `keepalive 32` on all upstreams (F-1, F-3) |
| `nginx/conf.d/default.conf` | Modify | `proxy_next_upstream*` in the orders location (F-2) |
| `nginx/conf.d/orders-fallback.conf` | Create | `listen 127.0.0.1:8081`, degraded JSON + `X-Degraded` (F-3) |
| `README.md` | Modify | Load balancing section, algorithm table, roadmap (F-4, F-5) |
| `services/smoke/replicas_test.go` | Create | F-1 spread, config, keepalive |
| `services/smoke/replicas_lifecycle_test.go` | Create | `withOrderReplicasEnv` helper (F-1) |
| `services/smoke/timeouts_lifecycle_test.go` | Modify | slow-orders test → all 3 replicas (agreed at G1) |
| `services/smoke/error_pages_lifecycle_test.go` | Modify | FAIL_RATE test → all 3 replicas (agreed at G1) |
| `services/smoke/failover_lifecycle_test.go` | Create | F-2 |
| `services/smoke/backup_lifecycle_test.go` | Create | F-3 |
| `services/smoke/algorithms_lifecycle_test.go` | Create | F-4 |
| `services/smoke/scale_lifecycle_test.go` | Create | F-5 |

## Dependencies & order

```
F-1 ──▶ F-2 ──▶ F-3
  └───▶ F-4 ──▶ F-5
```

- F-1 first: replicas and the upstream everything else uses.
- F-2 before F-3: retries decide when the backup is reached.
- F-4 needs the 3 replicas; F-5 last (README section documents F-1..F-4).

## Risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| `backup` not chosen when primaries have *no address* (resolve) rather than being ejected | Medium | High | F-3 tests exactly that case first; if nginx needs ejection instead, escalate before writing more |
| least_conn vs RR timing comparison flaky on a loaded laptop | Medium | Medium | compare request *shares* and p95 within one run, not absolute times; 60 requests at concurrency 6 |
| Distribution counts with tolerance still flaky (shared zone, retries) | Low | Low | ranges (8–12 of 30), sequential requests |
| Keepalive test: distinct ports bounded by worker count (CPU-dependent) | Medium | Low | assert `< 20` of 40, far above any realistic worker count here |
| Lifecycle runs get longer (3 more containers, scale, DELAY) | High | Low | keep delays short; chaos only on the replica that needs it |
| `max_fails` ejection from one test leaks into the next | Medium | Medium | `restoreStack` reloads nginx (resets peer state); tests don't rely on a specific replica right after a failure |
| `--scale order-service-3=2` conflicts with container names | Low | Medium | no `container_name` set anywhere (compose numbers replicas) |

## Out of scope

- k6 load tests in the automated suite (README shows the commands).
- Active health checks (Nginx Plus), cookie stickiness, `hash $request_uri consistent` (README mention only).
- Replicas for users/products/auth.
- Rate limiting (Phase 5) and caching (Phase 6) interplay with retries.
