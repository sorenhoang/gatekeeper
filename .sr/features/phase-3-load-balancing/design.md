# phase-3-load-balancing — Design

## Problem

From `docs/phases/03-load-balancing.md`: order-service runs as 3 replicas; traffic spreads across them and survives one replica dying or being slow.
- Compare round-robin, `least_conn`, `ip_hash` using the echo's `hostname` field.
- A `backup` server returning a degraded response.
- `keepalive 32` on the upstream.
- Break-its: kill one replica mid-load (errors with vs without `proxy_next_upstream`); one replica slow with `DELAY_MS=800` (p95 round-robin vs `least_conn`); `--scale` a new replica — seen without reload?
- Done when: I can pick an algorithm for a workload and justify it.

Starting point (Phases 0–2, merged): `upstream orders { zone orders 64k; server order-service:8080 resolve; }` with `resolver 127.0.0.11 valid=2s`; the proxy snippet already sends HTTP/1.1 + `Connection ""` (keepalive-ready); orders' `proxy_read_timeout` is 15s, connect 1s.

Constraints found while grounding:
- **Locked tests assume one order container.** Phase 2's `TestShouldLetASlowOrdersBackendFinish` (DELAY_MS on `order-service` → expects ≥3s) and `TestShouldPassABackendsOwnErrorThroughUnchanged` (FAIL_RATE=1 on `order-service` → expects 500) only hold if *every* request lands on a chaos'd replica. Phase 1's swap test reads `order-service`'s single IP (`docker compose ps -q` returning 3 ids breaks `containerIP`). Whatever wins, some merged tests must change; that is decided here at G1.
- **`resolve` changes what "killing a replica" means.** A stopped container leaves Docker DNS, so after ≤2s nginx simply drops it as a peer: no errors at all. Errors only happen in the stale window (connect timeout 1s, then `proxy_next_upstream` can retry elsewhere for GETs). The doc's "count errors with and without `proxy_next_upstream`" experiment has to be timed inside that window, or use a frozen replica (`SIGSTOP`), which keeps its DNS entry.
- **Retry on timeout multiplies waits** (flagged in the Phase 2 review): orders' 15s read timeout × N tries. A frozen replica costs a full 15s before any retry. `proxy_next_upstream_timeout` must cap the total, and the lesson is that retry-on-timeout only helps when the read timeout is shorter than the client's patience.
- **POST is not retried by default** (`non_idempotent` off since nginx 1.9.13). Keep it that way; demonstrate it.
- **`backup` is incompatible with `hash`, `ip_hash` and `random`.** It works with round-robin and `least_conn`.
- **`ip_hash` from the host:** every request from the Mac arrives from the same IP (`192.168.65.1`), so `ip_hash` sends all of them to one replica. That is the "weak stickiness" lesson, and it also means `ip_hash` can't spread test traffic.
- **Shared zone state:** with `zone`, round-robin and `least_conn` state is shared across workers, so distribution is measurable with a handful of requests (tolerance still needed).
- **Passive health checks with one server are ignored** (Phase 2 finding); with 3 order servers, `max_fails=1 fail_timeout=10s` (defaults) now takes a failing replica out for 10s — tests that induce failures must expect it.
- **Keepalive is observable:** the echo reports `remote_addr`; with a pool, sequential requests reuse the gateway's source port.

Out of scope: active health checks (Nginx Plus), sticky cookies (Plus / third-party), k6 load tests as automated tests (the README shows the commands; tests use Go loops), replicas for users/products.

## Approaches

The main decision is **how the 3 replicas exist**.

### A — One scaled service (`deploy.replicas: 3`)
- **Summary:** keep `order-service`, add `deploy: { replicas: 3 }`. Docker DNS returns 3 A records for `order-service`; `server order-service:8080 resolve` turns them into 3 peers.
- **How:** scaling is `docker compose up -d --scale order-service=4`; nginx picks the new A record within 2s, no config edit.
- **Pros:** smallest config; the `--scale` break-it works natively; existing tests that put chaos on `order-service` keep passing (they recreate all replicas with the same env).
- **Cons:** per-replica chaos is impossible: `DELAY_MS`/`FAIL_RATE` apply to every replica, so "one replica slow with `DELAY_MS=800`" can't be built (only freezing a container by id); replicas have no stable names, so tests address them by container id; Phase 1's swap test (single IP) must change.
- **Best when:** replicas are identical and you only care about count.

