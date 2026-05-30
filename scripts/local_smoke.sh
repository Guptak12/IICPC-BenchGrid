#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

NETWORK_NAME="${NETWORK_NAME:-iicpc-net}"
SANDBOX_IMAGE="${SANDBOX_IMAGE:-iicpc-sandbox:v1}"
BOT_IMAGE="${BOT_IMAGE:-bot-fleet:v1}"
PAYLOAD="${PAYLOAD:-test_payloads/main.cpp}"

# ── Exam config — kept tiny so a laptop can run this in ~30 seconds ──────────
export EXAM_NUM_BOTS="${EXAM_NUM_BOTS:-5}"
export EXAM_ORDERS_PER_BOT="${EXAM_ORDERS_PER_BOT:-20}"
export EXAM_RATE_PER_SEC="${EXAM_RATE_PER_SEC:-10}"
export EXAM_SEED="${EXAM_SEED:-42424242}"
MARKET_MAKER_PCT="${MARKET_MAKER_PCT:-1.0}"
MOMENTUM_PCT="${MOMENTUM_PCT:-0.0}"
NOISE_PCT="${NOISE_PCT:-0.0}"
echo "Smoke config: ${EXAM_NUM_BOTS} bots × ${EXAM_ORDERS_PER_BOT} orders @ ${EXAM_RATE_PER_SEC}/s"

# ── Networks ──────────────────────────────────────────────────────────────────

# 1. Create the standard bridge network for the infrastructure
if ! docker network inspect "$NETWORK_NAME" >/dev/null 2>&1; then
  docker network create "$NETWORK_NAME" >/dev/null
  echo "Created network: $NETWORK_NAME"
fi

# 2. Create the airgapped internal network for the contestant sandboxes
if ! docker network inspect sandbox-net >/dev/null 2>&1; then
  docker network create --internal sandbox-net >/dev/null
  echo "Created network: sandbox-net"
fi

docker build -f Dockerfile.sandbox -t "$SANDBOX_IMAGE" .
docker build -f bot-fleet/Dockerfile.botfleet -t "$BOT_IMAGE" bot-fleet

docker compose -f docker-compose.yml up -d redpanda

for _ in $(seq 1 60); do
  if docker exec redpanda rpk topic list --brokers localhost:9092 >/dev/null 2>&1; then
    break
  fi
  sleep 1
done

docker exec redpanda rpk topic create order-events \
  --brokers localhost:9092 \
  --partitions 3 \
  --replicas 1 \
  >/dev/null 2>&1 || true

docker exec redpanda rpk topic create fill-events \
  --brokers localhost:9092 \
  --partitions 3 \
  --replicas 1 \
  >/dev/null 2>&1 || true

docker compose -f docker-compose.workers.yml up -d --force-recreate redis worker-1 worker-2 worker-3 master

ORCH_LOG="${ORCH_LOG:-/tmp/iicpc-orchestrator.log}"
GOCACHE="${GOCACHE:-/tmp/go-build-iicpc-local}"
export GOCACHE

export MASTER_ADDR="http://127.0.0.1:4000"

go run . >"$ORCH_LOG" 2>&1 &
ORCH_PID=$!
cleanup() {
  kill "$ORCH_PID" >/dev/null 2>&1 || true
}
trap cleanup EXIT

for _ in $(seq 1 60); do
  if curl -fsS http://localhost:3000/api/v1/builds >/dev/null 2>&1; then
    break
  fi
  sleep 1
done

# --- CORRECTED CURL SYNTAX ---
SUBMIT_RESPONSE="$(curl -fsS -X POST \
  -F "source_code=@${PAYLOAD}" \
  -F "contestant_id=kush-gupta-01" \
  http://localhost:3000/api/v1/submit)"

BUILD_ID="$(printf '%s' "$SUBMIT_RESPONSE" | sed -n 's/.*"build_id":"\([^"]*\)".*/\1/p')"

if [ -z "$BUILD_ID" ]; then
  echo "Could not parse build_id from: $SUBMIT_RESPONSE" >&2
  exit 1
fi
echo "Build ID: $BUILD_ID"

ENDPOINT=""
JOB_ID=""

# Wait for the build to finish AND the Master to assign a Job ID
for _ in $(seq 1 90); do
  BUILD_STATUS="$(curl -fsS "http://localhost:3000/api/v1/build/${BUILD_ID}")"
  STATUS="$(printf '%s' "$BUILD_STATUS" | sed -n 's/.*"status":"\([^"]*\)".*/\1/p')"
  
  if [ "$STATUS" = "running" ]; then
    ENDPOINT="$(printf '%s' "$BUILD_STATUS" | sed -n 's/.*"endpoint":"\([^"]*\)".*/\1/p')"
    JOB_ID="$(printf '%s' "$BUILD_STATUS" | sed -n 's/.*"job_id":"\([^"]*\)".*/\1/p')"
    
    # Wait a fraction of a second for the async auto-trigger to populate the job_id
    if [ -n "$JOB_ID" ]; then
      break
    fi
  fi
  if [ "$STATUS" = "failed" ]; then
    echo "Build Failed: $BUILD_STATUS" >&2
    exit 1
  fi
  sleep 1
done

if [ -z "$ENDPOINT" ] || [ -z "$JOB_ID" ]; then
  echo "Sandbox did not reach running state or failed to trigger master. Orchestrator log: $ORCH_LOG" >&2
  exit 1
fi
echo "Sandbox endpoint: $ENDPOINT"
echo "Job ID: $JOB_ID"

FINAL_STATUS=""
# Track the exact job on the Master node (Restores fast-fail on aborted jobs!)
for _ in $(seq 1 120); do
  JOB_STATUS="$(curl -fsS "http://127.0.0.1:4000/status/${JOB_ID}")"
  STATUS="$(printf '%s' "$JOB_STATUS" | sed -n 's/.*"status":"\([^"]*\)".*/\1/p')"
  
  if [ "$STATUS" = "completed" ] || [ "$STATUS" = "aborted" ]; then
    FINAL_STATUS="$STATUS"
    printf '%s\n' "$JOB_STATUS" | python3 -m json.tool
    break
  fi
  sleep 1
done

if [ -z "$FINAL_STATUS" ]; then
  echo "Job did not finish before timeout. Last status:" >&2
  printf '%s\n' "${JOB_STATUS:-<none>}" >&2
  exit 1
fi

if [ "$FINAL_STATUS" != "completed" ]; then
  exit 1
fi
