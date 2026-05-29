// Pulse ↔ k6 bridge.
//
// Drop this file into your k6 test directory and import the helpers below
// to overlay your k6 runs on Pulse's production timeline. The dashboard
// then renders each run as a vertical band on the latency / RPS / error
// charts, labelled with the test name.
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

// pulseAuth performs the JWT login round-trip and returns the bearer token.
// Throws if the credentials are wrong — fail-fast so the test does not
// silently run without overlay.
export function pulseAuth(pulseUrl, username, password) {
  if (!pulseUrl) throw new Error('pulseAuth: PULSE_URL is required');
  const res = http.post(
    `${pulseUrl}/pulse/api/auth/login`,
    JSON.stringify({ username, password }),
    { headers: { 'Content-Type': 'application/json' } },
  );
  check(res, { 'pulse login 200': (r) => r.status === 200 });
  if (res.status !== 200) {
    throw new Error(`pulseAuth: login failed (${res.status}): ${res.body}`);
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
  const body = {
    name: opts.name,
    type: opts.type || 'k6.custom',
    started_at: new Date().toISOString(),
    metadata: opts.metadata || {},
  };
  const res = http.post(`${pulseUrl}/pulse/api/test-runs`, JSON.stringify(body), {
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${token}`,
    },
  });
  check(res, { 'pulse test-run created': (r) => r.status === 201 });
  if (res.status !== 201) {
    // Don't fail the whole test for an observability hiccup — just warn.
    console.warn(`pulseStartRun: server returned ${res.status}: ${res.body}`);
    return body;
  }
  return JSON.parse(res.body);
}

// pulseEndRun posts a completed test-run record with ended_at and any
// additional metadata collected during the run (peak VUs, thresholds, …).
export function pulseEndRun(pulseUrl, token, run, finalMetadata) {
  const body = {
    id: run.id,
    name: run.name,
    type: run.type,
    started_at: run.started_at,
    ended_at: new Date().toISOString(),
    metadata: Object.assign({}, run.metadata || {}, finalMetadata || {}),
  };
  const res = http.post(`${pulseUrl}/pulse/api/test-runs`, JSON.stringify(body), {
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${token}`,
    },
  });
  check(res, { 'pulse test-run finalized': (r) => r.status === 201 });
  if (res.status !== 201) {
    console.warn(`pulseEndRun: server returned ${res.status}: ${res.body}`);
  }
}
