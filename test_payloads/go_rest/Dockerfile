# Build stage
FROM golang:1.26-alpine AS builder
WORKDIR /app
COPY go.mod ./
COPY main.go .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o engine main.go

# Final stage
FROM alpine:3.19
WORKDIR /app
COPY --from=builder /app/engine .
ENV ENGINE_PROTOCOL=REST
EXPOSE 8000
CMD ["./engine"]
