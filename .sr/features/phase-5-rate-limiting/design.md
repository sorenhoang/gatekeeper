# phase-5-rate-limiting — Design

## Problem

From `docs/phases/05-rate-limiting.md`: abusive clients get 429s while normal traffic is unaffected.
- Strict limit for `/api/users/login` (e.g. 5r/s, burst 10).
- Looser global API limit keyed by `X-API-Key` when present, IP otherwise.
- Allowlist an internal IP range from limits.
- JSON body for 429 with `Retry-After`.
- Concepts: leaky bucket, `burst`, `nodelay`, `delay=N`, `limit_req_dry_run`, `limit_conn`, `limit_rate`, slow-client timeouts, `allow`/`deny`, per-instance zones.
- Break-its: 50 parallel requests with `burst` only vs `burst + nodelay` (latency spread); `limit_req_dry_run` changes only logs.
- Done when: I can explain what `burst=10 nodelay` does to the 11th and 12th request in a spike.

Starting point (Phases 0–4, merged): three API locations (`^~ /api/users|orders|products/`) behind upstreams; JSON error pages for nginx's own 502/503/504 via named locations; every location includes the `X-Request-ID` response snippet; JSON access log with `route`; `X-Forwarded-For` is never trusted (Phase 2: the gateway is the edge).

