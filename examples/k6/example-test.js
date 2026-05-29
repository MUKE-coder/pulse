// Example k6 test that overlays its run on Pulse's timeline.
//
// Run with:
//   PULSE_URL=http://localhost:8080 \
//   PULSE_USER=admin PULSE_PASS=changeme \
//   TARGET_URL=http://localhost:8080 \
//   k6 run example-test.js

import http from 'k6/http';
import { sleep } from 'k6';
import {
  pulseAuth,
  pulseStartRun,
  pulseEndRun,
} from './pulse-k6-bridge.js';

export const options = {
  stages: [
    { duration: '15s', target: 20 },
    { duration: '30s', target: 50 },
    { duration: '15s', target: 0 },
  ],
  thresholds: {
    http_req_duration: ['p(95)<500'],
  },
};

export function setup() {
  const token = pulseAuth(__ENV.PULSE_URL, __ENV.PULSE_USER, __ENV.PULSE_PASS);
  const run = pulseStartRun(__ENV.PULSE_URL, token, {
    name: 'average-load demo',
    type: 'k6.average-load',
    metadata: { stages: options.stages, target_p95_ms: 500 },
  });
  return { token, run };
}

export default function () {
  const r = http.get(`${__ENV.TARGET_URL}/api/users`);
  sleep(1);
}

export function teardown(data) {
  pulseEndRun(__ENV.PULSE_URL, data.token, data.run, {
    thresholds_passed: true,
  });
}
