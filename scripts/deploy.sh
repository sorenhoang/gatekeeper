#!/bin/sh
# Rolling redeploy of a service's replicas with no failed request
# (README "Performance & zero-downtime"). The image is built once, then for
# each replica in turn:
#   1. mark it `down` in upstreams.conf and reload: no new request goes there
#   2. wait until every worker from before the reload has exited: old workers
#      exit only once their requests are done, so the ones already on the
#      replica have answered (or worker_shutdown_timeout, 30s, cut them)
#   3. recreate it and wait until it is healthy (at most 30s)
#   4. remove `down` and reload
# A POST is never retried by the gateway (Phase 3), so stopping a replica
# without step 1-2 can fail the POSTs it is serving (measured: 2 of 674221).
# If anything fails, the replica is put back in rotation, except when the new
# container never got healthy: then it stays `down` and the others serve.
# Usage: SERVICE=order-service scripts/deploy.sh
set -eu

: "${SERVICE:?set SERVICE, e.g. SERVICE=order-service}"
case "$SERVICE" in *[!a-z0-9-]*) echo "deploy: bad SERVICE name '$SERVICE'" >&2; exit 1 ;; esac
root=$(cd "$(dirname "$0")/.." && pwd)
conf="$root/nginx/conf.d/upstreams.conf"
DRAIN_MAX=30   # worker_shutdown_timeout in nginx/nginx.conf
HEALTHY_MAX=30 # seconds for a recreated replica to report healthy

replicas=$(grep -oE "server ${SERVICE}(-[0-9]+)?:8080" "$conf" | sed 's/^server //; s/:8080$//')
[ -n "$replicas" ] || { echo "deploy: no upstream server for $SERVICE in upstreams.conf" >&2; exit 1; }

dc() { docker compose --project-directory "$root" "$@"; }

# In place (same inode), so the gateway's bind mount sees the change.
edit() { # edit <replica> mark|unmark
	python3 - "$conf" "$1" "$2" <<'EOF'
import re, sys
path, replica, mode = sys.argv[1:]
addr = f"server {replica}:8080"
s = open(path).read()
if mode == "mark":
    # " down" right after the address, whatever spacing follows it.
    s, n = re.subn(rf"({re.escape(addr)})(\s)", r"\1 down\2", s)
else:
    n = s.count(f"{addr} down")
    s = s.replace(f"{addr} down", addr)
if n != 1:
    sys.exit(f"deploy: expected one '{addr}' to {mode} in upstreams.conf, found {n}")
with open(path, "r+") as f:
    f.seek(0)
    f.write(s)
    f.truncate()
EOF
	make -s -C "$root" reload >/dev/null
}

# PIDs of the gateway's worker processes; with `alive <pids>`, those still running.
workers() { dc exec -T gateway sh -c "ps -o pid,args | grep '[n]ginx: worker process' | awk '{print \$1}'"; }
alive() { dc exec -T gateway sh -c "for p in $*; do kill -0 \$p 2>/dev/null && echo \$p; done; true"; }

# Refuse to start when capacity is already reduced: never two replicas out.
for r in $replicas; do
	if grep -q "server $r:8080 down" "$conf"; then
		echo "deploy: $r is already marked down; finish or undo that first" >&2
		exit 1
	fi
	if [ -z "$(dc ps --status running -q "$r")" ]; then
		echo "deploy: $r is not running; refusing to take another replica out" >&2
		exit 1
	fi
done

marked="" keep=0
trap 'code=$?; if [ "$code" != 0 ] && [ -n "$marked" ] && [ "$keep" = 0 ]; then
	echo "deploy: failed; putting $marked back in rotation" >&2; edit "$marked" unmark || true; fi' EXIT
trap 'exit 130' INT TERM

echo "deploy: building $SERVICE"
dc build $replicas >/dev/null
for r in $replicas; do
	echo "deploy: $r: draining"
	old=$(workers | tr '\n' ' ')
	# No worker list would make the drain wait pass at once: stop instead.
	[ -n "${old% }" ] || { echo "deploy: can't list the gateway's workers; is it running?" >&2; exit 1; }
	marked=$r
	edit "$r" mark
	i=0
	while [ -n "$(alive $old)" ]; do
		if [ "$i" -ge "$DRAIN_MAX" ]; then
			echo "deploy: $r: old workers still running after ${DRAIN_MAX}s; recreating anyway" >&2
			break
		fi
		sleep 1
		i=$((i + 1))
	done
	echo "deploy: $r: recreating"
	if ! out=$(dc up -d --no-deps --no-build --force-recreate --wait --wait-timeout "$HEALTHY_MAX" "$r" 2>&1); then
		echo "$out" >&2
		echo "deploy: $r did not become healthy; it stays marked down" >&2
		keep=1
		exit 1
	fi
	edit "$r" unmark
	marked=""
	echo "deploy: $r: back in rotation"
done
