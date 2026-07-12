FROM golang:1.21-alpine

WORKDIR /app
COPY go.mod go.sum ./
RUN apk add --no-cache git
RUN go mod download
COPY . .
RUN go build -o main ./cmd/main.go

EXPOSE 8085
CMD ["./main"]
