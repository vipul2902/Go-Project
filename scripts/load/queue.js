// Deployment pipeline throughput: each VU owns a project and keeps one
// deployment in flight (create -> wait for RUNNING -> repeat), so this
// measures how fast the PostgreSQL queue and the worker turn requests into
// finished deployments. Run it with the worker in dry-run mode
// (WORKER_BUILDER=dryrun WORKER_DEPLOYER=dryrun) to measure the platform,
// not docker builds or Kubernetes rollouts.
//
//   k6 run -e VUS=20 scripts/load/queue.js      (make perf)
import http from 'k6/http';
import { check, fail, sleep } from 'k6';
import { Trend, Counter } from 'k6/metrics';

const BASE = __ENV.BASE_URL || 'http://localhost:8080';
const API = `${BASE}/api/v1`;
const VUS = Number(__ENV.VUS || 20);
const RUN = Date.now().toString(36);

// Measured by the client (includes the 250 ms poll interval) and by the
// server's own timestamps (created_at -> completed_at): the real pipeline time.
const queueToRunning = new Trend('deployment_queue_to_running', true);
const serverLatency = new Trend('deployment_server_latency', true);
const completed = new Counter('deployments_completed');

export const options = {
  setupTimeout: '5m',
  scenarios: {
    pipeline: { executor: 'constant-vus', vus: VUS, duration: '60s' },
  },
  thresholds: {
    deployment_queue_to_running: ['p(95)<5000'],
    deployment_server_latency: ['p(95)<1000'],
    checks: ['rate>0.99'],
  },
  summaryTrendStats: ['avg', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
};

const ipFor = (i) => `10.78.${(i >> 8) & 255}.${i & 255}`;
const hex = () => Array.from({ length: 12 }, () => '0123456789abcdef'[Math.floor(Math.random() * 16)]).join('');

function call(method, path, body, token, ip, name) {
  const r = http.request(method, `${API}${path}`, body ? JSON.stringify(body) : null, {
    headers: { 'Content-Type': 'application/json', 'X-Forwarded-For': ip, ...(token ? { Authorization: `Bearer ${token}` } : {}) },
    tags: { name },
  });
  return r;
}

export function setup() {
  const users = [];
  for (let i = 0; i < VUS; i++) {
    const ip = ipFor(i + 1);
    let r = call('POST', '/auth/register', {
      email: `queue-${RUN}-${i}@example.com`, name: `Queue ${i}`,
      password: `queue-test-password-${RUN}`, organization_name: `Queue ${RUN} ${i}`,
    }, null, ip, 'register');
    if (r.status !== 201) fail(`register: ${r.status} ${r.body}`);
    const token = r.json().access_token;
    const org = call('GET', '/auth/me', null, token, ip, 'me').json().memberships[0].organization_id;
    r = call('POST', '/projects', { organization_id: org, name: `queue-${RUN}-${i}`, repository_url: 'https://github.com/example/queue' }, token, ip, 'create_project');
    if (r.status !== 201) fail(`project: ${r.status} ${r.body}`);
    const project = r.json().id;
    r = call('POST', `/projects/${project}/environments`, { name: 'staging' }, token, ip, 'create_environment');
    if (r.status !== 201) fail(`environment: ${r.status} ${r.body}`);
    users.push({ token, ip, project });
  }
  return { users };
}

export default function (data) {
  const u = data.users[(__VU - 1) % data.users.length];
  const start = Date.now();
  const r = call('POST', '/deployments', { project_id: u.project, environment: 'staging', commit_sha: hex() }, u.token, u.ip, 'create_deployment');
  if (!check(r, { 'deployment accepted': (res) => res.status === 202 })) {
    sleep(1);
    return;
  }
  const id = r.json().id;
  // Poll at 4/s per user: well under the 10/s per-user API limit.
  for (let i = 0; i < 120; i++) {
    sleep(0.25);
    const d = call('GET', `/deployments/${id}`, null, u.token, u.ip, 'get_deployment').json();
    const status = d.status;
    if (status === 'RUNNING' || status === 'FAILED' || status === 'CANCELLED') {
      check(status, { 'deployment RUNNING': (s) => s === 'RUNNING' });
      queueToRunning.add(Date.now() - start);
      if (d.completed_at) serverLatency.add(Date.parse(d.completed_at) - Date.parse(d.created_at));
      completed.add(1);
      return;
    }
  }
  check(null, { 'deployment RUNNING': () => false });
}
