# Build stage
FROM rust:1.75-slim AS builder
WORKDIR /usr/src/app
COPY Cargo.toml .
COPY src src
RUN cargo build --release --offline

# Run stage
FROM debian:bookworm-slim
WORKDIR /app
COPY --from=builder /usr/src/app/target/release/rust_crash .
EXPOSE 8000
CMD ["./rust_crash"]
