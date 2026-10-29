# phase-1-routing — Design

## Problem

From `docs/phases/01-static-and-routing.md`: Nginx serves `frontend/` at `/` with an SPA fallback and routes `/api/users/`, `/api/orders/`, `/api/products/` to their services, stripping `/api/<name>` (`/api/users/42` → backend sees `/42`). Plus:
- `location = /health` answered by Nginx itself (`return 200`).
- A second virtual server `admin.localhost` serving something different.
- Unknown `/api/*` → JSON 404.
- "Done when": given any URL, I can predict which `location` wins before running curl.

Starting point (phase 0, merged): `nginx/conf.d/default.conf` is one catch-all server with `try_files $uri $uri/ =404`; four Go echo services that report the `path`, `host` and `headers` they received; the smoke suite in `services/smoke` (`make test`).

Constraints found while grounding:
- **Names resolve once, at start/reload.** `proxy_pass http://user-service:8080` (directly or via a plain `upstream`) resolves when the config loads. Measured on this stack: removing `user-service` and `order-service` and starting them again in the other order **swapped their IPs** (order .3→.4, user .4→.3). A startup-resolved gateway would then send `/api/users/*` to order-service, which returns 200 with the wrong `service`, not a 502. Phase 0's documented chaos workflow (`DELAY_MS=… docker compose up -d order-service`) recreates containers, so this is a normal lab action, not an edge case. A single recreate kept its IP in the test, which is why the bug is intermittent.
- **Gateway start depends on every name resolving.** With static resolution, a service with no container (crashed on a bad `FAIL_RATE`, or `docker compose up gateway` alone) makes `nginx` refuse to start: `host not found in upstream`.
- **The phase-0 smoke test `/nope → 404`** contradicts the SPA fallback the phase requires (`/nope` would return `index.html`). That test belongs to the merged phase 0, so changing it must be agreed here.
- **Admin isolation.** Anything under `frontend/` is reachable at `localhost:8080/...`. If the admin page lived in `frontend/admin/`, the main server would serve it too, and Phase 8's `auth_basic` on the admin server could be bypassed through `localhost:8080/admin/`.
- **Later phases build on these blocks.** P2 adds `snippets/proxy-headers.conf` and per-route timeouts; P3 turns order-service into an `upstream` with 3 replicas and `keepalive`; P6 caches `/api/products/*`; P8 protects `/api/orders/*` and puts `auth_basic` on the admin server.
- Image is `nginx:1.27-alpine` = **1.27.5**: `server … resolve` in an open-source `upstream` (≥ 1.27.3) is available.

Out of scope: proxy headers, timeouts, buffering (P2); replicas and LB algorithms (P3); auth routes.

## Approaches

### A — Plain `upstream` blocks, resolved at load
- **Summary:** `upstream users { server user-service:8080; }` ×3; `location /api/users/ { proxy_pass http://users/; }`.
- **How:** names resolve once when the config loads; `make reload` re-resolves.
- **Pros:** exactly the doc's syntax; fewest directives; P3 just adds `server` lines.
- **Cons:** silent misrouting after services are recreated in a different order (measured above); the gateway won't start if any service lacks a container; the fix is a manual `make reload` the learner has to remember.
- **Best when:** backends have fixed addresses.

### B — `upstream` blocks with `resolve` (Docker DNS re-queried)
- **Summary:** as A, plus `resolver 127.0.0.11 valid=5s;` in `http` and `zone users 64k; server user-service:8080 resolve;` in each upstream.
- **How:** Nginx re-resolves the name in the background every `valid` window; the upstream stays a named block, so `proxy_pass http://users/` keeps its URI-replacement semantics.
- **Pros:** recreated or reordered services are picked up within ~5s, no reload; the gateway starts even when a service is down (that route returns 502 until it appears); the `upstream` block P3 extends already exists.
- **Cons:** three directives (`resolver`, `zone`, `resolve`) arrive before P3 explains them; up to `valid=5s` of stale routing right after a swap; `127.0.0.11` is Docker-specific; P3's "does Nginx see a new replica without reload?" experiment becomes "remove `resolve` and watch it stop".
- **Best when:** backends are containers that come and go (this lab).

