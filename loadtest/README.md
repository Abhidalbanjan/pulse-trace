# Moved

The ingestion load/scale harness now lives in [`scripts/load/`](../scripts/load/)
(ROAD_TO_100 · F0.2). It replaced the earlier single-endpoint VU-ramp script here
with a sustained, multi-protocol arrival-rate test (native + OTLP + Datadog +
Splunk), downstream back-pressure sampling, and a published
[`PERF_BASELINE.md`](../PERF_BASELINE.md).

See [`scripts/load/README.md`](../scripts/load/README.md).
