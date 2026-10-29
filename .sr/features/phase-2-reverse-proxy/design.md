# phase-2-reverse-proxy — Design

## Problem

From `docs/phases/02-reverse-proxy.md`: backends must receive correct client information, and slow or dead backends must be handled deliberately instead of by Nginx defaults.
- `snippets/proxy-headers.conf` included in every proxied location (`Host`, `X-Real-IP`, `X-Forwarded-For`, `X-Forwarded-Proto`; HTTP/1.1 to upstreams).
- Per-route timeouts: orders may be slow, users must be fast.
- 502/503/504 answered as JSON.
- Break-its: `DELAY_MS=5000` vs a 2s read timeout → 504; a stopped service → 502 plus its error-log line; a 5 MB body → 413.
- Done when: I can name the exact cause of a 502 vs a 504 from the error log.

Measured on the Phase 1 stack (this worktree, `curl` + `docker compose logs gateway`):

| Situation | Status | Time | Error log |
|---|---|---|---|
| Today, any proxied request | backend sees `Host: users`, `Connection: close` (HTTP/1.0), `remote_addr` = the gateway's IP | | |
| Client sends `X-Forwarded-For: 6.6.6.6` | passed through untouched: the backend would trust a forged IP | | |
| `user-service` stopped | **502** | 2 ms | `user-service could not be resolved (3: Host not found)` + `no live upstreams while connecting to upstream` |
| `user-service` frozen (`SIGSTOP`) | **504** | **60 s** | `upstream timed out (110: Operation timed out) while reading response header from upstream` |

So the defaults are: a hung backend holds the client for 60 s, and with Phase 1's `resolve` a stopped container is **not** the `connect() failed (111: Connection refused)` case the phase notes expect. That line now needs a container that is running but not listening (e.g. a wrong port), so the "Done when" covers three distinct 502/504 causes, not two.

Constraints found while grounding:
- **The gateway is the edge.** Nothing sits in front of it now; Phase 7 terminates TLS here. Any `X-Forwarded-For` / `X-Real-IP` a client sends is untrusted.
- **Header inheritance trap.** One `proxy_set_header` in a location drops every inherited one. Phase 8 adds `X-User-ID` per location; Phase 9 needs `Upgrade`/`Connection: upgrade` for WebSockets. Two `proxy_set_header Connection` lines in one location send the header twice, so the WebSocket location will need its own snippet.
- **Backend errors are already JSON.** `FAIL_RATE` makes services answer `500 {"error":"injected failure","service":…}`. JSON error pages must replace only what *nginx* generates (502/503/504), not backend responses: `proxy_intercept_errors` stays off.
- **`error_page` is an internal redirect.** `add_header X-Route …` in the API location is lost on the error response unless the error location sets it again.
- **Buffering is hard to observe with the echo services:** responses are a few hundred bytes and never streamed. Request-body buffering is observable without service changes (a body above `client_body_buffer_size` logs `a client request body is buffered to a temporary file`). Response buffering waits for Phase 9's SSE endpoint.
- **Phase 3** adds `keepalive` to the upstreams, which only works with `proxy_http_version 1.1` + `proxy_set_header Connection ""`. Setting them now is harmless and saves a later trap.
- Timeout tests need `DELAY_MS` on a service, which recreates its container: those tests are `lifecycle` (phase-0 pattern, `make up` restores).

Out of scope: request id generation (P4), rate limits and body limits as protection (P5: `client_max_body_size` stays default, it is only observed here), keepalive pools (P3), TLS (`X-Forwarded-Proto` is always `http` until P7), auth headers (P8), WebSocket/SSE (P9).

## Approaches

The phase's main decision is the **client-IP trust model**: what the backend is told about who the client is.

### A — Edge gateway, overwrite: never trust incoming forwarding headers
- **Summary:** `X-Forwarded-For $remote_addr`, `X-Real-IP $remote_addr`, `Host $host`, `X-Forwarded-Proto $scheme`, `X-Forwarded-Host $host`, `proxy_http_version 1.1`, `Connection ""`, all in `snippets/proxy-headers.conf`.
- **How:** whatever the client sent in `X-Forwarded-For` is replaced; the backend sees exactly one address, the TCP peer of the gateway.
- **Pros:** unspoofable today; the backend needs no parsing; the forged-header experiment shows the protection directly.
- **Cons:** if a load balancer is ever put in front, every client looks like the LB until `real_ip` is configured (`set_real_ip_from <lb cidr>` + `real_ip_header X-Forwarded-For`); the "chain" format of XFF isn't exercised.
- **Best when:** nginx is the first hop that clients reach (this lab).

