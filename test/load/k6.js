// k6 load test for the distributed rate limiter.
//
// Usage:
//   docker compose -f deployments/docker-compose.yml up -d
//   k6 run --env BASE_URL=http://localhost:8080 test/load/k6.js
//
// Scenarios:
//   - steady: 5k req/s for 60s, distinct keys (every request admitted)
//   - burst:  20k req/s for 10s, single key (most requests denied)
// Thresholds enforce p99 < 25ms and zero HTTP errors.

import http from "k6/http";
import { check } from "k6";
import { Counter, Trend } from "k6/metrics";

const BASE_URL = __ENV.BASE_URL || "http://localhost:8080";

const allowed = new Counter("rl_allowed");
const denied  = new Counter("rl_denied");
const decisionLatency = new Trend("rl_decision_ms");

export const options = {
  thresholds: {
    http_req_failed: ["rate<0.001"],
    http_req_duration: ["p(99)<25"],
  },
  scenarios: {
    steady_distinct: {
      executor: "constant-arrival-rate",
      rate: 5000,
      timeUnit: "1s",
      duration: "60s",
      preAllocatedVUs: 200,
      maxVUs: 800,
      exec: "steady",
    },
    burst_single_key: {
      executor: "constant-arrival-rate",
      rate: 20000,
      timeUnit: "1s",
      duration: "10s",
      startTime: "65s",
      preAllocatedVUs: 400,
      maxVUs: 1500,
      exec: "burst",
    },
  },
};

function callAllow(key, rule) {
  const t0 = Date.now();
  const res = http.post(`${BASE_URL}/v1/allow`,
    JSON.stringify({ key, rule }),
    { headers: { "Content-Type": "application/json" } });
  decisionLatency.add(Date.now() - t0);

  check(res, { "status 200": (r) => r.status === 200 });
  if (res.status !== 200) return;

  const body = res.json();
  if (body && body.decision && body.decision.allowed) {
    allowed.add(1);
  } else {
    denied.add(1);
  }
}

export function steady() {
  callAllow(`user:${Math.floor(Math.random() * 1e9)}`, "api");
}

export function burst() {
  // Single key — designed to hit the limit and exercise the deny path.
  callAllow("hot-key", "expensive_read");
}
