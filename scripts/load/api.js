// Read-heavy API load: the requests the dashboard makes, spread over many
// users at a fixed arrival rate (requests per second, independent of
// latency, so slow responses show up as latency rather than lower load).
//
//   k6 run -e RATE=300 -e USERS=60 scripts/load/api.js      (make perf)
//
// Every simulated client gets its own IP through X-Forwarded-For, so the
// API must trust the load generator as a proxy (TRUSTED_PROXY_CIDRS), the
// same way it trusts an ingress controller. Per-user and per-IP rate limits
// stay ON: each user stays below 10 req/s (the 600/min API limit).
import http from 'k6/http';
import exec from 'k6/execution';
import { check, fail } from 'k6';

const BASE = __ENV.BASE_URL || 'http://localhost:8080';
const API = `${BASE}/api/v1`;
const RATE = Number(__ENV.RATE || 300);
const USERS = Number(__ENV.USERS || 60);
const RUN = Date.now().toString(36);
// Optional: users prepared by scripts/load/volume.sh (a large organization).
const PRESET = __ENV.USERS_FILE ? JSON.parse(open(__ENV.USERS_FILE)) : null;

export const options = {
  setupTimeout: '5m',
  scenarios: {
    reads: {
      executor: 'ramping-arrival-rate',
      startRate: Math.min(20, RATE),
      timeUnit: '1s',
      preAllocatedVUs: 50,
      maxVUs: 300,
      stages: [
        { target: RATE, duration: '20s' }, // ramp up
        { target: RATE, duration: '60s' }, // steady state: the numbers that count
      ],
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.01'],
    'http_req_duration{kind:read}': ['p(95)<100', 'p(99)<250'],
    checks: ['rate>0.99'],
  },
  summaryTrendStats: ['avg', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
};

const ipFor = (i) => `10.77.${(i >> 8) & 255}.${i & 255}`;

function post(path, body, token, ip, name) {
  const r = http.post(`${API}${path}`, JSON.stringify(body), {
    headers: { 'Content-Type': 'application/json', 'X-Forwarded-For': ip, ...(token ? { Authorization: `Bearer ${token}` } : {}) },
    tags: { kind: 'setup', name },
  });
  if (r.status >= 300) fail(`${name}: HTTP ${r.status} ${r.body}`);
  return r.json();
}

export function setup() {
  if (PRESET) return PRESET;
  const users = [];
  for (let i = 0; i < USERS; i++) {
    const ip = ipFor(i + 1);
    const reg = post('/auth/register', {
      email: `load-${RUN}-${i}@example.com`, name: `Load ${i}`,
      password: `load-test-password-${RUN}`, organization_name: `Load ${RUN} ${i}`,
    }, null, ip, 'register');
    const token = reg.access_token;
    const me = http.get(`${API}/auth/me`, { headers: { Authorization: `Bearer ${token}`, 'X-Forwarded-For': ip } }).json();
    const org = me.memberships[0].organization_id;
    const project = post('/projects', {
      organization_id: org, name: `load-${RUN}-${i}`, repository_url: 'https://github.com/example/load',
    }, token, ip, 'create_project');
    post(`/projects/${project.id}/environments`, { name: 'staging' }, token, ip, 'create_environment');
    users.push({ token, ip, org, project: project.id });
  }
  return { users };
}

// The dashboard's request mix (weights roughly follow page views).
const MIX = [
  [30, (u) => [`/dashboard`, 'dashboard']],
  [25, (u) => [`/projects?organization_id=${u.org}&limit=20`, 'list_projects']],
  [20, (u) => [`/projects/${u.project}`, 'get_project']],
  [15, (u) => [`/deployments?project_id=${u.project}&limit=20`, 'list_deployments']],
  [10, (u) => [`/projects/${u.project}/environments`, 'list_environments']],
];
const TOTAL = MIX.reduce((s, [w]) => s + w, 0);

export default function (data) {
  const n = exec.scenario.iterationInTest;
  const u = data.users[n % data.users.length];
  let pick = (n * 7919) % TOTAL; // deterministic spread over the mix
  let route;
  for (const [w, f] of MIX) {
    if (pick < w) { route = f(u); break; }
    pick -= w;
  }
  const [path, name] = route;
  const r = http.get(`${API}${path}`, {
    headers: { Authorization: `Bearer ${u.token}`, 'X-Forwarded-For': u.ip },
    tags: { kind: 'read', name },
  });
  check(r, { 'status 200': (res) => res.status === 200 });
}
