# ── Build stage ──────────────────────────────────────────────────────────────
FROM golang:1.25-alpine3.21 AS builder

WORKDIR /app

COPY shared/ ../shared/
COPY correlation-service/ .

RUN apk add --no-cache git
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/correlation-service ./cmd

# ── Runtime stage ─────────────────────────────────────────────────────────────
FROM alpine:3.19

RUN apk --no-cache add ca-certificates tzdata

COPY --from=builder /bin/correlation-service /bin/correlation-service

EXPOSE 8083

ENTRYPOINT ["/bin/correlation-service"]
