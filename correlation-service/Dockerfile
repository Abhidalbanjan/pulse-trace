# ── Build stage ──────────────────────────────────────────────────────────────
FROM golang:1.22-alpine AS builder

WORKDIR /app

COPY shared/ ../shared/
COPY correlation-service/ .

RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/correlation-service ./cmd

# ── Runtime stage ─────────────────────────────────────────────────────────────
FROM alpine:3.19

RUN apk --no-cache add ca-certificates tzdata

COPY --from=builder /bin/correlation-service /bin/correlation-service

EXPOSE 8083

ENTRYPOINT ["/bin/correlation-service"]
