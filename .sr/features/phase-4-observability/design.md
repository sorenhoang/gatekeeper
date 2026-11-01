# phase-4-observability — Design

## Problem

From `docs/phases/04-observability.md`: every request can be traced from the gateway into the service, and the gateway exposes basic metrics.
- JSON access log (`escape=json`) with the request id and upstream timings (`$request_time`, `$upstream_response_time`, `$upstream_addr`, `$upstream_status`).
- Propagate `X-Request-ID` to services ("reuse the incoming one if present"); services log it; return it to the client.
- Conditional logging to skip health checks.
- `stub_status` on an internal-only port; exporter + Grafana optional.
- Break-it: make product-service slow and find the culprit from the gateway log alone.
- Done when: given a request id, I can find it in gateway and service logs within seconds.

Starting point (Phases 0–3, merged): access log is `combined`; services already log one `key=value` line per request with `request_id=<X-Request-ID or ->`; every location sets `add_header X-Route …`; the proxy snippet is the only place `proxy_set_header` lives; orders has retries and a loopback backup on `127.0.0.1:8081`.

Constraints found while grounding:
- **Client-sent request ids are untrusted.** Today `X-Request-ID` passes through unchanged. The services log it raw inside a space-separated `key=value` line: `X-Request-ID: x status=200 service=user-service` would forge fields in the service log. The gateway's own JSON log is safe (`escape=json`); the services' logs are not. Header values can't contain newlines, but spaces and `=` are fine to HTTP.
- **`add_header` inheritance trap, again.** Every location already has its own `add_header X-Route`, so an `add_header X-Request-ID` at server or http level is silently dropped in all of them. It has to be in every location that sets headers — one snippet included everywhere, like Phase 2's proxy headers, guarded by a test.
- **Port 8081 is taken** by Phase 3's backup (`127.0.0.1:8081`), and Phase 3's locked test asserts `localhost:8081` is unreachable from the host. The phase notes' `curl localhost:8081/nginx_status` would collide on both counts; Phase 8 also relies on only `:8080` being published.
- **Retries and the backup make some fields lists**: `$upstream_addr` is `"172.19.0.6:8080, 172.19.0.4:8080"` and `$upstream_response_time` `"1.001, 0.004"` after a retry. Fields must stay strings in the JSON; tools reading them must not assume one value.
- **The orders backup is a second nginx server**: a degraded request is logged twice (gateway server + fallback server), and with id reuse both lines would carry the same id.
- **`$upstream_addr` is an IP.** To find "the slow service" from the gateway log alone, the line needs the route/upstream name too (`$route` exists since Phase 2; `$proxy_host` gives the upstream).
- **Existing tests read the logs**: Phase 0's `make logs` test looks for `GET <marker>` in the access log; Phase 2 tests parse the *error* log (unchanged). A JSON field `"request":"GET /x HTTP/1.1"` keeps the former working.
- Image is nginx 1.27.5: `$request_id` (32 hex chars) exists; no `headers-more`, no `add_header_inherit`.

Out of scope: Prometheus/Grafana/exporter containers (optional in the notes; three more images, no new nginx concept beyond `stub_status`) — README shows how to add them; log shipping; tracing headers (`traceparent`).

## Approaches

The main decision is the **request id trust policy** at the edge.

### A — Always generate (`$request_id`), ignore the client's header
- **Summary:** `X-Request-ID` to the backend and to the client is always nginx's `$request_id`.
- **How:** `proxy_set_header X-Request-ID $request_id;`, `add_header X-Request-ID $request_id always;`.
- **Pros:** unspoofable, fixed format (32 hex), no validation code; matches Phase 2's "the gateway is the edge" stance.
- **Cons:** a client (or an upstream proxy in a future setup) can't pass its own id through, so a client-side log line can only be joined by reading the id from the response header.
- **Best when:** the gateway is the first hop and clients don't bring ids.

