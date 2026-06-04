#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

export DOCKER_HOST="${DOCKER_HOST:-unix:///var/run/docker.sock}"

# Verify/Create Docker networks
if ! docker network inspect iicpc-net >/dev/null 2>&1; then
  docker network create iicpc-net
  echo "Created network: iicpc-net"
fi
if ! docker network inspect sandbox-net >/dev/null 2>&1; then
  docker network create sandbox-net
  echo "Created network: sandbox-net"
fi

SANDBOX_IMAGE="iicpc-sandbox:v1"
FLEET_IMAGE="bot-fleet:v1"

echo "=== 1. Building Contestant Sandbox Image ==="
docker build -f Dockerfile.sandbox -t "$SANDBOX_IMAGE" .

echo "=== 2. Building Bot Fleet Image ==="
docker build -f bot-fleet/Dockerfile.botfleet -t "$FLEET_IMAGE" ./bot-fleet

echo "=== 3. Starting Infrastructure Services (PostgreSQL + Redis + Redpanda) ==="
docker compose up -d postgres redis redpanda || true

# Autodetect containers
POSTGRES_CONTAINER=$(docker ps --filter name=postgres --format "{{.Names}}" | head -n 1)
REDIS_CONTAINER=$(docker ps --filter name=redis --format "{{.Names}}" | head -n 1)
REDPANDA_CONTAINER=$(docker ps --filter name=redpanda --format "{{.Names}}" | head -n 1)

if [ -z "$POSTGRES_CONTAINER" ]; then
  echo "Error: Postgres container is not running"
  exit 1
fi
if [ -z "$REDIS_CONTAINER" ]; then
  echo "Error: Redis container is not running"
  exit 1
fi
if [ -z "$REDPANDA_CONTAINER" ]; then
  echo "Error: Redpanda container is not running"
  exit 1
fi

echo "=== 4. Waiting for Postgres, Redis and Redpanda to be healthy ==="
for _ in {1..30}; do
  if docker exec "$POSTGRES_CONTAINER" pg_isready -U iicpc -d iicpc_db >/dev/null 2>&1; then
    if docker exec "$REDIS_CONTAINER" redis-cli ping >/dev/null 2>&1; then
      if docker exec "$REDPANDA_CONTAINER" rpk cluster status >/dev/null 2>&1; then
        break
      fi
    fi
  fi
  sleep 1
done

echo "=== 4b. Pre-creating Kafka Topics ==="
docker exec -i "$REDPANDA_CONTAINER" rpk topic create order-events -p 6 || true

# Initialize PostgreSQL Schema
echo "=== 5. Running PostgreSQL Migrations ==="
docker exec -i "$POSTGRES_CONTAINER" psql -U iicpc -d iicpc_db < migrations/001_submissions.sql

# Pre-create submissions folder to guarantee write access
mkdir -p submissions
chmod 777 submissions

