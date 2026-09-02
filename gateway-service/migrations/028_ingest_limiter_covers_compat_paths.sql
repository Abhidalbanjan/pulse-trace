-- The Datadog and Splunk ingestion paths belong to the ingestion limiter.
--
-- `telemetry-ingest` listed only the native and OTLP paths, so /api/v2/logs
-- (Datadog) and /services/collector (Splunk HEC) fell through to the `default`
-- rule at 600/60s — 10 req/s, against 100 req/s for /api/v1/logs. Same traffic,
-- same cost, an order of magnitude apart, decided by which vendor's client the
-- customer happens to be migrating off.
--
-- These two endpoints exist precisely so a Datadog or Splunk user can repoint an
-- existing agent at us without changing it. An agent doing that is not sending
-- 10 requests a second; it is sending whatever it was already sending, which is
-- the volume the native limit was sized for. The compatibility path was
-- therefore unusable for the migration it exists to enable.
--
-- Found by the weekly scale-baseline job, which has failed every run since it
-- was introduced: it drives all four protocols at 30 req/s each, and the two
-- vendor paths were rejected at roughly two thirds. The job was reporting a real
-- defect and nobody was reading it.
UPDATE rate_limit_rules
SET path_prefixes = '["/v1/traces", "/v1/metrics", "/v1/logs", "/api/v1/logs", "/api/v2/logs", "/services/collector"]'::jsonb
WHERE name = 'telemetry-ingest';
