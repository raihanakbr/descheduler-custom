import http from 'k6/http';
import { check } from 'k6';

const url = __ENV.FOREGROUND_URL;
if (!url) throw new Error('FOREGROUND_URL is required');

function intEnv(name, fallback) {
  const value = Number.parseInt(__ENV[name] || '', 10);
  return Number.isFinite(value) && value >= 0 ? value : fallback;
}

export const options = {
  scenarios: {
    foreground: {
      executor: 'constant-arrival-rate',
      rate: intEnv('FOREGROUND_RPS', 20),
      timeUnit: '1s',
      duration: __ENV.FOREGROUND_DURATION || '6m',
      preAllocatedVUs: intEnv('FOREGROUND_VUS', 80),
      maxVUs: intEnv('FOREGROUND_MAX_VUS', 200),
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.20'],
  },
};

export default function () {
  const response = http.get(`${url}/work?cpu_units=${intEnv('FOREGROUND_CPU_UNITS', 20)}&mem_mb=0&hold_ms=0`, {
    timeout: __ENV.REQUEST_TIMEOUT || '15s',
    tags: { stream: 'foreground' },
  });
  check(response, { 'foreground status 200': (r) => r.status === 200 });
}