### B — Reuse a well-formed incoming id, otherwise generate
- **Summary:** a `map` keeps `$http_x_request_id` if it matches `^[A-Za-z0-9._-]{1,64}$`, else falls back to `$request_id`.
- **How:** `map $http_x_request_id $req_id { "~^[A-Za-z0-9._-]{1,64}$" $http_x_request_id; default $request_id; }`, then `$req_id` everywhere.
- **Pros:** what the phase notes ask ("reuse the incoming one"); a client can trace its own id end to end; the strict pattern makes log forging impossible in the services' `key=value` lines and bounds the length.
- **Cons:** clients choose ids, so two requests can share one (accidental or deliberate) — correlation, not authentication; one more `map` to explain.
- **Best when:** clients or upstream systems already have an id to carry through (this lab's own tests do).

### C — Reuse any incoming id as-is
- **Summary:** `$http_x_request_id` if present, else `$request_id`.
- **How:** `map $http_x_request_id $req_id { "" $request_id; default $http_x_request_id; }`.
- **Pros:** simplest reading of the notes.
- **Cons:** forged fields in service logs (`x status=500`), unbounded length, spaces breaking every `grep request_id=…`; the same class of trust bug Phase 2 removed for `X-Forwarded-For`.
- **Best when:** every client is trusted.

## Supporting decisions (to confirm with G1)

1. **JSON access log** `log_format gatekeeper escape=json` with: `time` (`$time_iso8601`), `request_id`, `remote_addr`, `host`, `request` (`$request`), `status`, `bytes`, `request_time`, `route`, `upstream` (`$proxy_host`), `upstream_addr`, `upstream_status`, `upstream_connect_time`, `upstream_header_time`, `upstream_response_time`, `user_agent`. All values strings (lists after retries stay readable).
2. **Skip the gateway's own `/health`** via `map $uri $loggable` + `access_log … if=$loggable`. Proxied requests, errors and static files are always logged.
3. **One snippet for response headers**: `snippets/response-headers.conf` with `add_header X-Request-ID $req_id always;`, included in every location that has an `add_header` (all of them today, including the JSON error handlers and the admin server); a test asserts it, mirroring Phase 2's proxy-headers test.
4. **`X-Request-ID` to services** in `snippets/proxy-headers.conf` (`proxy_set_header X-Request-ID $req_id;`), so every service log line has a real id instead of `-`.
5. **`stub_status` on `127.0.0.1:8082`** inside the gateway (not published; 8081 is the orders backup), read with `docker compose exec gateway wget -qO- 127.0.0.1:8082/nginx_status`. `make status` wraps it.
6. **The orders fallback server** sets `access_log off`, so a degraded request is logged once, by the gateway server, with `upstream_addr` `127.0.0.1:8081` making it recognisable.
7. **`make trace ID=<id>`** greps the id across gateway and service logs: the "Done when" in one command.

## Edge cases

- Incoming `X-Request-ID` that fails the pattern (spaces, `=`, 65+ chars, non-ASCII) → replaced, never logged or forwarded (B).
- Several `X-Request-ID` headers from a client → nginx's `$http_x_request_id` is the first; the forwarded header is replaced by `proxy_set_header`, so the service sees exactly one.
- `error_page` internal redirects (Phase 2 JSON 502/504) keep the same `$request_id`; the response must still carry `X-Request-ID` (the named locations set their own `add_header`, so they need the snippet too).
- Requests rejected before a location (`400`, `413`, `414`) still get a log line with an id; they won't have `X-Request-ID` in the response unless the error has a location (acceptable, documented).
- A retried request has one id and one gateway log line, with list-valued upstream fields.
- `$upstream_*` are `-` / empty for requests nginx answers itself (static, SPA, `/health`, JSON 404): the JSON must stay valid.
- `access_log … if=` evaluates at log time; `/health` responses still get `X-Request-ID`.
- Phase 0's `make logs` test (finds `GET <marker>` in the access log) must still pass.

## Recommendation

**B.** It delivers the phase's "reuse the incoming id" — which this lab's own tests and any client-side log rely on — while the strict pattern closes the log-forging hole C would open in the services' `key=value` logs. A is the fallback if correlation by client ids isn't wanted.

## Decision

B — reuse a well-formed incoming `X-Request-ID` (`^[A-Za-z0-9._-]{1,64}$`), otherwise generate `$request_id`; supporting decisions 1–7 as written (JSON access log with upstream timings and route, `/health` not logged, `X-Request-ID` response snippet in every location, forwarded to services via the proxy snippet, `stub_status` on `127.0.0.1:8082` with `make status`, fallback server `access_log off`, `make trace ID=`). User's words: "i prefer option B, please move to next phase /sr:plan".
