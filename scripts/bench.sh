#!/bin/sh
# Load-test the gateway (README "Performance & zero-downtime").
#   SCENARIOS  k6 scripts in loadtest/, without .js  (default: mixed)
#   RUNS       measured runs per scenario            (default: 3)
#   DURATION   per run                               (default: 15s)
#   VUS        concurrent k6 users                   (default: 50)
#   WARMUP     1 runs the warm-up, 0 skips it         (default: 1)
# One warm-up run per scenario comes first and is discarded: cold runs
# measured 13.8k, 28.6k and 37.3k req/s in a row on the lab machine.
# Prints `<scenario> <median_rps> <p95_ms> <failed>`, where failed counts
# failed checks (a check covers status and content; k6's own http_req_failed
# would call a 3xx or a degraded answer a success). Each run's k6 JSON
# summary lands in loadtest/results/ (gitignored).
set -eu

SCENARIOS="${SCENARIOS:-mixed}"
RUNS="${RUNS:-3}"
DURATION="${DURATION:-15s}"
VUS="${VUS:-50}"
WARMUP="${WARMUP:-1}"
K6_IMAGE="grafana/k6:0.57.0"

root=$(cd "$(dirname "$0")/.." && pwd)
[ -s "$root/secrets/auth/users.json" ] || { echo "bench: no lab users; run make secrets" >&2; exit 1; }
mkdir -p "$root/loadtest/results"
gw=$(docker compose --project-directory "$root" ps -q gateway)
[ -n "$gw" ] || { echo "bench: the gateway isn't running (make up)" >&2; exit 1; }
# The compose network (the first one the gateway is on): its addresses are
# exempt from limit_req (Phase 5).
net=$(docker inspect -f '{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}' "$gw" | cut -d' ' -f1)

k6() { # k6 <scenario> [summary file]
	docker run --rm --network "$net" \
		-e VUS="$VUS" -e DURATION="$DURATION" \
		-v "$root/loadtest:/scripts:ro" \
		-v "$root/secrets/auth/users.json:/secrets/users.json:ro" \
		-v "$root/loadtest/results:/results" \
		"$K6_IMAGE" run --quiet --no-color ${2:+--summary-export "/results/$2"} "/scripts/$1.js" >/dev/null || {
		code=$?
		# 99: a threshold failed. The run happened; failures are counted
		# from the summary. Anything else (a script or setup error, a
		# docker error) is a broken bench.
		[ "$code" -eq 99 ] || { echo "bench: k6 failed for $1 (exit $code)" >&2; exit 1; }
	}
}

for s in $SCENARIOS; do
	[ "$WARMUP" = 0 ] || k6 "$s"
	stamp=$(date -u +%Y%m%dT%H%M%SZ)
	set --
	i=1
	while [ "$i" -le "$RUNS" ]; do
		f="$s-$stamp-$i.json"
		k6 "$s" "$f"
		set -- "$@" "$root/loadtest/results/$f"
		i=$((i + 1))
	done
	python3 - "$s" "$@" <<'EOF'
import json, statistics, sys
scenario, paths = sys.argv[1], sys.argv[2:]
rps, p95, failed = [], [], 0
for p in paths:
    m = json.load(open(p))["metrics"]
    rps.append(m["http_reqs"]["rate"])
    p95.append(m["http_req_duration"]["p(95)"])
    # A scenario without any check has no "checks" metric.
    failed += int(m.get("checks", {}).get("fails", 0))
print(f"{scenario} {statistics.median(rps):.0f} {statistics.median(p95):.2f} {failed}")
EOF
done
