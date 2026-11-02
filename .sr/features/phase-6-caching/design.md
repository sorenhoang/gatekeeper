# phase-6-caching — Design

## Problem

From `docs/phases/06-caching.md`: product-service responses are cached at the gateway; latency and backend load drop measurably.
- Cache `GET /api/products/*` for 30s, expose `X-Cache-Status`.
- Never cache requests with `Authorization`.
- Serve stale when product-service is down.
- gzip for JSON/JS/CSS.
- Break-its: 200 concurrent requests on a cold key with and without `proxy_cache_lock` (count backend hits); stop product-service → stale; a backend `Set-Cookie` → nothing cached.
- Done when: a before/after latency number and backend request count.

Starting point (Phases 0–5, merged): `^~ /api/products/` proxies to `upstream products` (one replica, `resolve`, `keepalive 32`), 5s read timeout, `limit_req zone=api burst=100 nodelay`, JSON error pages, `X-Route`/`X-Request-ID` snippets, JSON access log with `route`, `upstream_*` and `limit_req`.

Constraints found while grounding:
- **The echo body reflects the request.** product-service answers with the request's headers (`X-Forwarded-For`, `X-Request-Id`, `X-Api-Key`, `Authorization`, `Cookie`, …), `remote_addr` and `hostname`. A shared cache stores one client's echo and serves it to the next — including any credential that client sent. "Never cache with `Authorization`" alone is not enough: `Cookie` and Phase 5's `X-API-Key` carry credentials too.
- **Merged tests assume products is never cached.** Phase 2's `TestShouldCutOffASlowProductsBackendAtFiveSeconds` (DELAY_MS=7000 → 504 at 5s) and Phase 4's `TestShouldFindTheSlowServiceFromGatewayLogFieldsAlone` (reads `upstream_response_time` for `/api/products/x`) would get fast HITs on a repeated URL, and stale serving would turn the 504 into a 200. Phase 1–5 tests that only check status or routing still pass on a HIT.
- **Rate limiting runs before the cache.** `limit_req` is a preaccess-phase check, so cached hits still spend the client's API budget, and the notes' "200 concurrent on a cold key" from one host IP would mostly be 429s (burst 100). Break-its need their own temporary servers (as in Phase 5).
- **With `resolve`, a stopped product-service is "no live upstreams"** (502-class error) or, in the first ~2s, a connect timeout — both are covered by `proxy_cache_use_stale error timeout …`.
- **Per-request fields in a cached body go stale on purpose**: a HIT's echo shows the original request's `X-Request-Id` while the response header carries the new one. That's the honest picture of caching a non-cacheable endpoint, and the README must say so.
- **nginx skips caching on its own** when the upstream sends `Set-Cookie`, `Cache-Control: private/no-store/no-cache`, `Expires` in the past or `Vary: *` — the notes' third break-it. The echo service sends none of these today.
- **The cache directory** `/var/cache/nginx` exists in the image and isn't persisted: a container restart (not a reload) empties it.
- Phase 4's access log should show the cache result: `$upstream_cache_status`.

Out of scope: purging (no built-in purge in open-source nginx; README covers short TTL / versioned keys), browser caching of static assets (`expires`), k6 load tests in the automated suite.

## Approaches

The main decision is **who may share a cached response**.

### A — Shared cache; never store or serve for requests carrying credentials
- **Summary:** one cache entry per URL for everyone; `proxy_cache_bypass` and `proxy_no_cache` on `$http_authorization$http_cookie$http_x_api_key`.
- **How:** key `$scheme$proxy_host$request_uri`; a credentialed request always goes to the service and its answer is never stored, so no credential can end up in a shared entry.
- **Pros:** the phase's goal — one backend hit serves everyone; the standard gateway rule ("public GETs only"); stampede and stale behaviour are meaningful.
- **Cons:** cached echoes still show other clients' non-credential headers (`User-Agent`, `X-Forwarded-For`); a client using an API key never gets cached answers (Phase 5's known-key users pay full latency).
- **Best when:** the cached endpoint serves public data (a product catalogue).