Constraints found while grounding:
- **Every request from the host has the same client IP** (`192.168.65.1`, Docker Desktop's host gateway). A per-IP limit is one shared bucket for all of `make test`, every curl, and the user's browser.
- **The existing suite is bursty.** Measured on this stack: 40 sequential requests on one connection arrive at ~2000 req/s; 6 concurrent clients × 10 requests at ~7000 req/s. No sensible *rate* fits that; only `burst` can absorb it. A global limit sized without this breaks Phase 1–4 tests with 429s.
- **"Keyed by `X-API-Key` when present" is a bypass as written.** Each distinct key value gets its own bucket, so a client that sends a fresh random key per request is never limited. The key must only count when it is a key the gateway knows.
- **There is no login route yet.** `/api/users/login` today is just the users echo at `/login`; Phase 8 adds `/api/auth/login` (auth-service). The strict limit is a property of the *login* location, so it must be easy to point at the Phase 8 path later.
- **Allowlisting must use the real peer.** Phase 2 overwrites `X-Forwarded-For`; an allowlist must key on `$remote_addr`, never a header. Allowlisting the host's `192.168.65.0/24` would exempt every test and curl, so limits could never be observed from the host.
- **The 429 page is a new nginx-generated error.** Like Phase 2's 502/504, it goes through `error_page` to a named location, which must re-add `X-Route` and `X-Request-ID` (inheritance trap) and set `Retry-After`.
- **Phase 4's log can show limiting directly**: `$limit_req_status` (`PASSED`, `DELAYED`, `REJECTED`, `DELAYED_DRY_RUN`, `REJECTED_DRY_RUN`).
- **Zones are per nginx instance** (the notes' interview question): two gateways = two separate counters. Mention only; one gateway here.
- Slow-client protection exists today only as defaults: `client_header_timeout` / `client_body_timeout` are 60s; `client_max_body_size 1m` already written down in Phase 2.

Out of scope: `limit_conn`, `limit_rate`, user-agent blocking (concepts in the README; nothing in the Build list needs them), a real API-key registry (Phase 8 auth), synchronising limits across gateways.

## Approaches

The main decision is **what identifies a client for the global API limit**.

### A — Client IP only
- **Summary:** `limit_req_zone $binary_remote_addr zone=api:…`; `X-API-Key` is ignored.
- **How:** every client address gets one bucket.
- **Pros:** simplest; unspoofable (the TCP peer).
- **Cons:** clients behind one NAT share a bucket (here: everyone on the host); doesn't do what the phase asks (keys).
- **Best when:** there are no API keys.

### B — `X-API-Key` when present (any value), IP otherwise
- **Summary:** `map $http_x_api_key $limit_key { "" $binary_remote_addr; default $http_x_api_key; }`.
- **How:** each key value is its own bucket; requests without a key share the IP bucket.
- **Pros:** the phase notes' wording; per-customer limits.
- **Cons:** trivially bypassed — a fresh random key per request is never limited; unbounded keys fill the shared-memory zone (nginx then evicts and returns 503 under pressure).
- **Best when:** a gateway in front already rejected unknown keys.

### C — Known `X-API-Key` → its own bucket; unknown or missing → IP bucket
- **Summary:** a `map` of the lab's known keys (`key → key`); anything else maps to `$binary_remote_addr`.
- **How:** a known key is limited per key; an invented key buys nothing — it lands in the sender's IP bucket.
- **Pros:** the phase's intent (per-key limits) without the bypass; zone size bounded by IPs + known keys; Phase 8 can replace the static map with the auth result.
- **Cons:** keys live in an nginx config file (fine for a lab, a secret store in production); adding a key needs a reload.
- **Best when:** keys are few and known to the gateway (this lab until Phase 8).

## Supporting decisions (to confirm with G1)

1. **Login limit:** `location = /api/users/login` (proxied to user-service like the users route) with `limit_req zone=login burst=10 nodelay`, `limit_req_zone $binary_remote_addr zone=login:10m rate=5r/s`. A 50-request spike → 11 pass at once (1 + burst 10), 39 get 429. Phase 8 moves the same zone to `/api/auth/login`.
2. **Global API limit:** `rate=50r/s burst=100 nodelay` on the three API locations, keyed per C. Sized from the measurement: the suite's largest spike is 40–60 requests at once, inside `burst=100`; the bucket then refills at 50/s.
3. **Allowlist:** `geo $limit_exempt { default 0; 127.0.0.0/8 1; 172.16.0.0/12 1; }` (loopback + Docker's compose networks) → exempt requests get an empty key, which `limit_req` never counts. The host's `192.168.65.1` stays limited so tests can see limits.
4. **429 response:** `limit_req_status 429; error_page 429 @too_many_requests;` → `{"error":"too many requests","status":429,"route":"<route>"}` with `Retry-After: 1` (1s = the smallest header unit; login refills one slot every 200ms), `X-Route`, `X-Request-ID`.
5. **Access log** gains `"limit_req":"$limit_req_status"`.
6. **Slow clients:** `client_header_timeout 5s; client_body_timeout 10s;` (defaults 60s). A client that stops sending headers is cut off after 5s.
7. **Break-its as tests, not shipped config:** `burst` vs `burst + nodelay` spread and `limit_req_dry_run` run on temporary `zz-smoke-*` servers with their own zones.

## Edge cases

- `limit_req` runs before proxying: a 429 never reaches a backend, isn't retried (Phase 3), and doesn't count toward `max_fails`.
- `/health`, static files, the SPA, `admin.localhost` and `stub_status` are not limited (no `limit_req` there).
- A known key and an invented key from the same IP: the known key has its own bucket; the invented one shares the IP bucket with key-less requests.
- `Retry-After` must be on the 429 only, and the 429 must still be JSON for `/api/users/login`.
- Two limits on one request (login inherits nothing from the users location — it's its own `location =`): only the login zone applies there; document it.
- `nodelay` vs default: without `nodelay`, burst requests are queued and released at the rate (login: 200ms apart → the 11th waits ~2s); with `nodelay`, they pass at once and the bucket slots are still consumed.
- Dry run: requests over the limit pass, log `REJECTED_DRY_RUN`, and nothing else changes.
- Zone memory: `10m` ≈ 160k `$binary_remote_addr` states; with C, known keys add only a handful.
- The Phase 0 `make test` and Phase 1–4 tests must still pass with the limits on (verified by the full gate).

## Recommendation

**C.** It gives the per-key limits the phase asks for without B's bypass — measured or not, a limit that a random header defeats is not a limit — and it bounds the zone's memory. A is the fallback if API keys aren't wanted at all.

## Decision

C — known `X-API-Key` gets its own bucket, unknown or missing key falls back to the client IP's bucket; supporting decisions 1–7 as written (login 5r/s burst=10 nodelay per IP on `= /api/users/login`; global API 50r/s burst=100 nodelay; `geo` allowlist for 127.0.0.0/8 and 172.16.0.0/12 on `$remote_addr`; JSON 429 with `Retry-After: 1`, `X-Route`, `X-Request-ID`; `limit_req` field in the access log; `client_header_timeout 5s`, `client_body_timeout 10s`; burst/nodelay and dry-run break-its as tests on temporary servers). User's words: "okay, follow as your recommend, option C. next phase /sr:plan".
