# Plan — Phase 9 Realtime & gRPC

## Approach (design A)

A new `realtime-service` (Go 1.26) serves WebSocket `/ws` (`coder/websocket`), SSE `/events` (standard library) and gRPC on :9090 (`grpc-go`: health + a hand-written `gatekeeper.Echo` with `wrapperspb.StringValue`). The gateway adds `/ws/` and `/api/events/` on the HTTPS server, both behind Phase 8's `auth_request`, with `proxy_read_timeout 1h` (the service heartbeats keep them alive) and `proxy_buffering off` for SSE. gRPC paths go to `grpc_pass grpc://realtime` with `grpc_set_header` for identity and request id, and error pages that answer gRPC statuses. `proxy-headers.conf` gets the `$connection_upgrade` map. The `/_auth` subrequest takes the token from `?access_token=` when there's no Authorization header; the token is removed before proxying, and the log's `request` field drops the query. `limit_conn` caps realtime connections per IP; `worker_shutdown_timeout 30s` bounds a reload's drain.

## Affected files

| File | Create/Modify/Delete | Reason |
|---|---|---|
| `services/go.mod`, `services/go.sum` | Modify / Create | go 1.26; grpc-go, coder/websocket |
| `services/Dockerfile` | Modify | golang:1.26-alpine |
| `services/internal/realtime/` (ws.go, sse.go, grpc.go + tests) | Create | handlers and the Echo service |
| `services/cmd/realtime-service/main.go` | Create | HTTP :8080 (echo.RunWith) + gRPC :9090 |
| `docker-compose.yml` | Modify | realtime-service, unpublished; gateway depends on it |
| `nginx/nginx.conf` | Modify | connection_upgrade map, access_token map, log request field, limit_conn zone, worker_shutdown_timeout |
| `nginx/snippets/proxy-headers.conf` | Modify | Upgrade + Connection from the map; drop access_token |
| `nginx/snippets/auth-subrequest.conf` | Modify | Authorization from access_token when the header is missing |
| `nginx/snippets/grpc-errors.conf` | Create | error_page → gRPC statuses (14, 16, 8, 7) |
| `nginx/conf.d/upstreams.conf` | Modify | upstream realtime (http) and realtime_grpc |
| `nginx/conf.d/default.conf` | Modify | `/ws/`, `/api/events/`, gRPC locations, JSON 429 for limit_conn |
| `services/smoke/realtime_test.go`, `realtime_lifecycle_test.go`, `grpc_test.go`, `grpc_lifecycle_test.go` | Create | F-2/F-3 tests |
| `services/smoke/readme_routes_test.go` | Modify | roadmap 8 ☑ 9 ◐ |
| `README.md` | Modify | Realtime & gRPC section, tech stack, route table |

## Dependencies & order

Sequential: F-1 (service + toolchain, testable alone) → F-2 (gateway WS/SSE, shared header and log changes) → F-3 (gRPC on top of the same auth and limits; README).

## Risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| The Connection map breaks upstream keepalive | Medium | Phase 3 tests red | empty value for non-upgrade requests; keepalive tests in the gate |
| The Go 1.26 bump changes another service's behaviour | Low | red tests | all unit and smoke tests run in the gate; no code changes elsewhere |
| A WebSocket client over TLS negotiates h2 and can't upgrade | High (nginx has no RFC 8441) | confusing failures | test client pins http/1.1; documented |
| Token in the query leaks to logs/services | Medium | security | the log field drops the query; the token is stripped from the proxied URI; tests grep the logs |
| Long streams slow the suite | Medium | slower gate | heartbeat intervals overridable by env for tests (5s), 20s waits only in a few tests |
| A reload during the suite leaves old workers | Medium | stray connections | worker_shutdown_timeout 30s; the reload test closes its own streams |
| grpc_pass error pages without content-type | Known (probed) | clients see 'malformed header' | 200 + application/grpc + grpc-status |

## Out of scope

WebSocket over HTTP/2 (nginx lacks RFC 8441). gRPC-Web for browsers. Server-streaming or bidirectional gRPC methods beyond what the tests need. Per-user connection limits (same preaccess-vs-access limit as Phase 8). Load-balancing long-lived connections across several realtime replicas.
