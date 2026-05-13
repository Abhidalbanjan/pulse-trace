# ── Build stage ──────────────────────────────────────────────────────────────
FROM golang:1.22-alpine AS builder

WORKDIR /app

# Copy shared module so the replace directive resolves correctly.
COPY shared/ ../shared/

# Copy the entire gateway-service module (go.sum may not exist if there are
# no external dependencies, so we copy the whole directory at once).
COPY gateway-service/ .

RUN go mod download

RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/gateway-service ./cmd

# ── Runtime stage ─────────────────────────────────────────────────────────────
FROM alpine:3.19

RUN apk --no-cache add ca-certificates tzdata

COPY --from=builder /bin/gateway-service /bin/gateway-service

EXPOSE 8080

ENTRYPOINT ["/bin/gateway-service"]
