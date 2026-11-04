NGINX := docker compose exec -T gateway nginx -c /etc/nginx/gatekeeper/nginx.conf

# Build attestations carry a timestamp, so every rebuild gets a new image id
# and `make up` recreates all services, reshuffling their IPs behind nginx.
export BUILDX_NO_DEFAULT_ATTESTATIONS := 1


.PHONY: certs secrets up down reload logs status trace test test-lifecycle bench bench-compare bench-gateways deploy

# Lab CA + gateway certificate in certs/ (gitignored); keeps valid files,
# reissues a leaf that expires within 30 days. CERTS_DIR overrides the folder.
certs:
	@./scripts/certs.sh

# Signing key, lab users and the admin htpasswd in secrets/ (gitignored);
# keeps existing files. SECRETS_DIR overrides the folder.
secrets:
	@./scripts/secrets.sh

# A smoke run killed mid-test can leave a broken zz-smoke-*.conf behind, which
# would stop the gateway from starting.
up: certs secrets
	rm -f nginx/conf.d/zz-smoke-*.conf
	docker compose up -d --build --wait
	@# A reload re-resolves the upstreams now (measured: 0.2s vs ~3s for a
	@# service that was missing); then wait until each route reaches its
	@# service (scripts/ready.sh: with a lab token, since a 401 from the auth
	@# check comes back before nginx contacts the service at all).
	$(NGINX) -s reload
	@./scripts/ready.sh

down:
	docker compose down --remove-orphans

# nginx -t first: an invalid config never reaches the master. To watch the
# master reject a bad config itself, run `nginx -s reload` directly instead.
reload:
	$(NGINX) -t
	$(NGINX) -s reload

logs:
	docker compose logs -f --tail=20 gateway

# Every log line for one request id, gateway and services: make trace ID=<id>
# The shell reads ID from the environment (make itself still expands `$` in
# it), and grep matches it as a fixed string. "No line found" (grep exit 1) is
# not an error; failing to read the logs at all is.
export ID
trace:
	@test -n "$$ID" || { echo "usage: make trace ID=<request id>" >&2; exit 2; }
	@logs=$$(docker compose logs --no-color) || exit $$?; \
		printf '%s\n' "$$logs" | grep -F -- "$$ID"; rc=$$?; [ $$rc -le 1 ]

# stub_status listens on loopback inside the gateway only (status.conf).
status:
	docker compose exec -T gateway wget -qO- -T 2 http://127.0.0.1:8086/nginx_status

test:
	cd services && go test -count=1 -tags smoke ./smoke/

# Also stops the gateway and runs down/up; restores the stack when done.
# Takes ~10 min, past go test's default 10m timeout.
test-lifecycle:
	cd services && go test -count=1 -timeout 30m -tags smoke,lifecycle ./smoke/

# k6 from the compose network (exempt from the rate limits): a warm-up, then
# RUNS runs per scenario; prints `<scenario> <median_rps> <p95_ms> <failed>`.
# SCENARIOS, RUNS, DURATION, VUS override the defaults (scripts/bench.sh).
bench:
	@./scripts/bench.sh

# Interleaved baseline vs candidate runs: CANDIDATE=<loadtest/candidates/*.env>.
bench-compare:
	@./scripts/bench-compare.sh

# nginx and Envoy under the same load, interleaved (scripts/bench-gateways.sh).
bench-gateways:
	@./scripts/bench-gateways.sh

# Drained rolling redeploy of every replica of SERVICE (scripts/deploy.sh).
deploy:
	@./scripts/deploy.sh
