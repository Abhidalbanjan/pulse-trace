FROM golang:1.24-alpine AS builder

WORKDIR /app

# Copy the shared module first
COPY shared ./shared

# Copy the topology-service module
COPY topology-service ./topology-service

# Build the topology-service
WORKDIR /app/topology-service
RUN apk add --no-cache git
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -o /topology-service ./cmd/main.go

FROM alpine:latest

WORKDIR /
COPY --from=builder /topology-service /topology-service

EXPOSE 8084

ENTRYPOINT ["/topology-service"]
