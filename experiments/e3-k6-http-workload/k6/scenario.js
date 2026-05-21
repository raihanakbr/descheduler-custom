import http from 'k6/http';
import exec from 'k6/execution';
import { check, sleep } from 'k6';

const baseUrl = (__ENV.BASE_URL || 'http://127.0.0.1').replace(/\/$/, '');

function intEnv(name, fallback) {
  const value = Number.parseInt(__ENV[name] || '', 10);
  return Number.isFinite(value) && value > 0 ? value : fallback;
}

export const options = {
  scenarios: {
    phased_load: {
      executor: 'ramping-arrival-rate',
      timeUnit: '1s',
      preAllocatedVUs: intEnv('PRE_ALLOCATED_VUS', 80),
      maxVUs: intEnv('MAX_VUS', 200),
      stages: [
        { duration: __ENV.WARMUP_DURATION || '2m', target: intEnv('WARMUP_RPS', 5) },
        { duration: __ENV.LOW_DURATION || '3m', target: intEnv('LOW_RPS', 10) },
        { duration: __ENV.MEDIUM_DURATION || '5m', target: intEnv('MEDIUM_RPS', 25) },
        { duration: __ENV.BURST_DURATION || '3m', target: intEnv('BURST_RPS', 45) },
        { duration: __ENV.COOLDOWN_DURATION || '2m', target: intEnv('COOLDOWN_RPS', 5) },
      ],
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.05'],
  },
};

export default function () {
  const target = pickTarget(Math.random(), exec.scenario.progress);
  const response = http.get(`${baseUrl}${target.path}`, {
    tags: {
      profile: target.profile,
      phase: phaseName(exec.scenario.progress),
    },
    timeout: '10s',
  });

  check(response, {
    'status is 200': (r) => r.status === 200,
  });
  sleep(0.1);
}

function pickTarget(rand, progress) {
  if (progress < 0.35) {
    return weighted(rand, [
      ['hot', 40, '/hot/work?cpu_ms=120&mem_mb=8&hold_ms=0'],
      ['warm', 35, '/warm/work?cpu_ms=70&mem_mb=32&hold_ms=150'],
      ['mem', 20, '/mem/work?cpu_ms=20&mem_mb=96&hold_ms=800'],
      ['idle', 5, '/idle/work?cpu_ms=10&mem_mb=8&hold_ms=0'],
    ]);
  }
  if (progress < 0.80) {
    return weighted(rand, [
      ['hot', 55, '/hot/work?cpu_ms=180&mem_mb=12&hold_ms=0'],
      ['warm', 30, '/warm/work?cpu_ms=100&mem_mb=48&hold_ms=250'],
      ['mem', 14, '/mem/work?cpu_ms=30&mem_mb=128&hold_ms=1000'],
      ['idle', 1, '/idle/work?cpu_ms=10&mem_mb=8&hold_ms=0'],
    ]);
  }
  return weighted(rand, [
    ['hot', 45, '/hot/work?cpu_ms=100&mem_mb=8&hold_ms=0'],
    ['warm', 35, '/warm/work?cpu_ms=60&mem_mb=32&hold_ms=150'],
    ['mem', 15, '/mem/work?cpu_ms=20&mem_mb=64&hold_ms=500'],
    ['idle', 5, '/idle/work?cpu_ms=10&mem_mb=8&hold_ms=0'],
  ]);
}

function weighted(rand, items) {
  const total = items.reduce((sum, item) => sum + item[1], 0);
  let cursor = rand * total;
  for (const [profile, weight, path] of items) {
    cursor -= weight;
    if (cursor <= 0) {
      return { profile, path };
    }
  }
  const last = items[items.length - 1];
  return { profile: last[0], path: last[2] };
}

function phaseName(progress) {
  if (progress < 0.13) return 'warmup';
  if (progress < 0.33) return 'low';
  if (progress < 0.66) return 'medium';
  if (progress < 0.86) return 'burst';
  return 'cooldown';
}
