# Phase 10 — Performance tuning & zero-downtime

## Problem

The last roadmap phase. Two things:

1. **Performance tuning:** change nginx settings because a measurement says so, and show the before/after. Not cargo-culted directives.
2. **Zero-downtime:** prove that the operations people actually run produce **no failed requests** under steady traffic: changing the gateway config, and redeploying a backend.

Grounding (this repo):
- **Settings today:**
  - `worker_processes auto`, `worker_connections 1024`, `keepalive_timeout 65`, `sendfile on`;
  - upstream `keepalive 16/32` with HTTP/1.1 (Phase 3);
  - TLS session cache (Phase 7), HTTP/2 to clients;
  - `access_log` unbuffered;
  - no `worker_rlimit_nofile`, no `open_file_cache`, no `keepalive_requests` set (default 1000).
- **No load tool installed:** the README's tech stack lists "k6, `wrk`, `curl`" and the layout lists `loadtest/ # k6 scripts`, but neither tool is installed and `loadtest/` doesn't exist. Only ApacheBench (`/usr/sbin/ab`) is on the machine. k6 runs from its official image (`grafana/k6`) with nothing installed on the host.
- **Phase 5 rate limits** (50 r/s per client IP, login 5 r/s) apply to the host's address (192.168.65.1). A load test from the host measures the limiter, not the gateway. The compose network (172.16.0.0/12) is exempt, so a load generator *inside* that network sees the real capacity, and it also avoids Docker Desktop's port-forwarding overhead.
- **Already proven (earlier phases):**
  - an invalid config is rejected and the old one keeps serving (Phase 0);
  - a reload keeps in-flight requests;
  - replica scale-down and a stopped replica don't fail GETs (Phase 3: `max_fails`, `proxy_next_upstream`, backup);
  - streams drain within `worker_shutdown_timeout` 30s (Phase 9);
  - services stop gracefully in under 5s.
- **Not proven yet:**
  - **No sustained traffic:** none of those checks ran with traffic flowing.
  - **No rolling redeploy:** nothing recreates each order replica in turn.
  - **POSTs during a deploy:** non-idempotent, and never retried (Phase 3), so a replica must be *drained* before it stops, not just stopped.
- **Single gateway:** recreating the gateway container is downtime by construction; only a reload is zero-downtime.

Constraints:
- **Machine-specific numbers:** absolute throughput depends on this laptop and Docker Desktop's CPU limit. Tests can assert error rates and *relative* changes, never absolute RPS. The README records the machine with the numbers.
- **Tests that read logs:** Phase 4 tests read the access log ~300ms after a request, so buffered access logging (`buffer=… flush=…`) can't exceed that without breaking them.

## Approaches

### A — Measure, tune, and prove zero-downtime under load (recommended)

- **Summary:**
  - `loadtest/` gets k6 scripts, run by `make bench` through `docker run grafana/k6` on the compose network, hitting `https://gateway`.
  - A baseline is recorded. A few directives are changed only where the numbers or nginx's own warnings justify them, and before/after numbers go in the README.
  - A `make deploy SERVICE=order-service` rolling redeploy drains each order replica (marked `down` in a generated upstream include + reload), waits for its in-flight requests, recreates it, re-enables it and moves on.
  - Tests run a short k6 load *while* doing a reload and a rolling deploy, and assert **zero failed requests**.