echo "=== 6. Compiling Microservices ==="
mkdir -p bin
go build -o bin/gateway services/gateway/*.go
go build -o bin/compiler services/compiler/*.go
go build -o bin/pretest services/pretest/*.go

echo "=== 6b. Starting Platform Microservices (Gateway + Compiler + Pretest) ==="
export REDIS_ADDR="127.0.0.1:6379"
export DB_ADDR="postgres://iicpc:iicpc_secret@127.0.0.1:5432/iicpc_db?sslmode=disable"
export SANDBOX_NET="sandbox-net"

PIDS=()
cleanup() {
  echo "=== Cleaning up services ==="
  for pid in "${PIDS[@]}"; do
    kill "$pid" >/dev/null 2>&1 || true
  done
  docker rm -f contestant-systest-run >/dev/null 2>&1 || true
  docker compose -f docker-compose.workers.yml down || true
  docker compose down || true
}
trap cleanup EXIT

# Run microservices in background
./bin/gateway > /tmp/gateway_sys.log 2>&1 &
PIDS+=($!)
./bin/compiler > /tmp/compiler_sys.log 2>&1 &
PIDS+=($!)
./bin/pretest > /tmp/pretest_sys.log 2>&1 &
PIDS+=($!)

# Wait for gateway to start
echo "=== Waiting for Submission Gateway to listen on port 3000 ==="
for _ in {1..30}; do
  if curl -fsS http://localhost:3000/api/v1/builds >/dev/null 2>&1; then
    break
  fi
  sleep 1
done

echo "=== 7. Submitting Contestant Code for Compilation and Pretesting ==="
CONTESTANT_ID="systest-contestant-$(date +%s)"
SUBMIT_RESPONSE=$(curl -s -X POST http://localhost:3000/api/v1/submit \
  -F "contestant_id=${CONTESTANT_ID}" \
  -F "source_code=@test_payloads/main.cpp")

echo "Submit Response: ${SUBMIT_RESPONSE}"
SUBMISSION_ID=$(echo "${SUBMIT_RESPONSE}" | jq -r '.build_id')
echo "Submission ID: ${SUBMISSION_ID}"

echo "=== 8. Polling Submission Lifecycle until Compiled ==="
while true; do
  STATUS_RESPONSE=$(curl -s http://localhost:3000/api/v1/build/${SUBMISSION_ID})
  STATUS=$(echo "${STATUS_RESPONSE}" | jq -r '.status')
  VERDICT=$(echo "${STATUS_RESPONSE}" | jq -r '.verdict')
  echo "Current status: ${STATUS} | Verdict: ${VERDICT}"
  
  if [ "${STATUS}" = "completed" ] || [ "${STATUS}" = "failed" ]; then
    break
  fi
  sleep 1
done

if [ "${STATUS}" = "failed" ]; then
  echo "Error: Pretest or compilation failed. Stderr:"
  curl -s http://localhost:3000/api/v1/build/${SUBMISSION_ID}
  exit 1
fi

echo "=== 9. Starting Bot Fleet Cluster (Master + 3 Workers) ==="
docker compose -f docker-compose.workers.yml up -d master worker-1 worker-2 worker-3

echo "=== 10. Launching Sandboxed Contestant Engine ==="
# Launch contestant container in sandbox-net network
docker run -d \
  --name contestant-systest-run \
  --network sandbox-net \
  --network-alias "contestant-${SUBMISSION_ID}" \
  -v "${ROOT_DIR}/submissions/iicpc_${SUBMISSION_ID}:/usr/src:ro" \
  --cap-drop=ALL \
  --pids-limit 2048 \
  --memory=256m \
  --cpus=1.0 \
  "$SANDBOX_IMAGE" \
  /usr/src/app

# Let it boot up
sleep 2

echo "=== 11. Dispatching Post-Contest High Load System Test ==="
# Send run request to Bot Fleet Master on port 4000
RUN_BODY=$(cat <<EOF
{
  "job_id": "${SUBMISSION_ID}",
  "contestant_id": "${CONTESTANT_ID}",
  "endpoint": "ws://contestant-${SUBMISSION_ID}:8080/ws",
  "num_bots": 20,
  "orders_per_bot": 100,
  "rate_per_sec": 200.0,
  "strategy_mix": {
    "market_maker": 0.4,
    "momentum_trader": 0.3,
    "noise_trader": 0.3
  }
}
EOF
)

echo "Triggering load test with: ${RUN_BODY}"
FLEET_RUN_RESP=$(curl -s -X POST -H "Content-Type: application/json" -d "${RUN_BODY}" http://localhost:4000/run)
echo "Fleet Run Response: ${FLEET_RUN_RESP}"

echo "=== 12. Polling Bot Fleet Job Status ==="
while true; do
  FLEET_STATUS_RESP=$(curl -s http://localhost:4000/status/${SUBMISSION_ID})
  FLEET_STATUS=$(echo "${FLEET_STATUS_RESP}" | jq -r '.status')
  echo "Bot Fleet Job Status: ${FLEET_STATUS}"
  
  if [ "${FLEET_STATUS}" = "completed" ] || [ "${FLEET_STATUS}" = "aborted" ]; then
    break
  fi
  sleep 2
done

if [ "${FLEET_STATUS}" = "aborted" ]; then
  echo "Error: Bot fleet job aborted."
  echo "${FLEET_STATUS_RESP}"
  exit 1
fi

echo "=== 13. System Test Finished! Fetching Official Database Record ==="
# Query DB to check updated score/telemetry for this submission ID
docker exec -i "$POSTGRES_CONTAINER" psql -U iicpc -d iicpc_db -c \
  "SELECT id, contestant_id, status, verdict, composite_score, correctness_score, p99_us, actual_tps, diagnostics FROM submissions WHERE id='${SUBMISSION_ID}';"

echo "=== SYSTEM TEST SUITE COMPLETED SUCCESSFULLY! ==="
