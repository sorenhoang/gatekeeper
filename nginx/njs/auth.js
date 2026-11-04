// The auth check in njs (README "njs"): the same answers as auth-service
// /verify (services/internal/auth), computed in the gateway without a
// subrequest to another service. Selected by switching auth-check.conf's
// auth_request to /_auth_njs (loadtest/candidates/njs-auth.env).
import crypto from 'crypto';
import fs from 'fs';

// Mounted read-only from secrets/auth/jwt.key (docker-compose.yml). Read once
// at config load: each request runs in a fresh clone of the njs VM, so a
// cache filled during a request would be gone by the next one. A missing key
// leaves it undefined and every check fails closed.
const KEY_FILE = '/etc/nginx/auth-key/jwt.key';
let key;
try {
  key = fs.readFileSync(KEY_FILE);
} catch (e) {
  key = undefined;
}

// The header is fixed, so a token's own choice of algorithm ("alg": "none"
// and friends) is never trusted.
const HS256_HEADER = Buffer.from('{"alg":"HS256","typ":"JWT"}').toString('base64url');

// Constant-time, so a forged signature learns nothing from timing.
function equal(a, b) {
  if (a.length !== b.length) {
    return false;
  }
  let d = 0;
  for (let i = 0; i < a.length; i++) {
    d |= a.charCodeAt(i) ^ b.charCodeAt(i);
  }
  return d === 0;
}

function claims(token) {
  const p = token.split('.');
  if (p.length !== 3 || p[0] !== HS256_HEADER) {
    return null;
  }
  const mac = crypto.createHmac('sha256', key).update(p[0] + '.' + p[1]).digest('base64url');
  if (!equal(p[2], mac)) {
    return null;
  }
  const c = JSON.parse(Buffer.from(p[1], 'base64url').toString());
  // The types token.go's json.Unmarshal enforces: a whole-number exp and
  // string sub/role. Truthiness alone would take "exp":"9999999999" or
  // "role":["admin"] (which then reads as admin).
  if (c === null || typeof c !== 'object' || !Number.isInteger(c.exp) ||
      typeof c.sub !== 'string' || typeof c.role !== 'string' ||
      c.exp === 0 || c.sub === '' || c.role === '' || Date.now() / 1000 >= c.exp) {
    return null;
  }
  return c;
}

function verify(r) {
  // 32 bytes: auth-service's CheckKey minimum, below which tokens are guessable.
  if (key === undefined || key.length < 32) {
    // auth.conf turns the 500 into a 503: fail closed.
    r.error(`njs auth: no usable signing key at ${KEY_FILE}`);
    return r.return(500);
  }
  // $auth_header: the Authorization header, or ?access_token= on the
  // realtime routes (nginx.conf).
  const m = /^Bearer (.+)$/.exec(r.variables.auth_header || '');
  let c = null;
  try {
    c = m && claims(m[1]);
  } catch (e) {
    // A malformed payload is a bad token, never a pass.
    c = null;
  }
  if (!c) {
    return r.return(401);
  }
  // Only admins may delete (auth-service's role rule).
  if (r.variables.request_method === 'DELETE' && c.role !== 'admin') {
    return r.return(403);
  }
  r.headersOut['X-User-ID'] = c.sub;
  r.headersOut['X-User-Role'] = c.role;
  r.return(204);
}

export default { verify };
