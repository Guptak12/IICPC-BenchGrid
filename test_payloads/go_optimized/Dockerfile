# Build stage
FROM golang:1.26-alpine AS builder
WORKDIR /app
COPY main.go .
RUN go env -w GO111MODULE=off
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o engine main.go

# Final stage
FROM alpine:3.19
WORKDIR /app
COPY --from=builder /app/engine .
EXPOSE 8000
CMD ["./engine"]