### B — Per-client cache (client IP or API key in the key)
- **Summary:** key `$scheme$proxy_host$request_uri$binary_remote_addr` (or the API key).
- **How:** each client only ever sees its own cached answers.
- **Pros:** no cross-client leakage at all, even of reflected headers.
- **Cons:** the hit rate collapses to one client's repeats — the shared-load benefit the phase measures disappears; zone memory grows with clients × URLs; **misleading in this lab**: every host request has the same IP, so tests would look like a shared cache.
- **Best when:** responses are per-user but expensive to compute.

### C — Cache only what the service marks cacheable
- **Summary:** gateway uses `proxy_cache` but no `proxy_cache_valid`; the service decides with `Cache-Control: public, max-age=30`.
- **How:** nginx honours upstream cache headers by default; product-service would add the header on catalogue paths only.
- **Pros:** correct HTTP semantics; the owner of the data decides; nothing else is ever cached by surprise.
- **Cons:** changes product-service (Phase 0 code, shared `internal/echo`); the gateway lesson becomes mostly about the service; the 30s rule from the notes lives in Go.
- **Best when:** you control the services and want caching policy next to the data.

## Supporting decisions (to confirm with G1)

1. **Zone and path:** `proxy_cache_path /var/cache/nginx/api levels=1:2 keys_zone=api_cache:10m max_size=100m inactive=10m use_temp_path=off;` (100m is plenty for echo bodies; the notes' 1g is a production figure).
2. **Products only:** `proxy_cache api_cache; proxy_cache_valid 200 30s;` in `^~ /api/products/`; other routes untouched. Only `GET`/`HEAD` (nginx default).
3. **`X-Cache-Status: $upstream_cache_status`** on products responses, and `"cache":"$upstream_cache_status"` in the access log.
4. **Stampede protection:** `proxy_cache_lock on; proxy_cache_lock_timeout 5s` (= products' read timeout).
5. **Stale:** `proxy_cache_use_stale error timeout updating http_500 http_502 http_503 http_504; proxy_cache_background_update on;`.
6. **gzip:** `gzip on; gzip_types application/json text/css application/javascript; gzip_vary on; gzip_min_length 256;` (echo bodies are ~600 bytes).
7. **Merged tests that need uncached products**: Phase 2's slow-products test and Phase 4's slow-service test use a unique query per request (a new cache key), so they still measure the service — assertions unchanged.
8. **Break-its on temporary servers** (own location, same cache zone, no rate limit): lock on vs off on a cold key, stale with the service stopped, `Set-Cookie` from a temporary upstream.

## Edge cases

- `Authorization`, `Cookie` or `X-API-Key` present → `X-Cache-Status: BYPASS`, never stored (A).
- A HIT's body shows the *original* request's id/headers; the response's `X-Request-ID` header is the new one. Phase 4's "the backend saw the same id" only holds for MISS/BYPASS.
- `/api/products/x` and `/api/products/x?y=1` are different keys (`$request_uri` includes the query).
- Non-200 answers (FAIL_RATE 500, 404) are not cached (`proxy_cache_valid 200` only), but a stored 200 can be served stale when the service fails.
- HEAD is converted to GET for the cache by default (`proxy_cache_convert_head on`): a HEAD MISS stores the GET body.
- Rate limit still applies to HITs (preaccess runs first): a cached endpoint doesn't raise anyone's API budget.
- gzip applies after the cache: entries are stored uncompressed; clients without `Accept-Encoding: gzip` get plain bodies; `Vary: Accept-Encoding` is sent.
- A container restart empties the cache; reloads keep it.

## Recommendation

**A.** It delivers what the phase measures — one backend hit serving everyone — while making sure no request that carries a credential is ever stored or answered from the shared cache, which is the actual risk the notes' interview question points at. C is the long-term pattern, but it moves the lesson out of nginx.

## Decision

A — shared cache keyed by URL; requests carrying `Authorization`, `Cookie` or `X-API-Key` bypass it and are never stored; supporting decisions 1–8 as written (100m zone, products only, `proxy_cache_valid 200 30s`, `X-Cache-Status` + `cache` log field, `proxy_cache_lock` 5s, stale on error/timeout/5xx with background update, gzip for JSON/CSS/JS ≥ 256 bytes, Phase 2/4 slow-products tests use unique URLs, break-its on temporary servers). User's words: "that's oke. base on your recommend, pls move to next phase /sr:plan".
