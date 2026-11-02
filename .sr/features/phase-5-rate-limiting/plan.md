# phase-5-rate-limiting — Plan

## Approach

Design C (G1). In `nginx.conf` (http):
- `geo $limit_exempt` (127.0.0.0/8, 172.16.0.0/12 → 1) on the TCP peer.
- `map $http_x_api_key $known_api_key` (lab keys → themselves, else "").
- `map "$limit_exempt:$known_api_key" $api_limit_key` → "" when exempt, the known key, or `$binary_remote_addr`.
- `limit_req_zone $api_limit_key zone=api:10m rate=50r/s;`
- `limit_req_zone $login_limit_key zone=login:10m rate=5r/s;` (`$login_limit_key` = "" when exempt, else `$binary_remote_addr`).
- `limit_req_status 429;`
- `client_header_timeout 5s; client_body_timeout 10s;`
- `limit_req` added to the JSON log format.

In `default.conf`: `limit_req zone=api burst=100 nodelay;` in the three API locations; a new `location = /api/users/login` (`set $route login`, `limit_req zone=login burst=10 nodelay`, proxies to `users/login` with the same snippets/timeouts as users); `error_page 429 @too_many_requests` returning the JSON body with `Retry-After: 1`, `X-Route $route` and the response-headers snippet.

Probes run before this plan (temporary config, removed):
- `return`-only locations are never limited (50/50 passed).
- `burst=10 nodelay` → 11 pass / 39 × 429; without `nodelay` → 11 pass spread over 2.00s.
- `limit_req_dry_run` → 50 pass.
- A known key has its own bucket; random keys share the IP bucket.
- The compose network is exempt.
- Partial headers are cut at 5.0s.

Pattern to follow: Phases 1–4 smoke suite (`withIDs`, `accessLines`/`linesFor`, `logLinesWith`, `writeConf`, `restoreStack`, `compose exec gateway wget`). Tests that drain a bucket wait 2.5s in cleanup so later tests start with a full bucket.

## Affected files

| File | Action | Reason |
|---|---|---|
| `nginx/nginx.conf` | Modify | geo, maps, zones, `limit_req_status`, client timeouts, `limit_req` log field (F-1..F-4) |
| `nginx/conf.d/default.conf` | Modify | `limit_req` in API locations, `location = /api/users/login`, `@too_many_requests` (F-1, F-2) |
| `README.md` | Modify | Rate limiting section, roadmap (F-4) |
| `services/smoke/ratelimit_helpers_test.go` | Create | spike helper, bucket-drain cleanup (F-1) |
| `services/smoke/login_limit_test.go` | Create | F-1 |
| `services/smoke/api_limit_test.go` | Create | F-2 |
| `services/smoke/allowlist_test.go` | Create | F-3 |
| `services/smoke/ratelimit_breakits_test.go` | Create | F-4 (burst / nodelay / dry run on temporary servers) |
| `services/smoke/slow_client_test.go` | Create | F-4 |
| `services/smoke/readme_routes_test.go` | Modify | roadmap: Phase 4 ☑, Phase 5 ◐ (F-4) |

## Dependencies & order

```
F-1 ──▶ F-2 ──▶ F-3 ──▶ F-4
```

- F-1 adds the 429 page and `limit_req_status` that F-2 reuses.
- F-3 needs the API zone of F-2.
- F-4 last: README documents F-1..F-3. Each item's tests are locked and implemented immediately (never locked ahead — Phase 3/4 lesson).

## Risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Global limit throttles the existing suite (all tests share one IP) | Medium | High | burst=100 sized from the measured peak (40–60 per spike); gate runs the full suite; drain-cleanup waits 2.5s |
| A rate-limit test drains the shared IP bucket and the next test gets 429 | High | Medium | cleanup waits 2.5s (burst/rate + 0.5s) |
| Spike counts off by a few because the bucket refills during the spike | Medium | Low | bounds derived from spike duration (F-2), exact count only where probed (F-1: 11) |
| Lab API keys in a config file | Low | Low | lab-only keys, documented; Phase 8 replaces with auth |
| client_header_timeout 5s cuts a slow but honest client on a bad network | Low | Low | lab value, documented; production would be ~10–20s |

## Out of scope

- `limit_conn`, `limit_rate`, user-agent blocking (README concepts only).
- An API-key registry / auth (Phase 8).
- Limits shared across several gateways.
