# Phase 7 — TLS & HTTP/2

## Problem

The gateway speaks plain HTTP on host port 8080 (`listen 80`, published `8080:80`). Phase 7 terminates TLS at the gateway and serves HTTP/2 to clients, while the services stay plain HTTP/1.1 inside the compose network.

What must be true at the end:
- HTTPS on a published port, with a certificate the lab's own tools (smoke tests, curl) can verify without `-k`.
- HTTP/2 negotiated through ALPN; HTTP/1.1 still works for clients that only offer that.
- Modern protocol settings only: TLS 1.2 and 1.3, a session cache, no session tickets.
- Backends see `X-Forwarded-Proto: https` for HTTPS requests (Phase 2's header snippet already sends `$scheme`).
- No private key in git (`.gitignore` already lists `certs/`, `*.key`, `*.pem`, `*.crt`).

Grounding (measured, this repo):
- `nginx:1.27-alpine` is built with `--with-http_ssl_module --with-http_v2_module --with-http_v3_module` (OpenSSL).
- The README's tech stack names **mkcert** for local TLS, but mkcert is **not installed** on this machine; `openssl` 3.6 is.
- The host curl (8.7.1, SecureTransport) supports HTTP/2, **not HTTP/3**. The Go module has no QUIC library.
- Tests reach the gateway through one constant, `gatewayURL = "http://localhost:8080"` (gateway_test.go), plus raw TCP dials in `slow_client_test.go`; only 2 test clients build their own `http.Transport`. `make up`'s readiness loop curls `localhost:8080/api/<route>/`.
- Every redirect must name the **host** port: inside the container the HTTPS port is 443 but the host sees 8443, so `return 301 https://$host$request_uri` would send clients to port 443, where nothing listens.

Ambiguities:
1. Does plain HTTP keep serving the app, or only redirect? Decided by the approach below.
2. HTTP/3 in or out? Decided by the approach below.
3. Where the certificate comes from: see supporting decision 1.

Constraints:
- Keep the lab runnable with `make up` on a fresh clone: no manual trust-store steps required for the tests.
- Don't add Go dependencies without a reason worth a lesson.
- The full smoke suite is ~8 min; churn in it is cost, and every test that changes must keep its assertions.

## Approaches

### A — HTTPS next to HTTP (no redirect)

- **Summary:** add a `listen 443 ssl` server that serves exactly what the port-80 server serves; publish `8443:443`; 8080 keeps working unchanged.
- **How it works:** move the current server body into a shared include, used by both an HTTP and an HTTPS `server` block. `http2 on;` on the HTTPS one. Tests stay on HTTP; new tests cover HTTPS.
- **Pros:** least churn: no existing test moves. Easy to compare HTTP/1.1 vs HTTP/2 on the same routes.
- **Cons:** not how anyone runs TLS: plaintext stays fully open, no redirect, no HSTS, so half of the phase's lessons (redirect rules, port mapping, HSTS, `X-Forwarded-Proto` everywhere) never happen. The 400 that nginx gives for "plain HTTP on the TLS port" is never shown.
- **Best when:** TLS is an add-on for a few clients, not the front door.

### B — HTTPS is the front door; HTTP only redirects (recommended)

- **Summary:** publish `8443:443` for HTTPS + HTTP/2. Port 8080 answers every request with a redirect to `https://$host:8443$request_uri`: **301** for GET/HEAD, **308** for everything else (a 301 lets clients turn a POST into a GET). HTTPS answers carry `Strict-Transport-Security`.
- **How it works:**
  - Today's default server (and `admin.localhost`) move to `listen 443 ssl` + `http2 on;`, sharing `snippets/tls.conf` (certificate, `ssl_protocols TLSv1.2 TLSv1.3`, `ssl_session_cache shared:SSL:10m`, `ssl_session_tickets off`).
  - A small port-80 server does the redirects. It keeps `/health` answering in plain HTTP for container probes, and keeps Phase 5's slow-client timeouts.
  - The smoke suite moves to `gatewayURL = "https://localhost:8443"` with a cert pool that trusts the lab CA. The default transport and the 2 custom transports get the same TLS config (`ForceAttemptHTTP2: true`: Go drops HTTP/2 when a transport has a custom TLS config).
  - `make up` readiness uses the CA file too. `slow_client_test.go`'s raw dials stay on 8080, because Phase 5's header/body timeouts apply on that server as well, and keeping them there needs no assertion change.
- **Pros:** the realistic shape, with each lesson observable: redirect codes, the port-mapping trap, HSTS, `X-Forwarded-Proto: https`, nginx's 400 for HTTP on the TLS port, h2 vs http/1.1 via ALPN. One server body, not two copies.
- **Cons:** the largest change. Every existing smoke test runs over TLS after it, so the base URL and the clients change (assertions don't). HSTS on `localhost` can stick in a browser, so `max-age` stays short (300s) in the lab and the README says why.
- **Best when:** the gateway is the public entry point (this lab).

### C — B plus HTTP/3 (QUIC)

- **Summary:** everything in B, plus `listen 443 quic reuseport;` with UDP `8443:443/udp` published and an `Alt-Svc: h3=":8443"` header.
- **How it works:** same as B; the tests would need an HTTP/3 client.
- **Pros:** shows the newest protocol and Alt-Svc upgrade discovery.
- **Cons:**
  - Neither tool here can prove it: the host curl has no HTTP/3, and Go needs a new dependency (`quic-go`) just for the tests.
  - Docker Desktop's UDP forwarding adds a new failure mode to a suite that already fights timing flakes.
  - A browser only switches to h3 after it has seen `Alt-Svc`, which is hard to observe in a test.
- **Best when:** the lab is about QUIC itself; better as a later "tuning" extra once a QUIC client is available.

## Supporting decisions (whichever approach wins)

1. **Certificates: generated by `make certs` with openssl, never committed.** A lab CA (10-year) signs a leaf for `localhost`, `admin.localhost`, `127.0.0.1` (398 days: the longest validity Apple and the browsers accept for leaf certs). Files go to `certs/` (gitignored), mounted read-only into the gateway. `make up` runs `make certs` when the files are missing. mkcert stays documented as the optional way to make browsers trust it (`mkcert -install` changes the system trust store, which the lab shouldn't do on its own); the README's tech stack row changes to "openssl (mkcert optional)".
2. **Protocols:** `ssl_protocols TLSv1.2 TLSv1.3` (a TLS 1.1 handshake fails), Mozilla "intermediate" cipher list for 1.2, `ssl_prefer_server_ciphers off` (TLS 1.3 picks for itself), `ssl_session_cache shared:SSL:10m`, `ssl_session_timeout 1d`, `ssl_session_tickets off`. No OCSP stapling: a private CA has no OCSP responder.
3. **HTTP/2 to clients only:** `http2 on;` (the 1.25+ directive; `listen … http2` is deprecated). Upstreams stay HTTP/1.1 keepalive, as today.
4. **Cache/limits unchanged:** the cache key already includes `$scheme`, so HTTPS entries are separate from any HTTP ones; rate limits key on the client IP whichever protocol is used.

## Edge cases

- **Port mismatch in redirects:** the target must be `https://$host:8443…`, not `$scheme://$host` (would be 443). A test follows the `Location` header to prove it lands.
- **POST on 8080:** 308 with the same method and body (a test sends a body and checks the method that arrives at the backend after the redirect is followed).
- **Plain HTTP sent to the TLS port** → nginx's `400 The plain HTTP request was sent to HTTPS port`; worth one test and a README line.
- **Unknown SNI / IP literal:** the default HTTPS server answers with the lab cert (SAN includes 127.0.0.1); `admin.localhost` gets its own server block on 443 too.
- **HSTS:** only on HTTPS responses (on HTTP it's ignored and misleading), short `max-age`, no `includeSubDomains` (would pin `admin.localhost` too).
- **Missing cert files:** the gateway would fail to start. `make up` creates them first; the README says what to do after `make down -v`-style cleanups.
- **Go client and HTTP/2:** a transport with a custom `TLSClientConfig` silently stays on HTTP/1.1 unless `ForceAttemptHTTP2` is set; the HTTP/2 test checks `resp.ProtoMajor == 2`, so this can't hide.
- **Error pages over HTTP/2:** the JSON 502/503/504/429 pages and `X-Request-ID` must look the same under h2 (headers are lowercased on the wire; Go normalizes them).
- **Slow clients on TLS:** `client_header_timeout` still applies after the handshake, and a client that stalls the handshake is cut by the same timeout. The Phase 5 tests stay on 8080 to keep their assertions unchanged.
- **Certificate expiry:** the leaf lasts 398 days; `make certs` regenerates it when it's missing or within 30 days of expiring.
- **Phase 6 stack still running on 8080** (another worktree's compose project) blocks `make up` here; stop it first.

## Recommendation

**B — HTTPS as the front door, HTTP only redirects.** Most important reason: a TLS phase where plaintext keeps serving everything never teaches the parts that break in real deployments: the redirect code and port, HSTS, and `X-Forwarded-Proto`. B makes each of those observable, and the test churn is mostly one base URL and one TLS config, because only 2 test clients build their own transport. HTTP/3 (C) needs a new dependency and a UDP path that this machine's curl can't even check, so it waits.

## Decision

**B — HTTPS is the front door; HTTP only redirects** (301 GET/HEAD, 308 otherwise, to `https://$host:8443`), HTTP/2 via ALPN, HSTS with a short max-age, `/health` still plain on 8080; supporting decisions 1–4 as written (openssl `make certs` lab CA, never committed; TLS 1.2/1.3, session cache, no tickets; h2 to clients only; cache/limits unchanged). HTTP/3 deferred.
