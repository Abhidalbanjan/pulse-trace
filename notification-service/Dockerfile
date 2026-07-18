# ── Build stage ──────────────────────────────────────────────────────────────
FROM golang:1.25-alpine3.21 AS builder

WORKDIR /app

COPY shared/ ../shared/
COPY notification-service/ .

RUN apk add --no-cache git
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/notification-service ./cmd

# ── Runtime stage ─────────────────────────────────────────────────────────────
FROM alpine:3.19

RUN apk --no-cache add ca-certificates tzdata

COPY --from=builder /bin/notification-service /bin/notification-service

ENTRYPOINT ["/bin/notification-service"]
