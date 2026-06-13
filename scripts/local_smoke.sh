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

# Ensure standard networks exist
if ! docker network inspect iicpc-net >/dev/null 2>&1; then
  docker network create iicpc-net
  echo "Created network: iicpc-net"
fi
# Recreate sandbox network without --internal for local host-to-container routing
docker network rm sandbox-net >/dev/null 2>&1 || true
docker network create sandbox-net
echo "Created network: sandbox-net"

PAYLOAD="${PAYLOAD:-submission.zip}"

echo "=== 2. Starting Infrastructure Services (PostgreSQL + Redis + MinIO) ==="
docker compose up -d postgres redis minio init-db

# Autodetect running Postgres, Redis and MinIO container names
POSTGRES_CONTAINER=$(docker ps --filter name=postgres --format "{{.Names}}" | head -n 1)
REDIS_CONTAINER=$(docker ps --filter name=redis --format "{{.Names}}" | head -n 1)
MINIO_CONTAINER=$(docker ps --filter name=minio --format "{{.Names}}" | head -n 1)

if [ -z "$POSTGRES_CONTAINER" ]; then
  echo "Error: Postgres container is not running"
  exit 1
fi
echo "Detected Postgres container: $POSTGRES_CONTAINER"
echo "Detected Redis container: $REDIS_CONTAINER"
echo "Detected MinIO container: $MINIO_CONTAINER"

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

# Initialize PostgreSQL Schema
echo "=== 4. Waiting for PostgreSQL Migrations to complete ==="
docker wait iicpc-init-db >/dev/null || true

# Pre-create and open submissions folder to guarantee write access for compiler container
mkdir -p submissions
chmod 777 submissions

ENGINE_NAME="${1:-go_optimized}"
echo "=== Packaging Mock Submission: $ENGINE_NAME ==="
if [ ! -d "test_payloads/$ENGINE_NAME" ]; then
  echo "Error: test_payloads/$ENGINE_NAME directory does not exist"
  exit 1
fi
rm -f "$ROOT_DIR/submission.zip"
(cd "test_payloads/$ENGINE_NAME" && zip -q -r "$ROOT_DIR/submission.zip" .)

echo "=== 5. Compiling Microservices ==="
mkdir -p bin
go build -o bin/gateway services/gateway/*.go
go build -o bin/compiler services/compiler/*.go
go build -o bin/testing services/testing/*.go

echo "=== 5. Starting Platform Microservices ==="
export REDIS_ADDR="127.0.0.1:6379"
export DB_ADDR="postgres://iicpc:iicpc_secret@127.0.0.1:5432/iicpc_db?sslmode=disable"
export SANDBOX_NET="sandbox-net"
export S3_ENDPOINT="127.0.0.1:9000"
export S3_ACCESS_KEY="minioadmin"
export S3_SECRET_KEY="minioadmin"
export S3_BUCKET="submissions"
export S3_USE_SSL="false"

# Clean up background jobs on exit
PIDS=()
cleanup() {
  echo "=== Cleaning up services ==="
  for pid in "${PIDS[@]}"; do
    kill "$pid" >/dev/null 2>&1 || true
  done
  rm -f "$ROOT_DIR/submission.zip"
  # docker compose down
}
trap cleanup EXIT

# Run decoupled microservices
./bin/gateway > /tmp/gateway.log 2>&1 &
PIDS+=($!)

./bin/compiler > /tmp/compiler.log 2>&1 &
PIDS+=($!)

./bin/testing > /tmp/testing.log 2>&1 &
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
for _ in {1..120}; do
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
