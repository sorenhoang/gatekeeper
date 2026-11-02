# phase-6-caching — Plan

## Approach

Design A (G1).

**In `nginx.conf` (http):**
- `proxy_cache_path /var/cache/nginx/api levels=1:2 keys_zone=api_cache:10m max_size=100m inactive=10m use_temp_path=off;`
- `map` → `$cache_skip`, non-empty when `Authorization`, `Cookie` or `X-API-Key` is present.
- gzip on, for `application/json text/css application/javascript`, `gzip_min_length 256`, `gzip_vary on`.
- `"cache":"$upstream_cache_status"` in the JSON log.

**New `nginx/snippets/cache-products.conf`:**
- `proxy_cache api_cache`
- `proxy_cache_bypass` / `proxy_no_cache $cache_skip`
- `proxy_cache_lock on`, `proxy_cache_lock_timeout 5s`
- `proxy_cache_use_stale error timeout updating http_500 http_502 http_503 http_504`
- `proxy_cache_background_update on`
- `add_header X-Cache-Status $upstream_cache_status always`

**The products location** includes the snippet and sets `proxy_cache_valid 200 30s`. Temporary test servers include the same snippet with a short TTL, so break-its exercise the shipped rules.

**Probes (temporary config, removed):**
- MISS→HIT works.
- `Authorization` / `Cookie` / `X-API-Key` give BYPASS, and nothing is stored.
- A 2s TTL shows EXPIRED after 3s.
- A `Set-Cookie` backend is never cached.
- An expired entry with the service stopped is served STALE; an uncached key gets the JSON 502.
- With a 500ms backend and 50 concurrent requests: lock → 1 backend hit (waiters finish in ~1.04s); no lock → 50 hits.
- With a fast backend the lock made no difference (1 hit either way), so the lock tests slow the backend down.
- The products echo body is 394 bytes, and `/health` is 33 bytes.

Pattern to follow: the smoke suite of Phases 1–5:
- helpers `withIDs`, `withServiceEnv`, `writeConf`, `logLinesWith`, `accessLines`/`linesFor`, `spike` (warmed connections)
- `afterDrain` for anything that sends more than ~50 requests to the API from the host, since the Phase 5 limit applies before the cache

## Affected files

| File | Action | Reason |
|---|---|---|
| `nginx/nginx.conf` | Modify | cache path, `$cache_skip` map, gzip, `cache` log field (F-1, F-3) |
| `nginx/snippets/cache-products.conf` | Create | shared cache rules (F-1, F-2) |
| `nginx/conf.d/default.conf` | Modify | products location: include snippet, `proxy_cache_valid 200 30s` (F-1) |
| `README.md` | Modify | Caching section, roadmap (F-3) |
| `services/smoke/cache_test.go` | Create | F-1 |
| `services/smoke/cache_lifecycle_test.go` | Create | F-2 (lock with DELAY, stale with service stopped) |
| `services/smoke/gzip_test.go` | Create | F-3 |
| `services/smoke/cache_numbers_lifecycle_test.go` | Create | F-3 before/after + Set-Cookie break-it |
| `services/smoke/timeouts_lifecycle_test.go` | Modify | slow-products test: unique URL (Phase 2, agreed at G1) |
| `services/smoke/trace_lifecycle_test.go` | Modify | slow-service test: unique URLs (Phase 4, agreed at G1) |
| `services/smoke/readme_routes_test.go` | Modify | roadmap: Phase 5 ☑, Phase 6 ◐ |

## Dependencies & order

```
F-1 ──▶ F-2 ──▶ F-3
```

- F-1 creates the cache zone, snippet and header that F-2 and F-3 use.
- F-3 last: the README documents the numbers of F-1 and F-2.
- Each item is locked and implemented immediately. Never lock ahead: locked tests of an unimplemented item fail every other item's full gate (the lesson from Phases 3–5).

## Risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Other merged tests repeat a products URL and now get HITs | Medium | Medium | gate runs the full suite; any such test gets a unique URL (setup only) |
| The Phase 5 API limit throttles cache tests (all from one IP) | Medium | Medium | bursts ≤ 50, `afterDrain`; lock and stale break-its on temporary servers |
| Cache state leaks between tests (same URL) | High | Medium | every test uses a unique path |
| Background update timing (unverified) | Medium | Low | poll up to 3s; if it doesn't hold, the line is reworded, not the config |
| A container restart in lifecycle tests empties the cache | Low | Low | tests populate their own entries after setup |

## Out of scope

- Purging (README: short TTL, versioned keys, third-party module).
- Browser caching of static assets (`expires`).
- Caching other routes; k6 runs in the automated suite.
