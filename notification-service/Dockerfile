# ── Build stage ──────────────────────────────────────────────────────────────
FROM golang:1.22-alpine AS builder

WORKDIR /app

COPY shared/ ../shared/
COPY notification-service/ .

RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/notification-service ./cmd

# ── Runtime stage ─────────────────────────────────────────────────────────────
FROM alpine:3.19

RUN apk --no-cache add ca-certificates tzdata

COPY --from=builder /bin/notification-service /bin/notification-service

ENTRYPOINT ["/bin/notification-service"]
