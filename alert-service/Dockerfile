# ── Build stage ──────────────────────────────────────────────────────────────
FROM golang:1.26.6-alpine AS builder

WORKDIR /app

# Copy shared module so the replace directive resolves correctly.
COPY shared/ ../shared/

# Copy the entire alert-service module.
COPY alert-service/ .

RUN apk add --no-cache git
RUN go mod download

RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/alert-service ./cmd

# ── Runtime stage ─────────────────────────────────────────────────────────────
FROM alpine:3.19

RUN apk --no-cache add ca-certificates tzdata

COPY --from=builder /bin/alert-service /bin/alert-service

EXPOSE 8082

ENTRYPOINT ["/bin/alert-service"]
