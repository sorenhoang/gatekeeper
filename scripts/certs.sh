#!/bin/sh
# Lab TLS material for the gateway (README "TLS & HTTP/2"). Never committed:
# certs/ and *.key/*.pem are in .gitignore.
#   ca.pem / ca.key          lab CA, 10 years; trust ca.pem to verify the gateway
#   gateway.pem / gateway.key leaf for localhost, admin.localhost, 127.0.0.1
# Valid files are kept; a leaf expiring within RENEW_DAYS is reissued from the
# same CA, so clients that trust ca.pem keep working.
set -eu

dir="${CERTS_DIR:-certs}"
CA_DAYS=3650
# The longest leaf validity Apple and the browsers accept.
LEAF_DAYS=398
RENEW_DAYS=30

mkdir -p "$dir"
cd "$dir"

if [ ! -f ca.pem ] || [ ! -f ca.key ]; then
	openssl req -x509 -newkey rsa:2048 -nodes -sha256 -days "$CA_DAYS" \
		-subj "/CN=gatekeeper lab CA" -keyout ca.key -out ca.pem 2>/dev/null
	rm -f gateway.pem
	echo "certs: created lab CA in $dir/ca.pem"
fi

if [ -f gateway.pem ] && [ -f gateway.key ] &&
	openssl x509 -in gateway.pem -noout -checkend $((RENEW_DAYS * 86400)) >/dev/null 2>&1; then
	exit 0
fi

[ -f gateway.key ] || openssl genrsa -out gateway.key 2048 2>/dev/null
ext=$(mktemp)
trap 'rm -f "$ext"' EXIT
printf '%s\n' 'subjectAltName=DNS:localhost,DNS:admin.localhost,IP:127.0.0.1' \
	'basicConstraints=critical,CA:FALSE' 'keyUsage=critical,digitalSignature,keyEncipherment' \
	'extendedKeyUsage=serverAuth' >"$ext"
openssl req -new -key gateway.key -subj "/CN=localhost" 2>/dev/null |
	openssl x509 -req -sha256 -days "$LEAF_DAYS" -CA ca.pem -CAkey ca.key -CAcreateserial \
		-extfile "$ext" -out gateway.pem 2>/dev/null
# The nginx master reads the key as root before dropping privileges, so the
# key stays private to its owner. ponytail: ca.key sits in the mounted folder
# too (lab only); a real CA key would never be on the gateway host.
chmod 600 gateway.key
chmod 644 gateway.pem ca.pem
echo "certs: issued $dir/gateway.pem (valid $LEAF_DAYS days)"
