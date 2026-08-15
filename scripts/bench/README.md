# Comparative benchmark harness (P0.1)

Loads an identical, reproducible corpus into **PulseTrace** and **OpenObserve**
so [`COMPETITIVE_OPENOBSERVE.md`](../../docs/COMPETITIVE_OPENOBSERVE.md) can cite
measured numbers instead of their marketing and our guesses.

Every performance and cost claim in that document is currently **unverified**.
This harness is what makes them falsifiable — and P0 exists precisely so we find
out whether the storage-economics gap (six weeks of budget in the plan) is real
before spending anything on it.

## The rules this harness enforces

**Same bytes, both sides.** The corpus is byte-reproducible from a seed and its
SHA-256 is verified at load time (`EXPECT_SHA256`). A benchmark whose input
drifts cannot tell a slower engine from a heavier dataset.

**Native path, both sides.** PulseTrace gets `/api/v1/logs`; OpenObserve gets
`_json`. Forcing either to accept the other's wire format would measure a
translation layer and hand the win to whichever product owns the format.

**Equal resources, both sides.** `BENCH_CPUS` / `BENCH_MEMORY` cap the
OpenObserve container and must match whatever the PulseTrace side runs under.
An uncapped single binary against a capped 23-container stack is not a result.

**Pinned versions.** A moving `latest` silently re-baselines every comparison.

## Usage

```bash
# 1. Generate the corpus (10 GiB default; --size-mb for a quick pass)
go run ./scripts/bench/corpus --size-mb 10240 --seed 42 --out corpus.ndjson
#    prints: corpus: N bytes … sha256=<hash>

# 2. Bring up the OpenObserve side
docker compose -f scripts/bench/compose.openobserve.yml up -d

# 3. Load each target with the same corpus
EXPECT_SHA256=<hash> ./scripts/bench/load.sh --target pulsetrace  --corpus corpus.ndjson
EXPECT_SHA256=<hash> ./scripts/bench/load.sh --target openobserve --corpus corpus.ndjson

# 4. Tear down
docker compose -f scripts/bench/compose.openobserve.yml down -v
```

## Corpus shape

70% logs / 20% traces / 10% metrics by bytes — logs dominate because they
dominate real observability spend, which is what the cost comparison is about.

Cardinality is a stated parameter, not an accident, because it decides both
storage size and group-by cost: `service`×40, `pod`×2000, **`customer_id`×50k**,
`host`×200. The high-cardinality customer dimension is deliberate — it is the
case that separates a columnar store from an inverted index, and the one a
vendor benchmark usually omits.

Messages come from templates with varying parameters rather than random strings:
compressibility decides bytes-on-disk, and random data compresses nothing like
real logs do.

## Status

Delivered: corpus generator (reproducibility enforced by `gen_test.go`), pinned
OpenObserve profile, loaders for both targets. Verified end to end — the same
corpus loads into both with zero failures.

**Not yet delivered (P0.2):** the six query classes and the machine-written
`BENCHMARK.md`. Until those land, do not quote throughput from `load.sh`: at
small corpus sizes it is dominated by integer-second timing resolution and
process startup, and it measures the loader as much as the engine.
