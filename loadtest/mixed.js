// Mixed read load (README "Performance & zero-downtime"): an authenticated
// GET on users and orders, and a cached products GET, per iteration. Run by
// `make bench` from the compose network, which Phase 5's limits exempt, so
// this measures the gateway rather than the rate limiter.
import http from 'k6/http';
import { check } from 'k6';

export const options = {
  // The lab certificate names localhost, not the compose name `gateway`.
  insecureSkipTLSVerify: true,
  vus: parseInt(__ENV.VUS || '50'),
  duration: __ENV.DURATION || '15s',
};

const base = __ENV.BASE || 'https://gateway';
// Login always goes through the nginx gateway: Envoy has no /api/auth/ route.
const authBase = __ENV.AUTH_BASE || 'https://gateway';
// From `make secrets`, mounted read-only by scripts/bench.sh.
const users = JSON.parse(open('/secrets/users.json'));

export function setup() {
  const r = http.post(`${authBase}/api/auth/login`,
    JSON.stringify({ username: 'alice', password: users.alice.password }),
    { headers: { 'Content-Type': 'application/json' } });
  if (r.status !== 200) throw new Error(`login failed: ${r.status} ${r.body}`);
  return { token: r.json('token') };
}

export default function (data) {
  const auth = { headers: { Authorization: `Bearer ${data.token}` } };
  for (const r of [
    http.get(`${base}/api/users/`, auth),
    http.get(`${base}/api/orders/`, auth),
    http.get(`${base}/api/products/bench`),
  ]) {
    check(r, { '2xx': (x) => x.status >= 200 && x.status < 300 });
  }
}