### C — Variable `proxy_pass` + `resolver`
- **Summary:** `set $users user-service:8080; rewrite ^/api/users(/.*)$ $1 break; proxy_pass http://$users;`.
- **How:** a variable in `proxy_pass` forces a DNS lookup per request (cached for `valid`).
- **Pros:** works on any Nginx version; per-request resolution.
- **Cons:** a variable switches off the trailing-slash URI replacement, which is this phase's main lesson, so prefixes have to be stripped with `rewrite`; there is no `upstream` block, so P3 has to rewrite every location; `keepalive` pools (P3) don't apply.
- **Best when:** older Nginx without `resolve`, or one-off dynamic targets.

## Supporting decisions (to confirm with G1)

1. **SPA fallback, but not for assets.** `location / { try_files $uri $uri/ /index.html; }` plus a regex location for static file extensions with `try_files $uri =404`, so `/settings` → `index.html` but `/missing.js` → 404, not HTML served as JavaScript. This also gives the "regex shadows a prefix" break-it a real target. Rewrite the phase-0 test: `/nope` → 200 with `index.html`, and a missing asset → 404.
2. **Admin server in its own file and directory:** `nginx/conf.d/admin.conf` with `server_name admin.localhost`, root `admin/` mounted at `/usr/share/nginx/admin:ro`, outside the main root. Chrome resolves `*.localhost` to loopback, so `http://admin.localhost:8080` works in a browser.
3. **`X-Route` response header on every location** (`add_header X-Route <name> always;`): `curl -I` then shows which location won. This turns "predict the location" into something the smoke tests can assert, URL by URL.
4. **JSON everywhere under `/api/`:** `location /api/ { default_type application/json; return 404 '{"error":"not found","path":"$uri"}'; }`. `/health` returns `{"status":"ok","from":"gateway"}` so it can't be mistaken for a backend's `/health`.
5. **`/api/users` without a slash** keeps Nginx's built-in 301 to `/api/users/` (the prefix location ends in `/` and uses `proxy_pass`). It is shown and tested, not overridden.
6. **Upstream names `users`, `orders`, `products`.** Until P2 sets `Host`, the backend sees `Host: users`; the echo shows this, which sets up P2's header lesson.
7. **Tests:** new smoke files in `services/smoke/` driven by a table: URL → status, `X-Route`, backend `service`, backend `path`.

## Edge cases

- `/api/users/` → backend `/`; `/api/users/42?x=1` → `/42` with query `x=1`; `/api/users//42` → backend `//42` (`merge_slashes` only normalizes for location matching).
- `/api/usersX` must not reach user-service: it falls to `/api/` → JSON 404.
- Encoded paths: with a URI in `proxy_pass`, Nginx forwards the *normalized* URI (`/api/users/a%2Fb` → backend `/a/b`). Document; don't fix.
- `/health` exact match must not catch `/health/` or `/healthz` (those go to the SPA fallback).
- `index.html` needs `Cache-Control: no-cache`, or a browser keeps the old SPA shell after a deploy. Optional: one directive.
- `add_header` in a location drops all server-level `add_header`s (same inheritance trap as P2's `proxy_set_header`).
- A request to `admin.localhost` with no admin content must not fall back to the main frontend; and any other `Host` (e.g. the IP `127.0.0.1:8080`) still hits the default server.
- `nginx -t` and `make reload` must still pass while a service is stopped (B only: names are not required to resolve at load).
- The phase-0 `TestShouldReturn404ForUnknownPath` test changes meaning (decision 1).

## Recommendation

**B.** On this stack, recreating services already swapped their IPs, and A would then route `/api/users/*` to order-service with a 200. That is a silent bug that teaches the wrong lesson. B fixes it with three directives and keeps the `proxy_pass` trailing-slash semantics this phase is about.

## Decision

B — `upstream` blocks with `resolve` (`resolver 127.0.0.11 valid=5s`, `zone`, `server … resolve`), with supporting decisions 1–5 as written (including rewriting the phase-0 `/nope → 404` test for the SPA fallback). User's words: "that's oke, i prefer option B, move to next phase /sr:plan".
