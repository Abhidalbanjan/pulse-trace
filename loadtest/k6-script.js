import http from 'k6/http';
import { check, sleep } from 'k6';

export const options = {
  stages: [
    { duration: '10s', target: 500 },  // Ramp up to 500 virtual users
    { duration: '30s', target: 1000 }, // Ramp up to 1000 virtual users (high volume)
    { duration: '30s', target: 1000 }, // Sustain 1000 virtual users
    { duration: '10s', target: 0 },    // Ramp down
  ],
};

const services = ['payment-service', 'auth-service', 'order-service', 'inventory-service'];
const levels = ['INFO', 'WARN', 'ERROR', 'DEBUG'];

export default function () {
  const payload = JSON.stringify({
    service: services[Math.floor(Math.random() * services.length)],
    level: levels[Math.floor(Math.random() * levels.length)],
    message: 'Load test generated log message',
    trace_id: 'load-test-' + __VU + '-' + __ITER,
    metadata: {
      vu: __VU.toString(),
      iter: __ITER.toString(),
      env: 'loadtest'
    }
  });

  const params = {
    headers: {
      'Content-Type': 'application/json',
    },
  };

  // Send logs to Vector (port 8383) instead of Gateway directly
  const url = 'http://localhost:8383/';

  const res = http.post(url, payload, params);
  check(res, {
    'is status 201': (r) => r.status === 201,
  });
}
