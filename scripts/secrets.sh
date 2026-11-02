#!/bin/sh
# Lab secrets (README "Auth & CORS"). Never committed: secrets/ is gitignored.
#   auth/jwt.key            32 random bytes; auth-service signs tokens with it
#   auth/users.json         lab users alice (user) and admin (admin), random passwords
#   gateway/admin.htpasswd  the admin host's Basic login (apr1, the scheme nginx
#                           checks itself; bcrypt isn't available to it on musl)
# Each half is mounted only into its own container, so the gateway can never
# sign a token. Non-empty files are kept; delete one to regenerate it. An empty
# one (a failed earlier run) is regenerated, never trusted.
set -eu

dir="${SECRETS_DIR:-secrets}"
mkdir -p "$dir/auth" "$dir/gateway"
umask 077

die() {
	echo "secrets: $*" >&2
	exit 1
}

# No pipefail in sh: check the result instead of trusting the pipeline.
pw() {
	p=$(openssl rand -base64 18 | tr -d '/+=')
	[ "${#p}" -ge 16 ] || die "could not generate a password"
	echo "$p"
}

if [ ! -s "$dir/auth/jwt.key" ]; then
	openssl rand -out "$dir/auth/jwt.key" 32
	[ "$(wc -c <"$dir/auth/jwt.key" | tr -d ' ')" = 32 ] || die "could not generate jwt.key"
	echo "secrets: created $dir/auth/jwt.key"
fi

if [ ! -s "$dir/auth/users.json" ]; then
	alice=$(pw)
	admin=$(pw)
	printf '{"alice":{"password":"%s","role":"user"},"admin":{"password":"%s","role":"admin"}}\n' \
		"$alice" "$admin" >"$dir/auth/users.json"
	echo "secrets: created $dir/auth/users.json (lab users alice, admin)"
fi

if [ ! -s "$dir/gateway/admin.htpasswd" ]; then
	# The admin host's password is the admin user's, so one secret to look up.
	admin=$(sed -n 's/.*"admin":{"password":"\([^"]*\)".*/\1/p' "$dir/auth/users.json")
	[ -n "$admin" ] || die "no admin password in $dir/auth/users.json (delete it to regenerate)"
	# -stdin: the password never appears in the process list.
	hash=$(printf '%s\n' "$admin" | openssl passwd -apr1 -stdin)
	case "$hash" in '$apr1$'*) ;; *) die "openssl passwd -apr1 failed" ;; esac
	printf 'admin:%s\n' "$hash" >"$dir/gateway/admin.htpasswd"
	echo "secrets: created $dir/gateway/admin.htpasswd"
fi

# The containers read these as their own users (nginx workers, distroless
# nonroot); the bind mount keeps host permissions, so they must be readable.
# ponytail: world-readable on the host, fine for a one-person lab machine.
chmod 755 "$dir" "$dir/auth" "$dir/gateway"
chmod 644 "$dir/auth/jwt.key" "$dir/auth/users.json" "$dir/gateway/admin.htpasswd"
