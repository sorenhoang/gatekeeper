#!/bin/sh
# nginx and Envoy under the same k6 load (README "Nginx vs Envoy").
#   SCENARIOS, RUNS, DURATION, VUS as for scripts/bench.sh
# One warm-up each, then RUNS rounds alternating the order, as
# bench-compare does, so drift hits both gateways alike. Prints per scenario
# `<scenario> <gateway> <rps> req/s p95 <ms>ms failed <n>` (medians).
set -eu

SCENARIOS="${SCENARIOS:-mixed orders}"
RUNS="${RUNS:-3}"
root=$(cd "$(dirname "$0")/.." && pwd)
run() { TARGET="$1" SCENARIOS="$2" RUNS=1 WARMUP=0 "$root/scripts/bench.sh"; }

for s in $SCENARIOS; do
	for gw in gateway envoy; do
		TARGET=$gw SCENARIOS="$s" RUNS=1 "$root/scripts/bench.sh" >/dev/null # warm-up
	done
	nginx="" envoy=""
	i=1
	while [ "$i" -le "$RUNS" ]; do
		if [ $((i % 2)) = 1 ]; then
			nginx="$nginx$(run gateway "$s")
"
			envoy="$envoy$(run envoy "$s")
"
		else
			envoy="$envoy$(run envoy "$s")
"
			nginx="$nginx$(run gateway "$s")
"
		fi
		i=$((i + 1))
	done
	python3 - "$s" "$nginx" "$envoy" <<'EOF'
import statistics, sys
scenario, nginx, envoy = sys.argv[1:]
for name, block in (("gateway", nginx), ("envoy", envoy)):
    rows = [l.split() for l in block.splitlines() if l.strip()]
    rps = statistics.median(float(r[1]) for r in rows)
    p95 = statistics.median(float(r[2]) for r in rows)
    print(f"{scenario} {name} {rps:.0f} req/s p95 {p95:.2f}ms failed {sum(int(r[3]) for r in rows)}")
EOF
done
