// Pulse ↔ k6 bridge.
//
// Drop this file into your k6 test directory and import the helpers below
// to record your k6 runs in Pulse. Each run appears as a labelled band on the
// dashboard's timeline charts, and the Test Runs page compares production
// traffic before, during and after it (GET /pulse/api/test-runs/:id/compare).
//
// Usage in your k6 script:
//
//     import http from 'k6/http';
//     import { pulseStartRun, pulseEndRun, pulseAuth } from './pulse-k6-bridge.js';
//
//     export const options = { vus: 50, duration: '2m' };
//
//     export function setup() {
//         // Log in once and pass the token through k6 stages.
//         const token = pulseAuth(__ENV.PULSE_URL, __ENV.PULSE_USER, __ENV.PULSE_PASS);
//         const run = pulseStartRun(__ENV.PULSE_URL, token, {
//             name: 'average-load',
//             type: 'k6.average-load',
//             metadata: { vus_peak: 50 },
//         });
//         return { token, run };
//     }
//
//     export default function (data) {
//         // ... your test scenarios ...
//         http.get(`${__ENV.TARGET_URL}/api/users`);
//     }
//
//     export function teardown(data) {
//         pulseEndRun(__ENV.PULSE_URL, data.token, data.run, {
//             thresholds_passed: true,
//             p95_ms: 311,
//         });
//     }

import http from 'k6/http';
import { check } from 'k6';

// Pulse requests use their own cookie jar, and this is not a nicety.
//
// k6 keeps one cookie jar per VU. A script that logs into the application
// under test before registering its run — which most do — sends that
// session cookie to Pulse as well, and a double-submit CSRF middleware
// correctly refuses a cookie-bearing POST with no X-CSRF-Token. The login
// then fails with 403 while the same request in isolation works, which is
// a miserable thing to debug. The dashboard is bearer-token auth and wants
// no cookies at all, so give it a jar of its own.
const jar = new http.CookieJar();

function pulseParams(token) {
  const headers = { 'Content-Type': 'application/json' };
  if (token) headers.Authorization = `Bearer ${token}`;
  return { headers, jar };
}

// pulseAuth performs the JWT login round-trip and returns the bearer token,
// or null when the dashboard cannot be reached or refuses the credentials.
//
// It returns rather than throws on purpose: a load test that also runs in
// CI, where Pulse may be off entirely, should not fail because its
// observability sidecar is missing. Every function below treats a null
// token as "skip the overlay".
export function pulseAuth(pulseUrl, username, password) {
  if (!pulseUrl) {
    console.warn('pulseAuth: PULSE_URL is not set; the run will not be recorded');
    return null;
  }
  const res = http.post(
    `${pulseUrl}/pulse/api/auth/login`,
    JSON.stringify({ username, password }),
    pulseParams(),
  );
  check(res, { 'pulse login 200': (r) => r.status === 200 });
  if (res.status !== 200) {
    console.warn(`pulseAuth: login failed (${res.status}): ${res.body}`);
    return null;
  }
  return JSON.parse(res.body).token;
}

// pulseStartRun posts an in-flight test-run record to Pulse and returns the
// server-assigned object (including ID) so a later teardown can patch it
// with the ended_at timestamp.
//
// opts:
//   name      - human-readable test name (required)
//   type      - e.g. 'k6.spike', 'k6.soak'. Default 'k6.custom'.
//   metadata  - arbitrary JSON object shown in the dashboard.
export function pulseStartRun(pulseUrl, token, opts) {
  if (!token) return null; // no session: the test runs, just without the overlay
  const body = {
    name: opts.name,
    type: opts.type || 'k6.custom',
    started_at: new Date().toISOString(),
    metadata: opts.metadata || {},
  };
  const res = http.post(`${pulseUrl}/pulse/api/test-runs`, JSON.stringify(body),
    pulseParams(token));
  check(res, { 'pulse test-run created': (r) => r.status === 201 });
  if (res.status !== 201) {
    // Don't fail the whole test for an observability hiccup — just warn.
    console.warn(`pulseStartRun: server returned ${res.status}: ${res.body}`);
    return body;
  }
  const run = JSON.parse(res.body);
  // Pulse profiles the run when profiling is enabled; say so, because the
  // flame graph it produces is the reason to register the start separately
  // from the end.
  console.log(`pulse: recording run ${run.id}${run.profiling ? ' with a CPU profile' : ''}`);
  return run;
}

// pulseEndRun posts a completed test-run record with ended_at and any
// additional metadata collected during the run (peak VUs, thresholds, …).
export function pulseEndRun(pulseUrl, token, run, finalMetadata) {
  if (!token || !run) return; // nothing was registered, so there is nothing to close
  const body = {
    id: run.id,
    name: run.name,
    type: run.type,
    started_at: run.started_at,
    ended_at: new Date().toISOString(),
    metadata: Object.assign({}, run.metadata || {}, finalMetadata || {}),
  };
  const res = http.post(`${pulseUrl}/pulse/api/test-runs`, JSON.stringify(body),
    pulseParams(token));
  check(res, { 'pulse test-run finalized': (r) => r.status === 201 });
  if (res.status !== 201) {
    console.warn(`pulseEndRun: server returned ${res.status}: ${res.body}`);
    return;
  }
  console.log(`pulse: run ${run.id} recorded`);
  console.log(`  comparison   ${pulseUrl}/pulse/ui/test-runs`);
  if (JSON.parse(res.body).profiling) {
    console.log(`  flame graph  ${pulseUrl}/pulse/api/test-runs/${run.id}/profile?format=svg`);
  }
}
