# ── Build stage ──────────────────────────────────────────────────────────────
FROM golang:1.24-alpine AS builder

WORKDIR /app

# Copy shared module so the replace directive resolves correctly.
COPY shared/ ../shared/

# Copy the entire log-service module.
COPY log-service/ .

RUN go mod download

RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/log-service ./cmd

# ── Runtime stage ─────────────────────────────────────────────────────────────
FROM alpine:3.19

RUN apk --no-cache add ca-certificates tzdata

COPY --from=builder /bin/log-service /bin/log-service

EXPOSE 8081

ENTRYPOINT ["/bin/log-service"]
