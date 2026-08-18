# ── Build stage ──────────────────────────────────────────────────────────────
#
# Debian, not Alpine, and CGO on — both required by DuckDB (P3.1 query core).
#
# The gateway embeds DuckDB to execute user-authored SQL locally, so that user
# SQL never reaches ClickHouse, Quickwit or Postgres: the stores only ever
# receive statements we generate, with the tenant bound by the scanner. That is
# the isolation property the query core is built on, and it costs a build
# change.
#
# Alpine cannot work here. go-duckdb links a prebuilt static library that is
# built against glibc, and on musl the link fails on `malloc_trim`,
# `backtrace_symbols` and `__res_init` — glibc symbols musl does not provide.
# Building DuckDB from source against musl instead would trade a base image for
# a very long, fragile compile in every build. Verified both ways before
# choosing; the alpine failure is a link error, not a configuration mistake.
#
# Cost, measured rather than estimated, so it is not rediscovered:
#
#   alpine + CGO_ENABLED=0   (before)            68.2 MB image
#   distroless/cc + CGO=1    (this file)         98.9 MB image, 33 MB binary
#   DuckDB static link                           +38 MB  (34.4 -> 72.2 MB)
#
# The image above does NOT yet include DuckDB: nothing in cmd/ imports
# internal/sqlq, so the linker drops it. Expect ~137 MB once the SQL endpoint
# wires the engine in. Recorded here because measuring the image today and
# calling it "the cost of DuckDB" would understate it by 38 MB.
#
# Only this service is affected — every other service keeps CGO_ENABLED=0 on
# Alpine.
FROM golang:1.26.6 AS builder

WORKDIR /app

# Copy shared module so the replace directive resolves correctly.
COPY shared/ ../shared/

# Copy the entire gateway-service module.
COPY gateway-service/ .

# Previously built with -mod=vendor against a gitignored, uncommitted
# gateway-service/vendor/ directory — that made this the only service whose
# Docker build couldn't reproduce from a fresh git checkout (CI included).
# Every other service resolves modules normally; do the same here.
RUN go mod download

RUN CGO_ENABLED=1 GOOS=linux go build -o /bin/gateway-service ./cmd

# ── Runtime stage ─────────────────────────────────────────────────────────────
#
# distroless/cc, which carries glibc and libstdc++ — everything DuckDB links
# against — and nothing else. Measured: debian-slim gives a 201 MB image against
# this one's 98.9 MB. That difference is worth the loss of a shell, because
# container count and image size are dimension D1, the one we already lose to
# OpenObserve's single 2-container deployment; tripling the gateway to buy a
# query engine would be paying for D3 with D1.
FROM gcr.io/distroless/cc-debian12:nonroot

COPY --from=builder /bin/gateway-service /bin/gateway-service

EXPOSE 8080

ENTRYPOINT ["/bin/gateway-service"]
