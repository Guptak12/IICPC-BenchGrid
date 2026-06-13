# Build stage
FROM golang:1.26-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY main.go .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o engine main.go

# Final stage
FROM alpine:3.19
WORKDIR /app
COPY --from=builder /app/engine .
ENV ENGINE_PROTOCOL=WS
EXPOSE 8000
CMD ["./engine"]
