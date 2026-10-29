# Gatekeeper

A hands-on lab for learning **Nginx** by building a small API gateway in front of a set of Go microservices.

The services are intentionally simple. The point of this project is the gateway: every phase adds one Nginx capability (routing, load balancing, rate limiting, caching, TLS, auth, realtime, tuning) and verifies it with real traffic, including deliberately breaking it to see how it fails.

## Architecture

```
                    ┌──────────────────────────────────────┐
  client ──HTTP(S)─▶│              Nginx (gateway)         │
                    │  routing · LB · rate limit · cache   │
                    │  TLS · auth_request · WS/SSE · logs  │
                    └──┬──────────┬──────────┬──────────┬──┘
                       │          │          │          │
                 /     │ /api/users│/api/orders│/api/products
                       ▼          ▼          ▼          ▼
                  frontend   user-svc   order-svc×3  product-svc
                  (static)                              │
                                 auth-svc ◀── auth_request (subrequest)
```

## Tech stack

| Part | Choice |
|---|---|
| Gateway | Nginx (open source, official Docker image) |
| Services | Go (standard library `net/http`, no framework) |
| Orchestration | Docker Compose |
| Load testing | k6, `wrk`, `curl` |
| TLS (local) | mkcert |

## Planned layout

```
gatekeeper/
├── docker-compose.yml
├── Makefile                 # up, down, reload, test, logs, loadtest
├── nginx/
│   ├── nginx.conf           # main context: workers, events, http
│   ├── conf.d/              # one file per virtual server
│   └── snippets/            # reusable blocks (proxy headers, ssl, cors)
├── services/
│   ├── user-service/
│   ├── order-service/
│   ├── product-service/
│   └── auth-service/
├── frontend/                # static files served directly by Nginx
├── loadtest/                # k6 scripts
└── certs/                   # local certificates (git-ignored)
```

## Getting started

> Available after Phase 0 is done.

```bash
make up        # build and start everything
make test      # smoke test all routes through the gateway
make reload    # nginx -t && nginx -s reload (no downtime)
make logs      # tail the gateway access log
make down
```

## Roadmap

| Phase | Topic | Status |
|---|---|---|
| 0 | Foundation: repo skeleton, Go services, Nginx anatomy | ☐ |
| 1 | Static files & routing | ☐ |
| 2 | Reverse proxy deep dive: headers, timeouts, buffering | ☐ |
| 3 | Load balancing & failover | ☐ |
| 4 | Observability: logs, request ID, metrics | ☐ |
| 5 | Rate limiting & protection | ☐ |
| 6 | Caching | ☐ |
| 7 | TLS & HTTP/2 | ☐ |
| 8 | Auth at the gateway & CORS | ☐ |
| 9 | Realtime & gRPC: WebSocket, SSE, gRPC proxy | ☐ |
| 10 | Performance tuning & zero-downtime | ☐ |
| 11 | Stretch: njs, Kubernetes Ingress, Nginx vs Envoy | ☐ |

Detailed phase notes live in `docs/` (kept local, not committed).

## Learning method

Every phase follows the same loop:

1. **Build** the feature with the smallest config that works.
2. **Verify** it with `curl -v`, logs, or a load test.
3. **Break it** on purpose (wrong config, dead upstream, traffic spike) and observe.
4. **Fix and explain** why the fix works.
