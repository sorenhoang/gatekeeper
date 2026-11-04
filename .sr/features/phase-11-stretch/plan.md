# Plan — Phase 11: njs, Nginx vs Envoy, Kubernetes on paper

## Approach (design.md, G1 = A)

1. **njs auth** is a Phase 10 tuning candidate. The njs code and an internal `/_auth_njs` location are always present. The candidate switches `auth_request` to that location, so `make bench-compare` measures it and the tuning-table tests decide whether it is kept. A parity test runs the Phase 8 auth matrix with and without the switch.
2. **Envoy twin** is a new compose service with a static config (`envoy/envoy.yaml`). It covers TLS + h2, the three API routes, round-robin with GET-only retries, and ext_authz to auth-service. It deliberately skips the response cache, the rate limits, the JSON error pages, the static site and the admin host.
3. **Comparison and docs.** `bench.sh` gains `TARGET` (the k6 scripts already read `BASE`). The README gains the comparison, the Kubernetes mapping and the closing section.

## Affected files

| File | Create/Modify/Delete | Reason |
|---|---|---|
| `nginx/nginx.conf` | Modify | `load_module modules/ngx_http_js_module.so;` `js_import` |
| `nginx/njs/auth.js` | Create | HS256 verify mirroring `services/internal/auth/token.go` and the DELETE rule in `handler.go` |
| `nginx/conf.d/default.conf` | Modify | internal `location = /_auth_njs { js_content auth.verify; }` |
| `nginx/snippets/auth-check.conf` | Modify | `auth_request_set` from `$sent_http_x_user_*` (works for both paths) |
| `docker-compose.yml` | Modify | signing key into the gateway (read-only); `envoy` service, port 127.0.0.1:9443, log rotation |
| `loadtest/candidates/njs-auth.env` | Create | candidate for `bench-compare` |
| `envoy/envoy.yaml` | Create | listener, TLS, routes, clusters, ext_authz, retry policy |
| `scripts/bench.sh` | Modify | `TARGET=gateway\|envoy` → `BASE` for k6 |
| `k8s/*.yaml` | Create | example Gateway API manifests (never applied) |
| `README.md` | Modify | njs, Nginx vs Envoy, Kubernetes, roadmap, where next, layout, tech stack |
| `services/smoke/*_test.go` | Create | njs parity, Envoy routes/auth/LB/retry, bench target, docs |

## Dependencies & order

F-1 → F-2 → F-3, in sequence. F-3 needs F-2's Envoy for its numbers, and F-1's row for the final README. F-1 and F-2 touch different files and could run in parallel, but the gate uses one stack, so they run one after the other.

## Risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| The signing key in the gateway weakens Phase 8's boundary: the gateway could mint tokens | certain if njs auth is done this way | medium (lab) | read-only mount, never in config or logs, README says why production uses ES256/RS256 with only a public key at the gateway (asked at G2) |
| njs auth loses to the cached auth_request | high | low | expected result for a lab; recorded as rejected with its numbers |
| `$sent_http_x_user_*` doesn't carry the identity for the proxied `/_auth` | medium | medium | probe first in F-1; otherwise keep `$upstream_http_*` and make the candidate switch the variable too |
| Envoy ext_authz header handling differs (client-sent X-User-ID leaks through, or the identity is missing) | medium | high (auth bypass) | explicit tests for both; `request_headers_to_remove` on the route |
| Envoy retries a POST | medium | high | retry policy only on a GET-matched route; a test counts the replicas' requests |
| An unfair comparison (nginx's auth cache, logging differences) | high | medium | list every difference next to the numbers; same k6 scenarios, interleaved runs |
| Docker Desktop [::1] resets / memory with one more container | medium | low | Phase 10 mitigations; Envoy idles at about 50 MB |

## Out of scope

- A real Kubernetes cluster, kind, ingress controllers, helm installs. Nothing here touches the current kube context.
- Envoy parity for caching, rate limiting, WebSocket/SSE/gRPC, the static site, the admin host, and the JSON error pages.
- Moving auth to asymmetric keys (ES256): only described in the README, unless chosen at G2.
- `.claude/` in `.gitignore`: a main-checkout matter, left to the user.
