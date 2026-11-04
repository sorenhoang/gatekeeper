#!/bin/sh
# Wait until every API route reaches its service (run by `make up` after the
# reload). Users and orders sit behind auth_request, which answers 401
# before nginx even contacts the service, so a 401 proves nothing: the check
# logs in as the lab user and wants a 2xx with the token. A route that stays
# down only warns: a stopped service is a valid state, not a failed `up`.
set -u

ROUTES="${ROUTES:-users orders products}"
# 50 x 0.2s per route before only warning.
TRIES="${TRIES:-50}"
BASE="https://localhost:8443"
root=$(cd "$(dirname "$0")/.." && pwd)
c() { curl -s --cacert "$root/certs/ca.pem" "$@"; }

pw=$(sed -n 's/.*"alice":{"password":"\([^"]*\)".*/\1/p' "$root/secrets/auth/users.json" 2>/dev/null)
token=""
i=0
# auth-service may still be starting: retry the login like the routes.
while [ -z "$token" ] && [ "$i" -lt "$TRIES" ]; do
	token=$(c -X POST -H 'Content-Type: application/json' \
		-d "{\"username\":\"alice\",\"password\":\"$pw\"}" "$BASE/api/auth/login" |
		sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
	[ -n "$token" ] || sleep 0.2
	i=$((i + 1))
done
[ -n "$token" ] || echo "warning: no lab token from /api/auth/login; users/orders checks will see 401" >&2

for route in $ROUTES; do
	i=0
	while :; do
		code=$(c -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $token" "$BASE/api/$route/")
		case $code in 2??) break ;; esac
		i=$((i + 1))
		if [ "$i" -ge "$TRIES" ]; then
			echo "warning: /api/$route/ not answering yet (last status $code)" >&2
			break
		fi
		sleep 0.2
	done
done
