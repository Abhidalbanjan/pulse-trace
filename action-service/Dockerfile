# ── Build stage ──────────────────────────────────────────────────────────────
FROM golang:1.26-alpine3.22 AS builder

WORKDIR /app

# Copy the entire action-service module. Build context is the repo root (see
# docker-compose.yml / .github/workflows/ci.yml) for consistency with every
# other service, even though action-service itself has no shared/ dependency.
COPY action-service/ .

RUN apk add --no-cache git
RUN go mod download

RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/action-service ./cmd

# ── Runtime stage ─────────────────────────────────────────────────────────────
FROM alpine:3.19

RUN apk --no-cache add ca-certificates tzdata

COPY --from=builder /bin/action-service /bin/action-service

EXPOSE 8085

ENTRYPOINT ["/bin/action-service"]
