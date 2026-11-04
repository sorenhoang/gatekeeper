// Orders under steady load (README "Performance & zero-downtime"): a GET
// and a POST per iteration, with an admin token. The zero-downtime tests run
// it through reloads and rolling deploys; a POST is never retried by the
// gateway (Phase 3), so any replica stopped mid-request shows up here.
import http from 'k6/http';
import { check } from 'k6';

export const options = {
  insecureSkipTLSVerify: true,
  vus: parseInt(__ENV.VUS || '30'),
  duration: __ENV.DURATION || '30s',
};

const base = __ENV.BASE || 'https://gateway';
const users = JSON.parse(open('/secrets/users.json'));

export function setup() {
  const r = http.post(`${base}/api/auth/login`,
    JSON.stringify({ username: 'admin', password: users.admin.password }),
    { headers: { 'Content-Type': 'application/json' } });
  if (r.status !== 200) throw new Error(`login failed: ${r.status} ${r.body}`);
  return { token: r.json('token') };
}

export default function (data) {
  const h = { headers: { Authorization: `Bearer ${data.token}`, 'Content-Type': 'application/json' } };
  const get = http.get(`${base}/api/orders/`, h);
  const post = http.post(`${base}/api/orders/`, '{"item":"bench"}', h);
  check(get, {
    'GET 2xx': (x) => x.status >= 200 && x.status < 300,
    // The Phase 3 backup answers degraded GETs when no replica is left.
    'GET not degraded': (x) => !x.headers['X-Degraded'],
  });
  check(post, { 'POST 2xx': (x) => x.status >= 200 && x.status < 300 });
}
