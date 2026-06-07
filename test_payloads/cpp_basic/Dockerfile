# Build stage
FROM debian:bookworm-slim AS builder
RUN apt-get update && apt-get install -y --no-install-recommends g++ build-essential && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY main.cpp .
RUN g++ -O3 -std=c++17 -pthread -o engine main.cpp

# Final secure runtime
FROM gcr.io/distroless/cc-debian12
WORKDIR /app
COPY --from=builder /app/engine .
EXPOSE 8000
CMD ["./engine"]
