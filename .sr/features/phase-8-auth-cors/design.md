# Phase 8 — Auth at the gateway & CORS

## Problem

Today any client reaches every API route. Phase 8 makes the **gateway** decide who may call what, so services can trust an identity they never verify themselves, and lets a browser app on another origin call the API (CORS).

What must be true at the end:
- A login endpoint issues a credential; protected routes answer **401** (JSON, like the other gateway errors) without a valid one, and the backend never sees an unauthenticated request.
- Backends receive the caller's identity in headers set by the gateway (e.g. `X-User-ID`, `X-User-Role`), and a client can never forge them.
- The admin host (`admin.localhost`) requires its own login (`auth_basic`), as Phase 1's comment promised.
- Browsers on an allowed origin get correct CORS answers (preflight included); other origins get none.
- No secret (signing key, passwords) is committed; like Phase 7's certs they are generated locally.

Grounding (this repo):
- `auth-service` exists in compose but is only the shared echo server (`services/cmd/auth-service` → `echo.Run`). `echo.Run` builds a fixed `Handler`; a service can't add routes yet.
- The README architecture already draws `auth-svc ◀── auth_request (subrequest)`.
- nginx:1.27-alpine has `auth_request` compiled in and ships njs as a dynamic module (`ngx_http_js_module.so`). Native `auth_jwt` is NGINX Plus only.
- Markers left by earlier phases:
  - `default.conf`: "Phase 8 moves login to /api/auth/login with the same zone".
  - `admin.conf`: "Phase 8 adds auth_basic here".
  - `nginx.conf`: the Phase 5 API-key map, "Phase 8 replaces this with auth".
  - Compose: only the gateway is published, so services can trust gateway-set headers.
- Phase 6: requests with `Authorization`/`Cookie`/`X-API-Key` bypass the products cache; the cache stays shared for anonymous callers.
- **Order of phases inside nginx:** `limit_req` runs in *preaccess*, `auth_request` in *access*. A rate limit can never be keyed on an identity that `auth_request` produces later. This shapes the API-key question below.

Ambiguities, settled by the approach chosen:
1. Which routes are protected: proposed users + orders protected; products public (cached catalogue); `/health` and login public.
2. Token format and where it is checked.
3. What happens to the Phase 5 API-key buckets.

Constraints: no new Go dependency (HS256 with `crypto/hmac` is enough), no secrets in git, existing tests keep their assertions except where this phase deliberately changes behaviour (protected routes now need a token: those tests send one, approved at G2).

## Approaches

### A — `auth_request` to auth-service (recommended)

- **Summary:** auth-service gets two endpoints: `POST /login` (checks a lab user, returns a signed HS256 token, 15 min) and `GET /verify` (checks the `Authorization: Bearer` token, answers 204 with `X-User-ID`/`X-User-Role`, or 401). Protected locations add `auth_request /_auth;`. Of the token check, only the subrequest reaches auth-service.
- **How it works:**
  - `location = /_auth { internal; proxy_pass http://auth/verify; proxy_pass_request_body off; … }`.
  - Protected locations use `auth_request_set $auth_user $upstream_http_x_user_id;` and send it with `proxy_set_header X-User-ID $auth_user;`, which overwrites anything the client sent.
  - A 401 from the subrequest maps to the JSON error page. An unreachable auth-service gives a 500, mapped to a JSON 503 ("auth unavailable"), so a broken auth path never fails open.
  - Optional: the `/_auth` location caches 200/401 verdicts for 10s keyed by the token. That's the classic "auth subrequest on every call is too expensive" lesson.
- **Pros:**
  - The real pattern behind ingress-nginx `auth-url`, oauth2-proxy and pomerium; the gateway has no secret.
  - Revocation and logic live in one service.
  - Every lesson is visible: subrequest cost, `auth_request_set`, failing closed, identity-header hygiene.
  - Matches the diagram already in the README.
- **Cons:**
  - One extra internal hop per protected request (measurable, and the cache lesson addresses it).
  - auth-service becomes a hard dependency of the protected routes.
  - `echo.Run` needs a small extension so auth-service can register its own routes.
- **Best when:** many services, one identity provider, and a gateway that shouldn't hold signing keys (most real setups).

### B — JWT verified inside nginx with njs

