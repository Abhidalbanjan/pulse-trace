# ── Build stage ──────────────────────────────────────────────────────────────
FROM golang:1.25-alpine3.21 AS builder

WORKDIR /app

# Copy shared module so the replace directive resolves correctly.
COPY shared/ ../shared/

# Copy the entire gateway-service module (including vendor/ folder).
COPY gateway-service/ .

RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -o /bin/gateway-service ./cmd

# ── Runtime stage ─────────────────────────────────────────────────────────────
FROM alpine:3.19

RUN apk --no-cache add ca-certificates tzdata

COPY --from=builder /bin/gateway-service /bin/gateway-service

EXPOSE 8080

ENTRYPOINT ["/bin/gateway-service"]