- **How it works:**
  - **k6 scripts:** a mixed read load (users/orders/products with a lab token, the k6 checks), plus a cached products load. k6 writes a JSON summary that the Makefile prints and the tests read.
  - **Tuning candidates, each kept only if measured or warned about:**
    - `worker_rlimit_nofile` + compose `ulimits` (nginx warns when connections exceed the file limit);
    - `keepalive_requests` for client connections;
    - `access_log … buffer=32k flush=200ms` (inside Phase 4's 300ms read window);
    - `open_file_cache` for static files;
    - upstream `keepalive_requests`/`keepalive_time`.
  - **Rolling deploy:** upstream servers come from an `upstreams/orders.d/*.conf` include, so `make deploy` can rewrite one file and `nginx -s reload` (validated with `-t` first, as `make reload` does). `max_fails` and the backup server stay as the safety net.
- **Pros:**
  - Real traffic and real numbers, the lab's whole method.
  - Uses the k6 the README already promised, with nothing to install.
  - Finds what zero-downtime really requires: draining for POSTs, not just stopping.
  - Every change has a reason in the README.
- **Cons:**
  - The longest phase: load tests add a minute or two to the suite.
  - The numbers only compare runs on one machine.
  - It pulls the `grafana/k6` image (one-time ~60 MB).
- **Best when:** the lab's goal holds: learn by measuring.

### B — Tuning checklist, no load testing

- **Summary:** set the commonly recommended directives (worker limits, buffering, caching, keepalive), assert them in config tests, and document why each matters. Zero-downtime is covered by the README's explanation of reload and the existing tests.
- **How it works:** config-text tests, plus a README section.
- **Pros:**
  - Quick; no new image, no slower suite.
- **Cons:**
  - Nothing proves a change helps, or that a deploy drops nothing. That's exactly what the earlier phases refused to do.
  - Tuning without a measurement often makes things worse (buffer sizes, connection counts).
- **Best when:** time is the only constraint.

### C — A plus a blue/green gateway

- **Summary:** everything in A, plus two gateway containers behind a tiny TCP front (nginx `stream` or HAProxy). `make gateway-swap` starts the new gateway, health-checks it, flips the front, and drains the old one, so even gateway recreates and nginx version upgrades lose nothing.
- **How it works:**
  - The front listens on 8080/8443 and proxies TCP to `gateway-blue` or `gateway-green`.
  - The swap flips the front's upstream and reloads it.
  - Tests run k6 through a swap.
- **Pros:**
  - Covers the one operation A can't (replacing the gateway itself).
  - Shows the pattern behind cloud load balancers.
- **Cons:**
  - Another proxy layer in every request and every earlier test.
  - The front itself is still a single point of failure.
  - Twice the gateway containers.
  - Client IPs become the front's IP unless it uses the PROXY protocol, which every Phase 2/5 test of client IPs and rate limits depends on.
- **Best when:** the gateway image or binary changes often enough that reload isn't enough.

## Edge cases

- **Rate limits in load tests:** load must come from the compose network (exempt) or it measures the limiter. One test checks that the same load from the host *is* limited, so the exemption is deliberate.
- **POSTs during a deploy:** never retried, so the replica must be out of rotation (marked `down` + reload) before it stops. A POST in flight on it must finish; services drain 5s on SIGTERM.
- **The last healthy replica:** a deploy never takes two replicas out at once, and refuses to start if a replica is already down.
- **The backup server:** a deploy must not trigger the degraded backup answers (Phase 3). The test asserts that no `X-Degraded` answers appear.
- **Reloads during a deploy:** each reload starts new workers; old ones linger up to 30s for streams (Phase 9). A deploy shouldn't stack many old workers.
- **Buffered access log:** `flush` must stay under Phase 4's ~300ms read window, or log tests fail.
- **`worker_rlimit_nofile` without a container limit:** nginx can't raise it. Compose must set `ulimits: nofile`, and nginx's startup warning is the check.
- **The cache skews numbers:** the products load is all cache hits after the first second. It's reported separately from the uncached routes.
- **Laptop noise:** a before/after counts only when it repeats; each number is a median of 3 runs.
- **k6 and TLS:** k6 inside the network calls `https://gateway`, a name the lab certificate doesn't list. Mount the lab CA and set the TLS server name to `localhost`, or use k6's `insecureSkipTLSVerify` for the bench only.

## Recommendation

**A: measure, tune, and prove zero-downtime under load.** Most important reason: "zero downtime" and "faster" are only true if traffic flowing through the change shows zero errors and a better number. A is the only option that makes both observable on this stack, using the k6 the README already promised. C's gateway swap is the natural next step, but it would rewrite every client-IP and rate-limit test for a case a reload already covers.

## Decision

**A: measure, tune, and prove zero-downtime under load.** k6 scripts in `loadtest/`, run by `make bench` from the `grafana/k6` image on the compose network (exempt from the Phase 5 limits); tuning only where a measurement or nginx's own warning justifies it, with before/after medians in the README; a rolling `make deploy SERVICE=order-service` that drains each replica (`down` in a generated upstream include + reload), waits, recreates and re-enables it; tests that run load *through* a reload and a rolling deploy and assert zero failed requests and no degraded answers. Edge cases as listed in the design.
