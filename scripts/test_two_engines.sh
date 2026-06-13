#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

export DOCKER_HOST="unix:///var/run/docker.sock"

# Ensure standard networks exist
if ! docker network inspect iicpc-net >/dev/null 2>&1; then
  docker network create iicpc-net
  echo "Created network: iicpc-net"
fi
if ! docker network inspect sandbox-net >/dev/null 2>&1; then
  docker network create --internal sandbox-net
  echo "Created network: sandbox-net"
fi

SANDBOX_IMAGE="iicpc-sandbox:v1"

echo "=== 1. Building Contestant Sandbox Image ==="
docker build -f Dockerfile.sandbox -t "$SANDBOX_IMAGE" .

echo "=== 2. Starting Infrastructure Services (PostgreSQL + Redis) ==="
docker compose up -d postgres redis || true

# Autodetect running Postgres and Redis container names
POSTGRES_CONTAINER=$(docker ps --filter name=postgres --format "{{.Names}}" | head -n 1)
REDIS_CONTAINER=$(docker ps --filter name=redis --format "{{.Names}}" | head -n 1)

if [ -z "$POSTGRES_CONTAINER" ]; then
  echo "Error: Postgres container is not running"
  exit 1
fi
echo "Detected Postgres container: $POSTGRES_CONTAINER"
echo "Detected Redis container: $REDIS_CONTAINER"

echo "=== 3. Waiting for Postgres and Redis to be healthy ==="
for _ in {1..30}; do
  if docker exec "$POSTGRES_CONTAINER" pg_isready -U iicpc -d iicpc_db >/dev/null 2>&1; then
    if docker exec "$REDIS_CONTAINER" redis-cli ping >/dev/null 2>&1; then
      break
    fi
  fi
  sleep 1
done

# Initialize PostgreSQL Schema
echo "=== 4. Running PostgreSQL Migrations ==="
docker exec -i "$POSTGRES_CONTAINER" psql -U iicpc -d iicpc_db < migrations/001_submissions.sql

# Pre-create and open submissions folder to guarantee write access for compiler container
mkdir -p submissions
chmod 777 submissions

echo "=== 5. Starting Platform Microservices ==="
export REDIS_ADDR="127.0.0.1:6379"
export DB_ADDR="postgres://iicpc:iicpc_secret@127.0.0.1:5432/iicpc_db?sslmode=disable"

# Clean up background jobs on exit
PIDS=()
cleanup() {
  echo "=== Cleaning up services ==="
  for pid in "${PIDS[@]}"; do
    kill "$pid" >/dev/null 2>&1 || true
  done
  rm -rf bin
  docker compose down
}
trap cleanup EXIT

echo "=== 5. Compiling Microservices ==="
mkdir -p bin
go build -o bin/gateway services/gateway/*.go
go build -o bin/compiler services/compiler/*.go
go build -o bin/testing services/testing/*.go
go build -o bin/leaderboard services/leaderboard/*.go

# Run decoupled microservices
./bin/gateway > /tmp/gateway_test.log 2>&1 &
PIDS+=($!)

./bin/compiler > /tmp/compiler_test.log 2>&1 &
PIDS+=($!)

./bin/testing > /tmp/testing_test.log 2>&1 &
PIDS+=($!)

./bin/leaderboard > /tmp/leaderboard_test.log 2>&1 &
PIDS+=($!)

# Wait for gateway to start
echo "=== Waiting for Submission Gateway to listen on port 3000 ==="
for _ in {1..30}; do
  if curl -fsS http://localhost:3000/api/v1/builds >/dev/null 2>&1; then
    break
  fi
  sleep 1
done

echo "=== 6. Submitting Engine 1: Baseline Engine ==="
SUBMIT_RESPONSE_1="$(curl -fsS -X POST \
  -F "source_code=@test_payloads/main.cpp" \
  -F "contestant_id=engine-baseline" \
  http://localhost:3000/api/v1/submit)"
echo "Submit Response 1: $SUBMIT_RESPONSE_1"
BUILD_ID_1="$(printf '%s' "$SUBMIT_RESPONSE_1" | sed -n 's/.*"build_id":"\([^"]*\)".*/\1/p')"

echo "=== 7. Submitting Engine 2: Ack Only Engine ==="
SUBMIT_RESPONSE_2="$(curl -fsS -X POST \
  -F "source_code=@test_payloads/ack_only.cpp" \
  -F "contestant_id=engine-ack-only" \
  http://localhost:3000/api/v1/submit)"
echo "Submit Response 2: $SUBMIT_RESPONSE_2"
BUILD_ID_2="$(printf '%s' "$SUBMIT_RESPONSE_2" | sed -n 's/.*"build_id":"\([^"]*\)".*/\1/p')"

if [ -z "$BUILD_ID_1" ] || [ -z "$BUILD_ID_2" ]; then
  echo "Failed to extract build_ids from submission responses"
  exit 1
fi

echo "=== 8. Polling Both Submission Lifecycles ==="
COMPLETED_1=false
COMPLETED_2=false

for _ in {1..60}; do
  if [ "$COMPLETED_1" = false ]; then
    STATUS_1_JSON="$(curl -fsS "http://localhost:3000/api/v1/build/${BUILD_ID_1}")"
    STATUS_1="$(printf '%s' "$STATUS_1_JSON" | sed -n 's/.*"status":"\([^"]*\)".*/\1/p')"
    VERDICT_1="$(printf '%s' "$STATUS_1_JSON" | sed -n 's/.*"verdict":"\([^"]*\)".*/\1/p')"
    SCORE_1="$(printf '%s' "$STATUS_1_JSON" | sed -n 's/.*"composite_score":\([^,}]*\).*/\1/p')"
    echo "Engine 1 [Baseline]: Status=$STATUS_1 | Verdict=$VERDICT_1 | Score=${SCORE_1:-0}"
    if [ "$STATUS_1" = "completed" ] || [ "$STATUS_1" = "failed" ]; then
      COMPLETED_1=true
    fi
  fi

  if [ "$COMPLETED_2" = false ]; then
    STATUS_2_JSON="$(curl -fsS "http://localhost:3000/api/v1/build/${BUILD_ID_2}")"
    STATUS_2="$(printf '%s' "$STATUS_2_JSON" | sed -n 's/.*"status":"\([^"]*\)".*/\1/p')"
    VERDICT_2="$(printf '%s' "$STATUS_2_JSON" | sed -n 's/.*"verdict":"\([^"]*\)".*/\1/p')"
    SCORE_2="$(printf '%s' "$STATUS_2_JSON" | sed -n 's/.*"composite_score":\([^,}]*\).*/\1/p')"
    echo "Engine 2 [Ack Only]: Status=$STATUS_2 | Verdict=$VERDICT_2 | Score=${SCORE_2:-0}"
    if [ "$STATUS_2" = "completed" ] || [ "$STATUS_2" = "failed" ]; then
      COMPLETED_2=true
    fi
  fi

  if [ "$COMPLETED_1" = true ] && [ "$COMPLETED_2" = true ]; then
    break
  fi
  sleep 2
done

echo "=== 9. Verifying Leaderboard Standing ==="
sleep 3
if [ -f "frontend/leaderboard.json" ]; then
  echo "Static Leaderboard JSON:"
  cat "frontend/leaderboard.json"
else
  echo "Error: static leaderboard.json was not generated"
  exit 1
fi

echo "=== LEADERBOARD VALIDATION COMPLETED SUCCESSFULLY! ==="
