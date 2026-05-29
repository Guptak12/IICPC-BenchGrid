#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

NETWORK_NAME="${NETWORK_NAME:-iicpc-net}"
SANDBOX_IMAGE="${SANDBOX_IMAGE:-iicpc-sandbox:v1}"
BOT_IMAGE="${BOT_IMAGE:-bot-fleet:v1}"
PAYLOAD="${PAYLOAD:-test_payloads/main.cpp}"
NUM_BOTS="${NUM_BOTS:-6}"
ORDERS_PER_BOT="${ORDERS_PER_BOT:-20}"
RATE_PER_SEC="${RATE_PER_SEC:-20}"
MARKET_MAKER_PCT="${MARKET_MAKER_PCT:-1.0}"
MOMENTUM_PCT="${MOMENTUM_PCT:-0.0}"
NOISE_PCT="${NOISE_PCT:-0.0}"

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

docker exec redpanda rpk topic create order-events fill-events --brokers localhost:9092 >/dev/null 2>&1 || true

docker compose -f docker-compose.workers.yml up -d --force-recreate worker-1 worker-2 worker-3 master

ORCH_LOG="${ORCH_LOG:-/tmp/iicpc-orchestrator.log}"
GOCACHE="${GOCACHE:-/tmp/go-build-iicpc-local}"
export GOCACHE

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

SUBMIT_RESPONSE="$(curl -fsS -X POST -F "source_code=@${PAYLOAD}" http://localhost:3000/api/v1/submit)"
BUILD_ID="$(printf '%s' "$SUBMIT_RESPONSE" | sed -n 's/.*"build_id":"\([^"]*\)".*/\1/p')"

if [ -z "$BUILD_ID" ]; then
  echo "Could not parse build_id from: $SUBMIT_RESPONSE" >&2
  exit 1
fi
echo "Build ID: $BUILD_ID"

ENDPOINT=""
for _ in $(seq 1 90); do
  BUILD_STATUS="$(curl -fsS "http://localhost:3000/api/v1/build/${BUILD_ID}")"
  STATUS="$(printf '%s' "$BUILD_STATUS" | sed -n 's/.*"status":"\([^"]*\)".*/\1/p')"
  if [ "$STATUS" = "running" ]; then
    ENDPOINT="$(printf '%s' "$BUILD_STATUS" | sed -n 's/.*"endpoint":"\([^"]*\)".*/\1/p')"
    break
  fi
  if [ "$STATUS" = "failed" ]; then
    echo "$BUILD_STATUS" >&2
    exit 1
  fi
  sleep 1
done

if [ -z "$ENDPOINT" ]; then
  echo "Sandbox did not reach running state. Orchestrator log: $ORCH_LOG" >&2
  exit 1
fi
echo "Sandbox endpoint: $ENDPOINT"

RUN_BODY="$(printf '{"endpoint":"%s","num_bots":%s,"orders_per_bot":%s,"mid_price":100.0,"spread":0.10,"rate_per_sec":%s,"strategy_mix":{"market_maker":%s,"momentum_trader":%s,"noise_trader":%s}}' "$ENDPOINT" "$NUM_BOTS" "$ORDERS_PER_BOT" "$RATE_PER_SEC" "$MARKET_MAKER_PCT" "$MOMENTUM_PCT" "$NOISE_PCT")"
RUN_RESPONSE="$(curl -fsS -X POST -H 'Content-Type: application/json' -d "$RUN_BODY" http://127.0.0.1:4000/run)"
JOB_ID="$(printf '%s' "$RUN_RESPONSE" | sed -n 's/.*"job_id":"\([^"]*\)".*/\1/p')"

if [ -z "$JOB_ID" ]; then
  echo "Could not parse job_id from: $RUN_RESPONSE" >&2
  exit 1
fi
echo "Job ID: $JOB_ID"

FINAL_STATUS=""
for _ in $(seq 1 120); do
  JOB_STATUS="$(curl -fsS "http://127.0.0.1:4000/status/${JOB_ID}")"
  STATUS="$(printf '%s' "$JOB_STATUS" | sed -n 's/.*"status":"\([^"]*\)".*/\1/p')"
  if [ "$STATUS" = "completed" ] || [ "$STATUS" = "aborted" ]; then
    FINAL_STATUS="$STATUS"
    printf '%s\n' "$JOB_STATUS"
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
