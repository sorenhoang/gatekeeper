# Phase 11 — Stretch: njs, Kubernetes Ingress, Nginx vs Envoy

## Problem

This is the final roadmap row: "Stretch: njs, Kubernetes Ingress, Nginx vs Envoy". It names three separate topics, and a learning lab should finish each one it picks with something measured and tested, as Phases 0–10 did. The main question is which of the three to build, and how deep.

What the codebase already gives us:

- **njs.** The gateway image (nginx 1.27.5, alpine) ships `ngx_http_js_module.so`, so njs is one `load_module` line away. The obvious target is auth. Phase 8 checks every API request with an `auth_request` subrequest to auth-service `/verify`, with a 10s verdict cache. A HS256 check in njs (`crypto.subtle`) could replace that hop. Phase 10's `make bench` / `bench-compare` can then measure the difference.
- **Envoy.** Phase 10 left a k6 bench that runs from the compose network. An Envoy container on the same network, with the same routes, can be measured with the same scenarios. That makes "Nginx vs Envoy" a measurement instead of an opinion piece.
- **Kubernetes Ingress.** There is no local cluster: kind, k3d and minikube aren't installed, and Docker Desktop has about 8 GB and 12 CPUs. `kubectl` and `helm` are installed, but **the current kube context is a real EKS dev cluster**. Anything Kubernetes must use its own kubeconfig and must never touch that context. Also, ingress-nginx, the controller most "Kubernetes Ingress with Nginx" material describes, was retired by the Kubernetes project (maintenance ended March 2026). Gateway API (HTTPRoute) with a controller such as NGINX Gateway Fabric, or Envoy Gateway, is the current path.

Constraints:
- Every existing test keeps passing.
- Phase 10's zero-downtime guarantees (reload, `make deploy`) still hold.
- `make up` stays the one command to run the lab.
- No secrets get committed.
- Laptop-scale: the full gate already takes about 15 minutes.

Ambiguity: does "final phase" mean covering all three topics, or finishing the lab well? This design treats it as finishing well, and keeps every topic the lab skips documented on paper.

## Approaches

### A. njs + Envoy, measured; Kubernetes on paper

**Summary.** Build njs auth and an Envoy twin of the gateway. Compare both with the Phase 10 bench. Write the Kubernetes Ingress / Gateway API mapping as README material, without a cluster.

**How it works.**
- **njs:** an `njs` HS256 verifier that sets `X-User-ID` / `X-User-Role` exactly as `auth_request` does. It reads the signing key from the gateway's secrets mount. It is switchable per route, or as a `bench-compare` candidate, so `auth_request` stays available and the two can be measured side by side.
- **Envoy:** an `envoy` service on the compose network (separate host port, e.g. 9443) with the same lab TLS cert. It covers the core of the gateway:
  - prefix routes for users, orders and products;
  - round-robin over the order replicas, with retries for idempotent requests only;
  - `ext_authz` to auth-service `/verify`;
  - a local rate limit;
  - HTTP/2.
- **Comparison:** `make bench TARGET=envoy` runs the same k6 scenarios against both gateways. The README gets a "Nginx vs Envoy" section: config side by side, numbers, operational differences (reload vs xDS hot restart, observability).
- **Kubernetes on paper:** a README section that maps gatekeeper's config to Ingress and Gateway API objects (HTTPRoute, a policy for rate limits and auth). It explains the ingress-nginx retirement, and gives example manifests that are not applied.

**Pros.**
- Two of the three topics end in measured, tested results, the way every earlier phase did.
- Reuses the Phase 10 tooling directly.
- Stays on the compose stack, so the gate stays reliable.
- Never touches a cluster, so there is zero risk to the work EKS context.

**Cons.**
- Kubernetes stays theoretical, with no hands-on cluster.
- Envoy parity is partial: the core routes only, not caching, gRPC-Web or the admin host. The comparison has to say so.
- One more long-running container: about 100–200 MB of memory.