- **Summary:** auth-service only issues tokens. nginx verifies the HS256 signature and expiry in an njs function (`js_set $jwt_sub`), with the key mounted into the gateway.
- **How it works:** `load_module ngx_http_js_module.so`; a small `jwt.js` splits the token, checks the HMAC with `crypto`, and compares `exp`. Protected locations `return 401` when `$jwt_sub` is empty.
- **Pros:**
  - No subrequest per call.
  - Because `js_set` variables are evaluated lazily, a rate-limit key can use the verified subject, so per-user limits become possible (A can't do that).
- **Cons:**
  - The signing secret lives in the gateway.
  - Revocation is impossible before `exp`.
  - Security logic in hand-written JavaScript inside the proxy is easy to get subtly wrong (`alg` confusion, timing-safe comparison).
  - njs is a separate module to load and debug, and the lab's Go tests can't unit-test it directly.
- **Best when:** one gateway, stateless tokens, and per-user rate limits are a hard requirement.

### C — `auth_basic` everywhere

- **Summary:** htpasswd files guard admin and the API; no auth-service code.
- **How it works:** `auth_basic` + `auth_basic_user_file` in each protected location; generated htpasswd (openssl `-apr1`).
- **Pros:** smallest change, nothing to run, easy to read.
- **Cons:**
  - Browsers' Basic dialog doesn't suit an SPA; the password goes on every request.
  - No expiry, roles or identity headers.
  - CORS with credentials gets awkward.
  - It teaches the least about gateway auth.
- **Best when:** a single internal tool behind the gateway (exactly the admin host, which uses it in A and B too).

## Supporting decisions (whichever approach wins)

1. **Admin host:** `auth_basic` with a generated `htpasswd` (bcrypt is unsupported by nginx's crypt on musl, so `openssl passwd -apr1`); only `admin.localhost` gets it.
2. **Login moves to `POST /api/auth/login`** with Phase 5's login zone (5 r/s, burst 10). The old `/api/users/login` paths answer 308 to it so nothing silently loses the limit; the Phase 5 login tests move to the new path (same assertions).
3. **Protected:** `/api/users/`, `/api/orders/` (A/B), with roles: `admin` may DELETE orders, `user` may not (403). Public: `/api/products/` (stays cached), `/health`, login, static/SPA.
4. **Identity headers are the gateway's alone:** `X-User-ID` and `X-User-Role` are always overwritten (set to `""` on public routes), so a client-sent header never reaches a service.
5. **CORS:**
   - A `map $http_origin $cors_origin` allowlist: `https://app.localhost:8443` and `http://localhost:5173`, the usual dev-server origin.
   - Allowed origins get `Access-Control-Allow-Origin: <origin>`, `Vary: Origin`, `Access-Control-Expose-Headers: X-Request-ID`, and no `Allow-Credentials` (tokens travel in `Authorization`, not cookies).
   - Preflight (`OPTIONS` + `Access-Control-Request-Method`) answers 204 with Allow-Methods/Allow-Headers (`Authorization, Content-Type, X-Request-ID`) and `Max-Age: 600`, before auth and before the rate limit's 401/429. The preflight itself is still rate-limited.
   - The headers live in a snippet included next to `response-headers.conf`, so the JSON error pages (401/403/429/502) carry them too. Without that, browsers hide the real error.
6. **Secrets: `make secrets`** (like `make certs`) generates the token signing key, the admin htpasswd and the lab users' passwords into `secrets/` (gitignored, mounted read-only). The tests read the generated passwords from there. Nothing secret is committed.
7. **Phase 5 API keys:** the known-key budget map stays as is. A per-user budget isn't possible with A, because `limit_req` runs before `auth_request`. The README says so and names B as the way to get it.

## Edge cases

- **Expired, malformed or wrong-signature token**, `alg: none`, or the wrong `alg`: 401. The verifier accepts HS256 only, compares in constant time, and rejects tokens without `exp`.
- **auth-service down:** protected routes give a JSON 503 (fail closed), and public routes keep working.
- **Forged `X-User-ID`/`X-User-Role` from the client:** never reaches a service, on protected or public routes.
- **Preflight with no token:** 204 with CORS headers, never 401. A disallowed origin gets the same answer as today, with no CORS headers.
- **Error pages lose CORS headers** through `add_header` inheritance; the snippet goes in every location, including the named error ones.
- **A cached products answer + CORS:** the headers are added on every answer, cached or not; `Vary: Origin` is sent.
- **Tokens and the cache:** a request with `Authorization` bypasses the products cache (Phase 6). Products stays public, so anonymous callers still share the cache.
- **Login brute force:** the login zone applies to `/api/auth/login`, and wrong passwords answer 401 in roughly constant time.
- **Token in logs:** the access log must never record `Authorization`. It doesn't today; a test keeps it that way.
- **Auth subrequest cache (if built):** keyed by the full token; a 401 is cached only briefly; never shared across tokens.
- **Existing tests on protected routes:** they get a lab token through one helper, so their assertions don't change.

## Recommendation

**A: `auth_request` to auth-service.** The most important reason: the gateway enforces identity without ever holding the signing key, which is how production gateways are built. It also teaches the subrequest, failing closed and identity-header hygiene. B's per-user rate limit is real but costs a secret in the proxy and hand-written crypto in njs; C is kept for the admin host only.

## Decision

**A: `auth_request` to auth-service.** auth-service issues HS256 tokens (`POST /login`) and checks them (`GET /verify` → 204 + `X-User-ID`/`X-User-Role`, or 401). Protected locations use `auth_request`, overwrite the identity headers, and fail closed (JSON 503 when auth-service is unreachable). Supporting decisions 1–7 as written: admin `auth_basic`; login moves to `/api/auth/login` with the Phase 5 zone; users and orders protected with roles, products public; gateway-only identity headers; CORS allowlist with preflight before auth and CORS on error pages; `make secrets` into a gitignored `secrets/`; Phase 5 API-key buckets unchanged.
