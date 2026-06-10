import http from 'k6/http';
import { check } from 'k6';

const url = __ENV.HOTSPOT_URL;
const resource = (__ENV.RESOURCE || 'cpu').toLowerCase();
if (!url) throw new Error('HOTSPOT_URL is required');

function intEnv(name, fallback) {
  const value = Number.parseInt(__ENV[name] || '', 10);
  return Number.isFinite(value) && value >= 0 ? value : fallback;
}

export const options = {
  scenarios: {
    hotspot: {
      executor: 'constant-arrival-rate',
      rate: intEnv('HOTSPOT_RPS', resource === 'memory' ? 2 : 8),
      timeUnit: '1s',
      duration: __ENV.HOTSPOT_DURATION || '5m',
      preAllocatedVUs: intEnv('HOTSPOT_VUS', 60),
      maxVUs: intEnv('HOTSPOT_MAX_VUS', 160),
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.20'],
  },
};

export default function () {
  const query = resource === 'memory'
    ? `cpu_units=0&mem_mb=${intEnv('HOTSPOT_MEM_MB', 80)}&hold_ms=${intEnv('HOTSPOT_HOLD_MS', 3000)}`
    : `cpu_units=${intEnv('HOTSPOT_CPU_UNITS', 900)}&mem_mb=0&hold_ms=0`;
  const response = http.get(`${url}/work?${query}`, {
    timeout: __ENV.REQUEST_TIMEOUT || '15s',
    tags: { stream: 'hotspot', resource },
  });
  check(response, { 'hotspot status 200': (r) => r.status === 200 });
}
