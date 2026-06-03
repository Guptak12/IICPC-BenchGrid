#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

export DOCKER_HOST="${DOCKER_HOST:-unix:///var/run/docker.sock}"

# Ensure standard networks exist
if ! docker network inspect iicpc-net >/dev/null 2>&1; then
  docker network create iicpc-net
  echo "Created network: iicpc-net"
fi
if ! docker network inspect sandbox-net >/dev/null 2>&1; then
  docker network create --internal sandbox-net
  echo "Created network: sandbox-net"
fi

SANDBOX_IMAGE="${SANDBOX_IMAGE:-iicpc-sandbox:v1}"
PAYLOAD="${PAYLOAD:-test_payloads/main.cpp}"

echo "=== 1. Skipping sandbox image build (pre-built) ==="
# docker build -f Dockerfile.sandbox -t "$SANDBOX_IMAGE" .

echo "=== 2. Skipping infrastructure startup (already running) ==="
# docker compose up -d postgres redis || true

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
docker exec -i "$POSTGRES_CONTAINER" psql -U iicpc -d iicpc_db < migrations/002_add_source_code.sql

# Pre-create and open submissions folder to guarantee write access for compiler container
mkdir -p submissions
chmod 777 submissions

echo "=== 5. Starting Platform Microservices ==="
export REDIS_ADDR="127.0.0.1:6379"
export DB_ADDR="postgres://iicpc:iicpc_secret@127.0.0.1:5432/iicpc_db?sslmode=disable"
export SANDBOX_NET="host"

# Clean up background jobs on exit
PIDS=()
cleanup() {
  echo "=== Cleaning up services ==="
  for pid in "${PIDS[@]}"; do
    kill "$pid" >/dev/null 2>&1 || true
  done
  # docker compose down
}
trap cleanup EXIT

# Run decoupled microservices
go run services/gateway/*.go > /tmp/gateway.log 2>&1 &
PIDS+=($!)

go run services/compiler/*.go > /tmp/compiler.log 2>&1 &
PIDS+=($!)

go run services/pretest/*.go > /tmp/pretest.log 2>&1 &
PIDS+=($!)

go run services/leaderboard/*.go > /tmp/leaderboard.log 2>&1 &
PIDS+=($!)

# Wait for gateway to start
echo "=== Waiting for Submission Gateway to listen on port 3000 ==="
for _ in {1..30}; do
  if curl -fsS http://localhost:3000/api/v1/builds >/dev/null 2>&1; then
    break
  fi
  sleep 1
done

echo "=== 6. Submitting Contestant Code ==="
SUBMIT_RESPONSE="$(curl -fsS -X POST \
  -F "source_code=@${PAYLOAD}" \
  -F "contestant_id=smoke-contestant-$(date +%s)" \
  http://localhost:3000/api/v1/submit)"

echo "Submit Response: $SUBMIT_RESPONSE"
BUILD_ID="$(printf '%s' "$SUBMIT_RESPONSE" | sed -n 's/.*"build_id":"\([^"]*\)".*/\1/p')"

if [ -z "$BUILD_ID" ]; then
  echo "Failed to extract build_id from submission response"
  exit 1
fi
echo "Submission ID: $BUILD_ID"

echo "=== 7. Polling Submission Lifecycle Status ==="
FINAL_STATUS=""
for _ in {1..60}; do
  BUILD_STATUS="$(curl -fsS "http://localhost:3000/api/v1/build/${BUILD_ID}")"
  STATUS="$(printf '%s' "$BUILD_STATUS" | sed -n 's/.*"status":"\([^"]*\)".*/\1/p')"
  VERDICT="$(printf '%s' "$BUILD_STATUS" | sed -n 's/.*"verdict":"\([^"]*\)".*/\1/p')"
  SCORE="$(printf '%s' "$BUILD_STATUS" | sed -n 's/.*"composite_score":\([^,}]*\).*/\1/p')"
  
  echo "Current status: $STATUS | Verdict: $VERDICT | Score: ${SCORE:-0.0}"
  
  if [ "$STATUS" = "completed" ]; then
    FINAL_STATUS="$STATUS"
    echo "=== SUCCESS: Submission completed execution! ==="
    printf '%s\n' "$BUILD_STATUS" | python3 -m json.tool || echo "$BUILD_STATUS"
    break
  fi
  
  if [ "$STATUS" = "failed" ]; then
    echo "=== ERROR: Compilation/Execution Failed! ==="
    printf '%s\n' "$BUILD_STATUS" | python3 -m json.tool || echo "$BUILD_STATUS"
    exit 1
  fi
  
  sleep 1
done

if [ -z "$FINAL_STATUS" ]; then
  echo "Timeout waiting for submission to compile and execute"
  exit 1
fi

echo "=== 8. Checking Leaderboard Static JSON ==="
sleep 3 # wait for next periodic leaderboard generation tick
if [ -f "frontend/leaderboard.json" ]; then
  echo "Leaderboard generated successfully! Content:"
  cat "frontend/leaderboard.json"
else
  echo "Error: static leaderboard.json was not generated"
  exit 1
fi

echo "=== SMOKE TEST PASSED SUCCESSFULLY! ==="
