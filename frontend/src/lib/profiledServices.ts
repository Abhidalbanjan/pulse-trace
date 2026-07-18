// Every service that actually runs the Pyroscope agent (see each service's
// cmd/main.go). Shared between the Continuous Profiler and the Service Page's
// Code Hotspots panel so both agree on which services genuinely have profiles.
export const PROFILED_SERVICES = [
  'gateway-service',
  'alert-service',
  'correlation-service',
  'log-service',
  'topology-service',
  'notification-service',
];
