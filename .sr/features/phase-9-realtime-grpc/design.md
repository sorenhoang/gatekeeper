# Phase 9 — Realtime & gRPC (WebSocket, SSE, gRPC proxy)

## Problem

Every route so far is short request/response over HTTP/1.1 to the services. Phase 9 proxies three long-lived or HTTP/2-native protocols through the gateway:
- **WebSocket**: an HTTP/1.1 `Upgrade` that turns into a two-way connection.
- **Server-Sent Events (SSE)**: one response that stays open and streams `data:` lines.
- **gRPC**: HTTP/2 end to end, with trailers carrying the status.

Each must pass the gateway's existing layers (TLS, auth, rate limits, logging) without those layers breaking it, and must fail visibly when misconfigured, which is the lab's "break it" lesson.

Grounding (this repo):
- The README's tech stack row already says "standard library `net/http`; WebSocket and gRPC libraries only from Phase 9". The Go module has no dependencies today. `go.mod` says `go 1.23`, the Dockerfile builds with `golang:1.23-alpine`, and the host has Go 1.27.
- `snippets/proxy-headers.conf` sets `proxy_http_version 1.1` and `proxy_set_header Connection ""` (for Phase 3's keepalive). A WebSocket upgrade needs `Upgrade` passed and `Connection: upgrade`. A second `proxy_set_header Connection` in the same location would send both values, so the snippet itself must decide.
- Read timeouts are 2s (users), 5s (products), 15s (orders). An idle stream longer than that is cut with a 504 or a closed connection.
- `proxy_buffering` is on, nginx's default. An SSE event would sit in nginx's buffer instead of reaching the client.
- Phase 7: clients reach the gateway over TLS with HTTP/2 (`http2 on`). nginx does not support WebSocket over HTTP/2 (RFC 8441 extended CONNECT), so WebSocket clients must use HTTP/1.1. Browsers fall back automatically, but Go clients must ask for it.
- Phase 8: protected routes use `auth_request`, which reads `Authorization`. A browser `WebSocket` or `EventSource` cannot set that header. And the access log's `request` field is `$request`, which includes the query string, so a token passed in the URL would be logged.
- Phase 5: `limit_req` counts requests, not open connections. A client can hold many streams open within its request budget.

Constraints: no secret in logs (Phase 8's "never log the token" test must stay true), existing tests keep their assertions, and new dependencies are named here so G1 approves them.

## Approaches

### A — One realtime-service with real libraries (recommended)

- **Summary:** a new Go service, `realtime-service`, serves WebSocket on `/ws` (`github.com/coder/websocket`, a small, maintained, context-aware library), SSE on `/events` (standard library), and a gRPC server on a second port (`google.golang.org/grpc`, with the standard health service plus a tiny hand-written `gatekeeper.Echo` service using `wrapperspb.StringValue`, so no protoc step). The gateway routes `/ws/` and `/api/events/` to its HTTP port, and gRPC paths `/gatekeeper.Echo/` and `/grpc.health.v1.Health/` with `grpc_pass grpc://realtime-service:9090` (h2c to the upstream).
- **How it works:**
  - **Connection map:** `map $http_upgrade $connection_upgrade { default upgrade; "" ""; }` replaces the fixed `Connection ""` in proxy-headers. Keepalive stays for normal requests, and WebSocket gets `Upgrade` + `Connection: upgrade`.
  - **Per-protocol locations:** each sets its own read timeout (WebSocket 1h with server pings every 30s; SSE 1h with a comment heartbeat every 15s). SSE has `proxy_buffering off` and no cache. gRPC uses `grpc_read_timeout`, `grpc_set_header` for request id and identity, and `error_page` mapped to a gRPC status, so gRPC clients get `grpc-status`, not an HTML 502.
  - **Auth (Phase 8) on the handshake:** gRPC sends `authorization` metadata natively. Browsers send `access_token` in the query, which the gateway copies into the `Authorization` of its `/_auth` subrequest. The log's request field switches to a form without the query, and a test proves the token never appears.
  - **`limit_conn`:** a per-IP cap on simultaneous streams.
- **Pros:**
  - It's what production does: these libraries are the standard choices.
  - Every gateway lesson is visible: the Upgrade map, buffering, timeouts versus heartbeats, grpc_pass and h2c, gRPC error mapping, `limit_conn`, auth on a handshake.
  - Tests use the same libraries as clients.
  - The README already promised these libraries for this phase.
- **Cons:**
  - Two new dependencies (plus their transitive ones), in the module and in the Docker build.
  - grpc-go's minimum Go version may need the `go` line and the Dockerfile base bumped from 1.23. That gets probed in plan.
  - It adds one more service to compose.
- **Best when:** the lab is teaching how real systems run (its stated goal).

### B — Standard library only, protocols by hand

- **Summary:** SSE with `net/http` flushing; a minimal RFC 6455 WebSocket (handshake + text frames + ping) written in `internal/ws`; gRPC framing by hand over HTTP/2 (5-byte length prefix, `grpc-status` trailer). It would need h2c in the standard library (`http.Protocols`, Go 1.24+), so a toolchain bump.
- **How it works:** the same gateway config as A; the services and test clients implement the wire formats themselves.
- **Pros:**
  - No third-party code.
  - You see exactly what nginx forwards: frames, the 101 response, trailers.
- **Cons:**
  - It's a lot of protocol code to get right (masking, fragmentation, close codes, HTTP/2 trailers), and it isn't what anyone deploys.
  - Bugs in the hand-written client and server could hide gateway bugs.
  - It still needs the toolchain bump.
  - It contradicts the README's plan.
- **Best when:** the lesson is the wire format itself rather than the gateway.

### C — WebSocket + SSE now, gRPC later

- **Summary:** A without gRPC. Only the WebSocket library is added; gRPC moves to Phase 10 or a later extra.
- **How it works:** the same as A for `/ws` and `/api/events`; no grpc_pass, no second port.
- **Pros:**
  - Smaller: one dependency, no toolchain question, faster to finish.
- **Cons:**
  - It leaves the roadmap row half done.
  - gRPC is where the HTTP/2-to-upstream and trailer lessons live, and the phase 7 HTTP/2 front door was partly groundwork for it.
- **Best when:** time is short or the toolchain bump is unwelcome.

## Supporting decisions (whichever approach wins)

1. **Routes:** `/ws/` (WebSocket), `/api/events/` (SSE), and the gRPC services (A only). All are on the HTTPS server; plain HTTP keeps redirecting.
2. **Auth:** all three are protected like users/orders (a token, through `auth_request`). Browsers pass `?access_token=`; the gateway uses it only when there's no `Authorization` header, and removes it before proxying to the service.
3. **Logs never carry the token:** the access log's `request` field becomes `"$request_method $uri $server_protocol"` (no query string) for every route. That's a visible change to the Phase 4 log format, listed for G2.
4. **Timeouts and heartbeats:** the stream locations allow 1h of silence; the service sends a WebSocket ping every 30s and an SSE comment every 15s. A test proves a stream survives longer than the old 15s limit, and the README explains timeouts versus heartbeats.
5. **`limit_conn`:** a zone keyed per client IP (compose networks exempt, as in Phase 5) caps simultaneous realtime connections at a small number (planned: 10). The next connection gets a JSON 429-style answer, or a gRPC `RESOURCE_EXHAUSTED` status for gRPC.
6. **Drain on reload:** `nginx -s reload` keeps old workers alive for open streams until `worker_shutdown_timeout` (planned 30s, then they're closed). This is documented and tested, since a reload otherwise leaves old workers alive for hours.

## Edge cases

- **WebSocket over HTTP/2:** nginx doesn't support it, so the handshake needs HTTP/1.1. Browsers fall back automatically; a Go client must pin `http/1.1` ALPN. A test shows an h2-only attempt fails clearly.
- **Keepalive for normal requests** must stay intact after the Connection map: Phase 3's keepalive tests keep passing.
- **SSE through gzip and the cache:** `text/event-stream` isn't in `gzip_types`, and the events location must never be cached (Phase 6's cache is only on products).
- **SSE and buffering:** with `proxy_buffering on` an event would arrive late or in bursts. A test measures that an event is delivered within 1s of being sent.
- **The backend restarts mid-stream:** the client sees the stream end (SSE `EventSource` reconnects with `Last-Event-ID`), and the gateway logs a 499 or 502, never an infinite hang.
- **gRPC errors from nginx:** no upstream gives `UNAVAILABLE` (14) with `grpc-status`/`grpc-message`, not HTML; a missing token gives `UNAUTHENTICATED` (16); `limit_conn` gives `RESOURCE_EXHAUSTED` (8).
- **gRPC and `limit_req`:** one HTTP/2 connection carries many RPCs, and each RPC is a request that counts against the API budget.
- **Token in the query** never reaches the service or the logs; a client-sent `X-User-ID` is still dropped.
- **Reload with open streams:** old workers drain until `worker_shutdown_timeout`.
- **Many open streams per client:** blocked by `limit_conn` before a backend is touched.

## Recommendation

**A: one realtime-service using `coder/websocket` and `grpc-go`.** Most important reason: the lab teaches the gateway, and real libraries keep the protocol code trustworthy, so every failure a test sees is the gateway's (missing Upgrade map, buffering, timeouts, grpc_pass errors) rather than a bug in hand-written framing. It also delivers the README's stated plan. The one real cost, a possible Go toolchain bump for grpc-go, gets measured in plan before anything is written.

## Decision

**A: one realtime-service with real libraries.** WebSocket (`github.com/coder/websocket`) on `/ws/`, SSE (standard library) on `/api/events/`, and gRPC (`google.golang.org/grpc`: the standard health service plus a hand-written `gatekeeper.Echo` using `wrapperspb.StringValue`, no protoc) via `grpc_pass` over h2c. The gateway gets a `$connection_upgrade` map in proxy-headers, per-protocol timeouts with service heartbeats, `proxy_buffering off` for SSE, and gRPC error statuses. Supporting decisions 1–6 as written: routes; Phase 8 auth (token in `authorization` or `?access_token=`, never forwarded); the access log's request field without the query string; 1h stream timeouts with heartbeats; a `limit_conn` per-IP cap; drain on reload with `worker_shutdown_timeout`. The Go toolchain version needed by grpc-go gets probed in plan.
