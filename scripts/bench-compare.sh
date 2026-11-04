#!/bin/sh
# Compare one tuning candidate against the current config
# (README "Performance & zero-downtime").
#   CANDIDATE  a file in loadtest/candidates/ (without .env); required
#   SCENARIOS, RUNS, DURATION, VUS as for scripts/bench.sh
# After one warm-up, baseline and candidate runs alternate RUNS times, with
# the order flipped every round, so drift on a busy laptop hits both sides
# alike. The candidate is an in-place text replacement of every FROM by TO
# (same inode, so the gateway's bind mount sees it), reverted at the end even
# when a run fails. A candidate already in the config is refused.
set -eu

: "${CANDIDATE:?set CANDIDATE to a file in loadtest/candidates/ (without .env)}"
SCENARIOS="${SCENARIOS:-mixed}"
RUNS="${RUNS:-3}"
root=$(cd "$(dirname "$0")/.." && pwd)
cand="$root/loadtest/candidates/$CANDIDATE.env"
[ -f "$cand" ] || { echo "bench-compare: no $cand" >&2; exit 1; }

swap() { # swap check|apply|revert
	python3 - "$cand" "$root" "$1" <<'EOF'
import sys
cand, root, mode = sys.argv[1:]
spec = {}
for line in open(cand):
    if "=" in line and not line.startswith("#"):
        k, v = line.rstrip("\n").split("=", 1)
        spec[k] = v.replace("\\n", "\n")
path = f"{root}/{spec['FILE']}"
s = open(path).read()
if mode == "check":
    if spec["TO"] in s:
        sys.exit(f"bench-compare: {cand.rsplit('/', 1)[-1]} is already applied in {spec['FILE']}; nothing to compare")
    mode = "apply"
    write = False
else:
    write = True
old, new = (spec["FROM"], spec["TO"]) if mode == "apply" else (spec["TO"], spec["FROM"])
if old not in s:
    sys.exit(f"bench-compare: {spec['FILE']} doesn't contain the text to {mode}")
if write:
    with open(path, "r+") as f:
        f.seek(0)
        f.write(s.replace(old, new))
        f.truncate()
EOF
	[ "$1" = check ] || make -s -C "$root" reload >/dev/null 2>&1 || {
		echo "bench-compare: reload failed after $1" >&2
		exit 1
	}
}

run() { SCENARIOS="$1" RUNS=1 WARMUP=0 "$root/scripts/bench.sh"; }

swap check
applied=0
apply() { applied=1; swap apply; }
revert() { swap revert; applied=0; }
trap '[ "$applied" = 0 ] || revert' EXIT
trap 'exit 130' INT TERM
for s in $SCENARIOS; do
	SCENARIOS="$s" RUNS=1 "$root/scripts/bench.sh" >/dev/null # warm-up
	base="" tuned=""
	i=1
	while [ "$i" -le "$RUNS" ]; do
		if [ $((i % 2)) = 1 ]; then
			base="$base$(run "$s")
"
			apply
			tuned="$tuned$(run "$s")
"
			revert
		else
			apply
			tuned="$tuned$(run "$s")
"
			revert
			base="$base$(run "$s")
"
		fi
		i=$((i + 1))
	done
	python3 - "$s" "$CANDIDATE" "$base" "$tuned" <<'EOF'
import statistics, sys
scenario, cand, base, tuned = sys.argv[1:]
def parse(block):
    rows = [l.split() for l in block.splitlines() if l.strip()]
    return [float(r[1]) for r in rows], [float(r[2]) for r in rows], sum(int(r[3]) for r in rows)
(br, bp, bf), (tr, tp, tf) = parse(base), parse(tuned)
mbr, mbp, mtr, mtp = map(statistics.median, (br, bp, tr, tp))
print(f"{scenario} {cand} baseline {mbr:.0f} req/s p95 {mbp:.2f}ms failed {bf} | "
      f"tuned {mtr:.0f} req/s p95 {mtp:.2f}ms failed {tf} | "
      f"rps {100 * (mtr - mbr) / mbr:+.1f}% p95 {100 * (mtp - mbp) / mbp:+.1f}%")
EOF
done