### B — Three named services, listed in the upstream
- **Summary:** `order-service`, `order-service-2`, `order-service-3` (one YAML anchor, same image), and three `server … resolve` lines.
- **How:** chaos on one replica is `DELAY_MS=800 docker compose up -d order-service-2`; the upstream lists each by name.
- **Pros:** every break-it in the phase notes works as written, including the per-replica `DELAY_MS=800` p95 comparison; replicas are addressable by name in tests and logs; the config shows exactly what nginx balances over; `--scale order-service-3=2` still demonstrates DNS pickup.
- **Cons:** adding a replica means editing compose and the upstream; the two Phase 2 tests that put chaos on `order-service` now hit 1 of 3 replicas and must apply it to all three (a helper); Phase 1's swap test only swaps replica 1.
- **Best when:** you need to treat replicas differently (canaries, chaos, weights) — this lab.

### C — Named services sharing a network alias
- **Summary:** B's three services, each with a compose network alias `orders`; the upstream has one line, `server orders:8080 resolve`.
- **How:** Docker DNS answers `orders` with all three IPs; a fourth replica is just another service with the alias.
- **Pros:** per-replica chaos *and* scale-without-upstream-edit.
- **Cons:** the upstream hides how many peers exist (`nginx -T` no longer shows them); per-server parameters (`weight`, `max_fails`, `backup` on a specific replica) are impossible because they are one `server` line; same Phase 2 test changes as B; relies on Docker-specific alias behaviour.
- **Best when:** many identical replicas that still need individual chaos.

## Supporting decisions (to confirm with G1)

1. **Shipped algorithm for orders: `least_conn`.** Orders is the route allowed to be slow (15s); least connections steers away from a replica that is busy or stuck, while round-robin keeps feeding it. Round-robin and `ip_hash` are compared in tests through temporary upstreams (`zz-smoke-*.conf`), not shipped.
2. **Retries:** `proxy_next_upstream error timeout http_502 http_503; proxy_next_upstream_tries 2; proxy_next_upstream_timeout 15s;` — GETs retry once on another replica, and the total wait stays inside orders' 15s. POST is never retried (nginx default kept, and tested).
3. **Passive health checks explicit:** `max_fails=1 fail_timeout=10s` on each order server, written down so the 10s ejection is visible in config.
4. **Backup without a new container:** `server 127.0.0.1:8081 backup;` pointing at a small nginx-internal server that answers `200 {"degraded":true,"service":"orders-fallback"}` with `X-Degraded: true`. Used only when all three replicas are out.
5. **`keepalive 32` on all three upstreams** (users and products too: same benefit, one line each); tested by source-port reuse.
6. **Tests that assume one order container** are updated, as agreed here: Phase 2's slow-orders and FAIL_RATE tests apply the env to all three replicas through a helper; Phase 1's swap test keeps swapping `user-service` with `order-service` (replica 1), which is still a valid swap.

## Edge cases

- With `resolve`, a stopped replica returns no errors after ≤2s; errors appear only in the stale window (connect timeout → retry for GETs). Tests and README must say which window they measure.
- A frozen replica (`SIGSTOP`) keeps its DNS entry and accepts TCP: GETs to it wait the full read timeout; with `proxy_next_upstream_timeout 15s` the retry has no time left, so the client gets 504. `least_conn` reduces how often that happens; it can't remove it.
- POST to a dead or frozen replica → error to the client, never a silent second execution.
- `max_fails=1`: one induced failure ejects a replica for 10s; tests must not depend on a specific replica answering right after a failure.
- `backup` only engages when *all* primaries are unavailable (no address or ejected), not when one is slow.
- `ip_hash` with the host's single client IP → 100% on one replica; `hash $request_uri consistent` would spread by URL instead (mention only).
- Keepalive + `resolve`: when a replica's IP changes, pooled connections to the old IP are closed on failure; no stale reuse across a swap (Phase 1 swap test must still pass).
- `Makefile` readiness loop hits `/api/orders/` once: fine with 3 replicas.
- Phase 2's `error_page 502/504` still applies: if all replicas and the backup are gone, the client gets the JSON 502.
- Echo `service` stays `order-service` on all replicas (same binary); `hostname` (container id) distinguishes them.

## Recommendation

**B.** It is the only layout where every break-it in the phase notes works as written, the per-replica `DELAY_MS=800` comparison above all, and it keeps per-server parameters (`max_fails`, later `weight`) visible in the upstream. The price is updating two Phase 2 tests to put chaos on all three replicas.

## Decision

B — three named services (`order-service`, `order-service-2`, `order-service-3`), one `server … resolve` line each in `upstream orders`; supporting decisions 1–6 as written (`least_conn` shipped, retries capped by `proxy_next_upstream_timeout 15s` with POST never retried, explicit `max_fails=1 fail_timeout=10s`, nginx-internal degraded backup, `keepalive 32` on all upstreams, Phase 1/2 tests that assume one order container updated). User's words: "yes, i will happy with option B, and now please help me move to next phase /sr:plan".
