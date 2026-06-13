#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

export PATH="/usr/local/go/bin:/opt/homebrew/bin:$PATH"

if [ -z "${DOCKER_HOST:-}" ]; then
  DETECTED_HOST=$(docker context inspect --format '{{.Endpoints.docker.Host}}' 2>/dev/null || true)
  if [ -n "$DETECTED_HOST" ]; then
    export DOCKER_HOST="$DETECTED_HOST"
  else
    export DOCKER_HOST="unix:///var/run/docker.sock"
  fi
fi
echo "Using DOCKER_HOST=$DOCKER_HOST"

# Verify/Create Docker networks
if ! docker network inspect iicpc-net >/dev/null 2>&1; then
  docker network create iicpc-net
  echo "Created network: iicpc-net"
fi
# Recreate sandbox network without --internal for local host-to-container routing
docker network rm sandbox-net >/dev/null 2>&1 || true
docker network create sandbox-net
echo "Created network: sandbox-net"

SANDBOX_IMAGE="iicpc-sandbox:v1"
RUNTIME_IMAGE="iicpc-runtime-sandbox:v1"

echo "=== 1. Building/Verifying Contestant Sandbox Images ==="
docker build -f Dockerfile.sandbox -t "$SANDBOX_IMAGE" .
docker build -f Dockerfile.runtime-sandbox -t "$RUNTIME_IMAGE" .

echo "=== 2. Starting Infrastructure Services (PostgreSQL + Redis + MinIO) ==="
docker compose up -d postgres redis minio init-db

# Autodetect Postgres container
POSTGRES_CONTAINER=$(docker ps --filter name=postgres --format "{{.Names}}" | head -n 1)
REDIS_CONTAINER=$(docker ps --filter name=redis --format "{{.Names}}" | head -n 1)
MINIO_CONTAINER=$(docker ps --filter name=minio --format "{{.Names}}" | head -n 1)
if [ -z "$POSTGRES_CONTAINER" ]; then
  echo "Error: Postgres container is not running"
  exit 1
fi
if [ -z "$REDIS_CONTAINER" ]; then
  echo "Error: Redis container is not running"
  exit 1
fi
if [ -z "$MINIO_CONTAINER" ]; then
  echo "Error: MinIO container is not running"
  exit 1
fi

echo "=== 3. Waiting for Postgres, Redis and MinIO to be healthy ==="
for _ in {1..30}; do
  if docker exec "$POSTGRES_CONTAINER" pg_isready -U iicpc -d iicpc_db >/dev/null 2>&1; then
    if docker exec "$REDIS_CONTAINER" redis-cli ping >/dev/null 2>&1; then
      if curl -fs http://localhost:9000/minio/health/live >/dev/null 2>&1; then
        break
      fi
    fi
  fi
  sleep 1
done

# Run Migrations
echo "=== 4. Waiting for PostgreSQL Migrations to complete ==="
docker wait iicpc-init-db >/dev/null || true

# Set world-writable permission on submissions dir to avoid sandboxed compiler permission conflicts
mkdir -p submissions
chmod 777 submissions

echo "=== 5. Starting Platform Microservices in Background ==="
export REDIS_ADDR="127.0.0.1:6379"
export DB_ADDR="postgres://iicpc:iicpc_secret@127.0.0.1:5432/iicpc_db?sslmode=disable"
export S3_ENDPOINT="127.0.0.1:9000"
export S3_ACCESS_KEY="minioadmin"
export S3_SECRET_KEY="minioadmin"
export S3_BUCKET="submissions"
export S3_USE_SSL="false"
export COMPILE_IMAGE="iicpc-sandbox:v1"
export RUNTIME_IMAGE="iicpc-runtime-sandbox:v1"

# Process registry for cleanup
PIDS=()
cleanup() {
  echo "=== Shutting down and cleaning up microservice workers ==="
  for pid in "${PIDS[@]}"; do
    kill "$pid" >/dev/null 2>&1 || true
  done
  rm -rf bin
}
trap cleanup EXIT

echo "=== 5. Compiling Microservices ==="
mkdir -p bin
go build -o bin/gateway services/gateway/*.go
go build -o bin/compiler services/compiler/*.go
go build -o bin/testing services/testing/*.go
go build -o bin/leaderboard services/leaderboard/*.go

# Run services
./bin/gateway > /tmp/e2e_gateway.log 2>&1 &
PIDS+=($!)

./bin/compiler > /tmp/e2e_compiler.log 2>&1 &
PIDS+=($!)

./bin/testing > /tmp/e2e_testing.log 2>&1 &
PIDS+=($!)

./bin/leaderboard > /tmp/e2e_leaderboard.log 2>&1 &
PIDS+=($!)

# Wait for gateway to start
echo "=== Waiting for Submission Gateway to listen on port 3000 ==="
for _ in {1..30}; do
  if curl -fsS http://localhost:3000/api/v1/builds >/dev/null 2>&1; then
    break
  fi
  sleep 1
done

echo "=== 6. Executing Go E2E Test Suite ==="
TEST_EXIT_CODE=0
go test -v ./tests/... || TEST_EXIT_CODE=$?

if [ "$TEST_EXIT_CODE" -eq 0 ]; then
  echo "=== SUCCESS: ALL END-TO-END TESTS PASSED SUCCESSFULLY! ==="
else
  echo "=== ERROR: E2E TEST SUITE FAILED WITH EXIT CODE: $TEST_EXIT_CODE ==="
  echo "--- Gateway Logs ---"
  cat /tmp/e2e_gateway.log || true
  echo "--- Compiler Logs ---"
  cat /tmp/e2e_compiler.log || true
  echo "--- Testing Logs ---"
  cat /tmp/e2e_testing.log || true
  exit "$TEST_EXIT_CODE"
fi
