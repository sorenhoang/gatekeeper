# Plan — Phase 8 Auth at the gateway & CORS

## Approach (design A)

auth-service becomes real: `POST /login` checks a lab user from `secrets/auth/users.json` and returns an HS256 JWT (15 min, signed with `secrets/auth/jwt.key`); `GET /verify` checks the Bearer token and the original method (role rules), answering 204 with `X-User-ID`/`X-User-Role`, 401 or 403. The gateway puts `auth_request /_auth` on the users and orders routes. `/_auth` is internal, sends `X-Original-Method`/`X-Original-URI`, has no body, and caches 204 verdicts for 10s keyed by token+method. The routes overwrite the identity headers with `auth_request_set`; on public routes they are set empty. 401/403/500 from the subrequest map to the JSON 401 / 403 / 503 pages. Login moves to `/api/auth/login` under the Phase 5 login zone. `admin.localhost` gets `auth_basic` with a generated htpasswd. CORS comes from a map-based allowlist snippet included next to response-headers, with preflight answered by `return 204`.

## Affected files

| File | Create/Modify/Delete | Reason |
|---|---|---|
| `scripts/secrets.sh` | Create | jwt key, users.json, admin.htpasswd (openssl), idempotent |
| `.gitignore` | Modify | `secrets/` |
| `Makefile` | Modify | `secrets` target, `up` depends on it; readiness loop accepts 401 |
| `docker-compose.yml` | Modify | auth-service mounts `./secrets/auth`, gateway mounts `./secrets/gateway` (both read-only) |
| `services/internal/auth/` (token.go, users.go, handler.go + tests) | Create | HS256 sign/verify (stdlib), login/verify handlers |
| `services/internal/echo/server.go` | Modify | `RunWith(service, extra func(*http.ServeMux))`; `Run` unchanged for the other services |
| `services/cmd/auth-service/main.go` | Modify | register /login and /verify |
| `nginx/conf.d/upstreams.conf` | Modify | `upstream auth` (resolve, keepalive) |
| `nginx/nginx.conf` | Modify | `proxy_cache_path … keys_zone=auth_cache`, CORS origin map |
| `nginx/snippets/auth.conf`, `nginx/snippets/cors.conf` | Create | auth_request + identity headers; CORS headers |
| `nginx/conf.d/default.conf` | Modify | `/_auth`, auth on users/orders, `/api/auth/login`, old login → 308, JSON 401/403/503 pages, preflight, identity headers cleared on products |
| `nginx/conf.d/admin.conf` | Modify | auth_basic |
| `services/smoke/gateway_test.go` (+ transports) | Modify | lab token attached to users/orders requests |
| `services/smoke/login_limit_test.go`, `api_limit_test.go`, `ratelimit_*` | Modify | login path + the approved 405 expectation |
| `services/smoke/auth_test.go`, `auth_lifecycle_test.go`, `admin_auth_test.go`, `cors_test.go`, `secrets_test.go` | Create | F-2/F-3 tests |
| `services/smoke/readme_routes_test.go` | Modify | roadmap 7 ☑ 8 ◐ |
| `README.md` | Modify | Auth & CORS section, route table, architecture note |

## Dependencies & order

Sequential: F-1 (auth-service + secrets, testable in Go alone) → F-2 (gateway uses it; the suite gains the token helper) → F-3 (CORS on top of the final error pages; README).

## Risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| 33 test files hit users/orders and start getting 401 | High | many red tests | one transport wrapper attaches a lab token for those paths only |
| A bearer token on products would bypass the cache and break Phase 6 tests | Medium | red cache tests | the wrapper never touches /api/products/ |
| Auth-verdict cache keyed wrongly leaks a role decision | Low | security | key = token + method; only 204 cached; 401/403 never cached |
| Login spikes now also trigger auth-service work | Low | slower suite | login is a plain proxy to auth-service, no auth_request |
| `sed -i`/regeneration replaces mounted files → stale mounts | Medium | confusing failures | mount directories, scripts write in place, tests remove the container before deleting |
| CORS headers lost on error pages via add_header inheritance | Medium | browsers hide errors | snippet included in every location incl. named error pages; tested on 401/403/429/502 |

## Out of scope

Per-user rate limits (need the identity before preaccess: njs, design B). Refresh tokens, logout and revocation lists. OAuth/OIDC providers. Cookie sessions (so no `Allow-Credentials`, no CSRF). mTLS between gateway and services.