**Best when:** the goal is to close the lab with measured lessons, without new host tooling.

### B. All three, hands-on

**Summary.** Everything in A, plus a local kind cluster running a Gateway API controller (NGINX Gateway Fabric) in front of the Go services.

**How it works.**
- Install kind with `go install` (Go is present), into the lab folder.
- `make k8s-up` creates a cluster with `KUBECONFIG=.kube/lab.yaml`. The Makefile never uses the default context.
- Load the service images into the cluster, deploy them, install the controller with helm (pinned chart), and apply HTTPRoutes that mirror the routes.
- Smoke tests run against the cluster's port.

**Pros.**
- Covers the whole roadmap row, hands-on.
- Shows the real Ingress → Gateway API migration.

**Cons.**
- A heavy new toolchain: kind, a controller chart, and CRDs.
- A cluster plus the compose stack strains the 8 GB Docker VM.
- Cluster tests are slow and the flakiest part of the lab; the gate would grow well past 15 minutes.
- A mistake with the kube context is a real risk: the default context is a work cluster.
- Most of the time goes into Kubernetes operations, not nginx.

**Best when:** Kubernetes ingress is the main thing to learn, and the extra setup and gate time are acceptable.

### C. njs only, in depth

**Summary.** Use njs for real gateway jobs and measure each one.

**How it works.**
- HS256 verification instead of `auth_request`.
- A/B routing by a cookie hash.
- JSON response shaping (strip internal fields from upstream answers).
- A `js_periodic` job that reloads the key.
- Each one comes with tests, and a bench where it matters.
- Envoy and Kubernetes go to a short "what's next" note.

**Pros.**
- The smallest scope, and pure nginx, in keeping with the lab's subject.
- Deep coverage of scripting, which earlier phases never touched.

**Cons.**
- Drops two of the row's three topics.
- No comparison, so the lab never shows where nginx stops being the right tool.

**Best when:** the lab should stay strictly about nginx.

## Edge cases (any approach)

- **Kube context safety:** no lab command uses the default kubeconfig. In B, every `kubectl`/`helm` call sets `KUBECONFIG` to a file inside the lab. In A, the manifests are never applied.
- **njs auth parity:** the njs check must answer exactly like auth-service:
  - same 401s for expired or wrong-signature tokens and for `alg: none`;
  - same identity headers;
  - same CORS preflight bypass;
  - same `?access_token=` handling on `/ws/` and `/api/events/`;
  - whatever auth-service checks beyond the signature (for example `jti`, or the user still existing) is either kept or listed as a deliberate difference.
- **Key handling:** njs reads `secrets/auth/jwt.key` from a read-only mount. The key is never put into the nginx config or logs. Rotating the key needs a reload; the README says so.
- **Fair comparison:** nginx and Envoy get the same TLS, the same HTTP/2, the same upstreams, and comparable logging (both on or both buffered). The k6 run is identical, and the runs are interleaved as in Phase 10. Every feature only one side has is listed.
- **Ports and the existing stack:** Envoy on a free host port (8443, 8080, 8086 and 8081 are taken). It is excluded from `make deploy` and from the Phase 5 rate limits, the same as the compose network.
- **Bench noise:** the same warm-up and medians as Phase 10. The 10% bar applies only when a change is kept.
- **Docker Desktop IPv6 resets** after heavy runs (Phase 10): the same setup mitigations, and Envoy tests recreate what they load.
- **Roadmap:** 10 ☑, 11 ◐ during the work. Since this is the final phase, the README also gets a short "where to go next" section.

## Recommendation

**A.** It finishes two topics as measured, tested results on the stack the lab already trusts, reusing Phase 10's bench, without risking the work EKS context. The Kubernetes part is best taught as a Gateway API mapping, because the ingress-nginx project it would otherwise teach is retired.

## Decision

**A** — njs auth and an Envoy twin of the gateway, both measured with the Phase 10 bench; Kubernetes Ingress / Gateway API mapped in the README, not applied to any cluster. Chosen by the user at G1.
