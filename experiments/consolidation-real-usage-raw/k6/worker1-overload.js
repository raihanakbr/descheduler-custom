import http from 'k6/http';
import { check, sleep } from 'k6';

const workerIP = __ENV.WORKER_IP;
const nodePort = __ENV.NODEPORT;
const mode = (__ENV.MODE || 'cpu').toLowerCase();

if (!workerIP) {
  throw new Error('WORKER_IP is required');
}

if (!nodePort) {
  throw new Error('NODEPORT is required');
}

function intEnv(name, fallback) {
  const value = Number.parseInt(__ENV[name] || '', 10);
  return Number.isFinite(value) && value >= 0 ? value : fallback;
}

const cpuMs = intEnv('CPU_MS', 900);
const memMB = intEnv('MEM_MB', 48);
const holdMs = intEnv('HOLD_MS', 1500);
const warmupRps = intEnv('WARMUP_RPS', intEnv('RAMP_RPS', 1));
const targetRps = intEnv('TARGET_RPS', 10);

export const options = {
  scenarios: {
    worker1_overload: {
      executor: 'ramping-arrival-rate',
      timeUnit: '1s',
      preAllocatedVUs: intEnv('PREALLOCATED_VUS', 50),
      maxVUs: intEnv('MAX_VUS', 120),
      stages: [
        { duration: __ENV.WARMUP || '10s', target: warmupRps },
        { duration: __ENV.RAMP_TO_PEAK || '15s', target: targetRps },
        { duration: __ENV.HOLD || '45s', target: targetRps },
        { duration: __ENV.RAMP_DOWN || '20s', target: 0 },
      ],
    },
  },
  thresholds: {
    http_req_duration: ['p(95)<10000'],
  },
};

export default function () {
  const path = pickPath();
  const response = http.get(`http://${workerIP}:${nodePort}${path}`, {
    timeout: __ENV.TIMEOUT || '15s',
    tags: {
      mode,
      worker: workerIP,
    },
  });

  check(response, {
    'status is 200': (r) => r.status === 200,
  });

  sleep(0.1);
}

function pickPath() {
  if (mode === 'memory') {
    return `/memory/work?cpu_ms=${cpuMs}&mem_mb=${memMB}&hold_ms=${holdMs}`;
  }

  if (mode === 'mixed') {
    if (Math.random() < 0.75) {
      return `/cpu/work?cpu_ms=${cpuMs}&mem_mb=0&hold_ms=0`;
    }
    return `/memory/work?cpu_ms=20&mem_mb=${memMB}&hold_ms=${holdMs}`;
  }

  return `/cpu/work?cpu_ms=${cpuMs}&mem_mb=0&hold_ms=0`;
}
