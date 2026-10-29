NGINX := docker compose exec -T gateway nginx -c /etc/nginx/gatekeeper/nginx.conf

# Build attestations carry a timestamp, so every rebuild gets a new image id
# and `make up` recreates all services, reshuffling their IPs behind nginx.
export BUILDX_NO_DEFAULT_ATTESTATIONS := 1

API_ROUTES  := users orders products
# 50 x 0.2s: how long `make up` waits for each route before only warning.
READY_TRIES := 50

.PHONY: up down reload logs test test-lifecycle

# A smoke run killed mid-test can leave a broken zz-smoke-*.conf behind, which
# would stop the gateway from starting.
up:
	rm -f nginx/conf.d/zz-smoke-*.conf
	docker compose up -d --build --wait
	@# A reload re-resolves the upstreams now (measured: 0.2s vs ~3s for a
	@# service that was missing); then wait for each route. A route that stays
	@# down only warns: a stopped service is a valid state, not a failed `up`.
	$(NGINX) -s reload
	@for route in $(API_ROUTES); do \
		for i in $$(seq $(READY_TRIES)); do \
			curl -fs -o /dev/null localhost:8080/api/$$route/ && break; \
			[ $$i -eq $(READY_TRIES) ] && echo "warning: /api/$$route/ not answering yet" >&2; \
			sleep 0.2; \
		done; \
	done

down:
	docker compose down --remove-orphans

# nginx -t first: an invalid config never reaches the master. To watch the
# master reject a bad config itself, run `nginx -s reload` directly instead.
reload:
	$(NGINX) -t
	$(NGINX) -s reload

logs:
	docker compose logs -f --tail=20 gateway

test:
	cd services && go test -count=1 -tags smoke ./smoke/

# Also stops the gateway and runs down/up; restores the stack when done.
test-lifecycle:
	cd services && go test -count=1 -tags smoke,lifecycle ./smoke/
