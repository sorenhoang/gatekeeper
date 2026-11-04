# Plan — Phase 10 Performance tuning & zero-downtime

## Approach (design A)

- **Measure:** k6 runs from its official image on the compose network (exempt from Phase 5's limits, no port-forward overhead). `make bench` does a warm-up, then median-of-3 per scenario; `make bench-compare` interleaves baseline and candidate runs.
- **Tune:** a candidate is kept only with a ≥10% median gain and no extra failures. The README records the kept and rejected candidates with their numbers.
- **Zero-downtime:**
  - **Reload:** load runs through reloads, asserting zero failures.
  - **`make deploy SERVICE=…`:** a drained rolling recreate. Mark the replica `down` in place in upstreams.conf, reload, wait for old workers to finish, recreate, wait healthy, un-mark, reload; one replica at a time.

## Affected files

| File | Create/Modify/Delete | Reason |
|---|---|---|
| `loadtest/mixed.js`, `loadtest/orders.js`, `loadtest/static.js` | Create | k6 scenarios |
| `loadtest/candidates/*.sh` | Create | apply/revert one tuning candidate in place |
| `scripts/bench.sh` | Create | warm-up, runs, medians, JSON summaries |
| `scripts/deploy.sh` | Create | drained rolling deploy |
| `Makefile` | Modify | `bench`, `bench-compare`, `deploy` targets |
| `nginx/nginx.conf`, `nginx/conf.d/upstreams.conf` | Modify | only the candidates that measured ≥10% |
| `services/smoke/bench_lifecycle_test.go`, `tuning_test.go`, `deploy_lifecycle_test.go` | Create | F-1/F-2/F-3 tests |
| `services/smoke/readme_routes_test.go` | Modify | roadmap 9 ☑ 10 ◐ |
| `README.md` | Modify | Performance & zero-downtime section, tech stack, layout |

## Dependencies & order

Sequential: F-1 (the measuring tool) → F-2 (uses it to decide the tuning) → F-3 (uses the load scripts for the zero-downtime tests; README).

## Risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Laptop noise hides or fakes a gain | High (probed: 13.8k–37.3k cold) | wrong tuning | warm-up, interleaved A/B runs, medians, a 10% bar |
| Buffered access log breaks Phase 4 log tests | Medium | red tests | flush 200ms (< 300ms read window); full suite in the gate |
| A deploy leaves upstreams.conf with `down` | Medium | lost capacity | the script restores on every path; a test checks the file is byte-identical |
| Load tests make the gate slower/flakier | Medium | longer runs | short durations in tests; numbers only in README, never asserted |
| Old workers pile up during a deploy (Phase 9 streams) | Low | slow drain | wait at most worker_shutdown_timeout per step |

## Out of scope

Blue/green gateway (design C). Kernel/sysctl tuning (Docker Desktop's VM). HTTP/3. Tuning the Go services.