### B — Append the chain: `$proxy_add_x_forwarded_for` (the phase notes' default)
- **Summary:** same snippet, but `X-Forwarded-For $proxy_add_x_forwarded_for`.
- **How:** nginx appends `$remote_addr` to whatever XFF the client sent: `6.6.6.6, 192.168.65.1`. Only the right-most entry was added by a trusted hop.
- **Pros:** standard multi-hop format; works unchanged behind a load balancer.
- **Cons:** the left-most entry is attacker-controlled, and naive backends (and Phase 5's rate limiting by `$http_x_forwarded_for`, if anyone keys on it) read the left-most; every consumer must know to read right-to-left.
- **Best when:** nginx sits behind trusted proxies that already build the chain.

### C — Chain with `real_ip` trust list
- **Summary:** B plus `set_real_ip_from` for trusted proxy CIDRs and `real_ip_recursive on`, so `$remote_addr` becomes the real client once trusted hops are peeled off; XFF still appended.
- **How:** with an empty trust list it behaves like B; adding the LB's CIDR makes nginx's own logs and limits use the real client.
- **Pros:** the production pattern for "nginx behind an LB"; one place to declare trust.
- **Cons:** nothing to trust in this stack, so the config is dead weight today and the forged left-most entry still reaches backends; misconfiguring the CIDR (e.g. trusting the Docker bridge) silently lets any container spoof.
- **Best when:** a real LB/CDN fronts nginx.

## Supporting decisions (to confirm with G1)

1. **Snippet, included in every proxied location** (`include /etc/nginx/gatekeeper/snippets/proxy-headers.conf;`), nothing set at server level, so there is no inherited set to lose. A test asserts every `proxy_pass` location includes it.
2. **Per-route timeouts** in the API locations (`proxy_connect_timeout` 1s everywhere — inside the compose network a connect either works instantly or the target is gone):
   - users: `proxy_read_timeout 2s` ("must be fast")
   - orders: `proxy_read_timeout 15s` ("may be slow")
   - products: `proxy_read_timeout 5s`
   - `proxy_send_timeout` 5s for all.
3. **JSON error pages for nginx-generated 502/503/504** via `error_page 502 @bad_gateway; error_page 504 @gateway_timeout; error_page 503 @unavailable;` in the server block (inherited by the API locations). Body: `{"error":"bad gateway","status":502,"route":"users"}`, where `route` comes from a variable each API location sets. `proxy_intercept_errors` stays off, so a backend's own 500 JSON passes through unchanged.
4. **`X-Route` kept on error responses** by re-adding it in the named locations from the same variable.
5. **Buffering:** keep nginx defaults (`proxy_buffering on`, `proxy_request_buffering on`); document and test only what is observable now: a large request body is buffered to a temp file before the backend sees it, and a 5 MB body hits the default `client_max_body_size 1m` → 413 before any backend is contacted. Response buffering and `proxy_buffering off` move to Phase 9 (SSE).
6. **The three 502/504 causes** go in the README as an error-log table, each reproduced by a test: no address (`no live upstreams`), refused (`connect() failed (111…)`, via a container that is up but not listening — `docker compose kill -s STOP` doesn't do it; a temporary upstream pointing at a wrong port does), and timed out (`upstream timed out (110…)`).

## Edge cases

- `X-Forwarded-For`, `X-Real-IP`, `Forwarded` sent by the client must not reach the backend unchanged (A overwrites the first two; `Forwarded` (RFC 7239) is not set by nginx and would pass through → clear it with `proxy_set_header Forwarded "";`).
- `Host`: `$host` is lower-case and has no port; `$http_host` keeps the client's port. Backends here don't build URLs, so `$host`; `X-Forwarded-Host` carries the same.
- `Connection: close` today (HTTP/1.0) → `Connection` must become empty, not `keep-alive`, so Phase 3's pool works.
- A backend that returns its own 502/503/504 (none today) would pass through as-is because interception is off; document.
- 504 must arrive within the route's timeout (+ connect), not 60 s: tests measure elapsed time.
- `DELAY_MS` delays every non-health path, including the 15 s orders route: tests use delays between the users and orders timeouts (e.g. 3 s → users 504, orders 200).
- A 504 on `/api/users/` does not mark the route unavailable: nginx ignores `max_fails`/`fail_timeout` when an upstream has a single server. This changes in Phase 3 (3 order replicas), where an induced timeout will take a replica out for `fail_timeout`; tests written now must not depend on it either way.
- 413 is generated before routing reaches the upstream; its body stays nginx's HTML unless an `error_page 413` is added (not required).
- `error_page` to a named location keeps the original `$uri`, so `route`/`X-Route` stay correct; a `location = /50x.json` would lose them.
- Phase 1 tests (`X-Route` on API responses, `make reload`, swap recovery) must still pass.

## Recommendation

**A.** The gateway is the first hop, and measured today a forged `X-Forwarded-For: 6.6.6.6` reaches the backend unchanged; overwriting with `$remote_addr` removes that with no parsing burden on services. B's chain and C's `real_ip` are taught as a break-it experiment ("switch to `$proxy_add_x_forwarded_for` and forge the header") rather than shipped.

## Decision

A — edge gateway, overwrite: `X-Forwarded-For` and `X-Real-IP` set to `$remote_addr`, never trusting incoming forwarding headers; supporting decisions 1–6 as written (shared `snippets/proxy-headers.conf`, per-route timeouts users 2s / orders 15s / products 5s / connect 1s, JSON for nginx-generated 502/503/504 with `X-Route` kept and `proxy_intercept_errors` off, default buffering with request-body buffering and 413 observed, README table of the three 502/504 causes). User's words: "that's oke, and now option A, move to next phase /sr:plan".
