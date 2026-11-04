// Static files straight from nginx (the SPA shell and an asset): what
// open_file_cache can help with. No auth, no upstream.
import http from 'k6/http';
import { check } from 'k6';

export const options = {
  insecureSkipTLSVerify: true,
  vus: parseInt(__ENV.VUS || '50'),
  duration: __ENV.DURATION || '15s',
};

const base = __ENV.BASE || 'https://gateway';

export default function () {
  for (const r of [http.get(`${base}/`), http.get(`${base}/assets/app.css`)]) {
    check(r, { '2xx': (x) => x.status >= 200 && x.status < 300 });
  }
}
