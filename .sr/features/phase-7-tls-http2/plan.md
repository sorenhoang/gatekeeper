# Plan — Phase 7 TLS & HTTP/2

## Approach (design B)

TLS terminates at the gateway. A lab CA made by `make certs` (openssl, files in gitignored `certs/`) signs a leaf for localhost / admin.localhost / 127.0.0.1. The default server and `admin.localhost` move to `listen 443 ssl` + `http2 on;` sharing `snippets/tls.conf`; a small port-80 server answers `/health` and redirects everything else to `https://$host:8443$request_uri` (301 GET/HEAD, 308 others). HSTS (max-age=300) on HTTPS only. Upstreams stay HTTP/1.1. The smoke suite switches its base URL to `https://localhost:8443` with the lab CA.

## Affected files

| File | Create/Modify/Delete | Reason |
|---|---|---|
| `scripts/certs.sh` | Create | openssl CA + leaf, idempotent, renews a leaf expiring within 30 days, honours `CERTS_DIR` |
| `Makefile` | Modify | `certs` target; `up` depends on it; readiness loop over https with `--cacert` |
| `docker-compose.yml` | Modify | publish `8443:443`; mount `./certs:/etc/nginx/certs:ro` |
| `nginx/snippets/tls.conf` | Create | certificate paths, protocols, ciphers, session cache, tickets off |
| `nginx/snippets/hsts.conf` | Create | `Strict-Transport-Security: max-age=300` (always) |
| `nginx/conf.d/default.conf` | Modify | main server → 443 ssl + http2; new port-80 redirect server (+/health) |
| `nginx/conf.d/admin.conf` | Modify | 443 ssl + http2; port-80 redirect handled by the redirect server |
| `services/smoke/gateway_test.go` | Modify | `gatewayURL` https; lab-CA TLS config on `http.DefaultTransport` |
| `services/smoke/*_test.go` with own transports (2) | Modify | same TLS config + ForceAttemptHTTP2 |
| `services/smoke/proxy_headers_test.go` | Modify | X-Forwarded-Proto expectation http → https (approved here) |
| `services/smoke/allowlist_test.go` | Modify | in-gateway wget to `https://<addr>:443 --no-check-certificate` |
| `services/smoke/tls_test.go`, `tls_certs_test.go`, `redirect_test.go`, `http2_test.go` | Create | F-1/F-2/F-3 tests |
| `services/smoke/readme_routes_test.go` | Modify | roadmap 6 ☑ 7 ◐; route table host localhost:8443 |
| `README.md` | Modify | TLS & HTTP/2 section, route table, tech stack, roadmap |

## Dependencies & order

Sequential: F-1 (HTTPS added while HTTP still serves; tests still on HTTP) → F-2 (flip: HTTP redirects, suite on HTTPS) → F-3 (h2 behaviour + docs). F-2 cannot start before F-1 because the suite needs the HTTPS listener.

## Risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| A test client built without the lab CA fails after the flip | Medium | many red tests | set the TLS config on `http.DefaultTransport` in one place; grep for every `http.Transport{` (2 today) |
| Go silently stays on HTTP/1.1 with a custom TLS config | High (probed) | HTTP/2 untested | `ForceAttemptHTTP2: true`; the h2 test asserts ProtoMajor == 2 |
| HSTS sticks in the user's browser for localhost | Low | localhost:8080 unusable in that browser for 5 min | max-age=300, no includeSubDomains, README warns |
| Phase 6 worktree stack still holds :8080 | High | `make up` fails | stop it before the first `make up` here |
| Raw-TCP and in-container tests assume plain HTTP | Known | red tests | slow-client stays on :8080 (timeouts set at http level); allowlist uses https://addr:443 |
| Certs missing after a clean checkout | Medium | gateway won't start | `up` depends on `certs`; test covers it |

## Out of scope

HTTP/3/QUIC (no QUIC client here: host curl lacks it, Go would need quic-go). mTLS to upstreams or clients. OCSP stapling (no responder for a private CA). Automatic trust-store installation (mkcert -install stays a manual, optional step). Certificate hot-rotation beyond `make certs` + `make reload`.
