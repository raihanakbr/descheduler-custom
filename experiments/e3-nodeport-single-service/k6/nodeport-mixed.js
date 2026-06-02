import http from 'k6/http';
import { check, sleep } from 'k6';

const workerIPs = (__ENV.WORKER_IPS || '172.31.18.162,172.31.18.78,172.31.17.242').split(',');
const nodePort = __ENV.NODEPORT;

if (!nodePort) {
  throw new Error('NODEPORT is required. Example: NODEPORT=30080 k6 run k6/nodeport-mixed.js');
}

export const options = {
  scenarios: {
    mixed: {
      executor: 'ramping-arrival-rate',
      timeUnit: '1s',
      preAllocatedVUs: Number(__ENV.PREALLOCATED_VUS || 30),
      maxVUs: Number(__ENV.MAX_VUS || 60),
      stages: [
        { duration: __ENV.RAMP_UP || '30s', target: Number(__ENV.RAMP_RPS || 8) },
        { duration: __ENV.HOLD || '90s', target: Number(__ENV.TARGET_RPS || 18) },
        { duration: __ENV.RAMP_DOWN || '30s', target: 0 },
      ],
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.05'],
  },
};

export default function () {
  const r = Math.random();
  let ip;
  let path;

  if (r < 0.45) {
    ip = workerIPs[0];
    path = '/cpu/work?cpu_ms=250&mem_mb=4&hold_ms=0';
  } else if (r < 0.75) {
    ip = workerIPs[1];
    path = '/memory/work?cpu_ms=20&mem_mb=24&hold_ms=500';
  } else {
    ip = workerIPs[2];
    path = '/balanced/work?cpu_ms=100&mem_mb=16&hold_ms=200';
  }

  const res = http.get(`http://${ip}:${nodePort}${path}`, { timeout: '10s' });
  check(res, { 'status 200': (response) => response.status === 200 });
  sleep(0.1);
}
