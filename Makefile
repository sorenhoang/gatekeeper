NGINX := docker compose exec -T gateway nginx -c /etc/nginx/gatekeeper/nginx.conf

.PHONY: up down reload logs test test-lifecycle

# A smoke run killed mid-test can leave a broken zz-smoke-*.conf behind, which
# would stop the gateway from starting.
up:
	rm -f nginx/conf.d/zz-smoke-*.conf
	docker compose up -d --build --wait

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
