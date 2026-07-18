# ── Build stage ──────────────────────────────────────────────────────────────
FROM golang:1.25-alpine3.21 AS builder

WORKDIR /app

# Copy shared module so the replace directive resolves correctly.
COPY shared/ ../shared/

# Copy the entire gateway-service module.
COPY gateway-service/ .

# Previously built with -mod=vendor against a gitignored, uncommitted
# gateway-service/vendor/ directory — that made this the only service whose
# Docker build couldn't reproduce from a fresh git checkout (CI included).
# Every other service resolves modules normally; do the same here.
RUN apk add --no-cache git
RUN go mod download

RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/gateway-service ./cmd

# ── Runtime stage ─────────────────────────────────────────────────────────────
FROM alpine:3.19

RUN apk --no-cache add ca-certificates tzdata

COPY --from=builder /bin/gateway-service /bin/gateway-service

EXPOSE 8080

ENTRYPOINT ["/bin/gateway-service"]
