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
if ! docker network inspect sandbox-net >/dev/null 2>&1; then
  docker network create sandbox-net
  echo "Created network: sandbox-net"
fi


echo "=== 2. Starting Infrastructure Services (PostgreSQL + Redis + MinIO + Prometheus + Grafana) ==="
docker compose up -d postgres redis minio prometheus grafana init-db

POSTGRES_CONTAINER=$(docker ps --filter name=postgres --format "{{.Names}}" | head -n 1)
REDIS_CONTAINER=$(docker ps --filter name=redis --format "{{.Names}}" | head -n 1)
MINIO_CONTAINER=$(docker ps --filter name=minio --format "{{.Names}}" | head -n 1)

echo "=== 3. Waiting for services to be healthy ==="
for _ in {1..30}; do
  if docker exec "$POSTGRES_CONTAINER" pg_isready -U iicpc -d iicpc_db >/dev/null 2>&1; then
    if docker exec "$REDIS_CONTAINER" redis-cli ping >/dev/null 2>&1; then
      if curl -fs http://localhost:9000/minio/health/live >/dev/null 2>&1; then
        if curl -fs http://localhost:9090/-/healthy >/dev/null 2>&1; then
          if curl -fs http://localhost:3001/api/health >/dev/null 2>&1; then
            break
          fi
        fi
      fi
    fi
  fi
  sleep 1
done

echo "=== 4. Waiting for migrations ==="
docker wait iicpc-init-db >/dev/null || true

# Pre-create submissions folder to guarantee write access
mkdir -p submissions
chmod 777 submissions

echo "=== 5. Compiling Microservices ==="
mkdir -p bin
go build -o bin/gateway services/gateway/*.go
go build -o bin/compiler services/compiler/*.go
go build -o bin/testing services/testing/*.go

echo "=== 6. Launching Platform Microservices in the Background ==="
export REDIS_ADDR="127.0.0.1:6379"
export DB_ADDR="postgres://iicpc:iicpc_secret@127.0.0.1:5432/iicpc_db?sslmode=disable"
export SANDBOX_NET="sandbox-net"
export S3_ENDPOINT="127.0.0.1:9000"
export S3_ACCESS_KEY="minioadmin"
export S3_SECRET_KEY="minioadmin"
export S3_BUCKET="submissions"
export S3_USE_SSL="false"
export SWEEPER_TIMEOUT_MINUTES="30"

# Kill existing background instances of our services to avoid port binding conflicts
killall gateway compiler testing 2>/dev/null || true
sleep 2

./bin/gateway > /tmp/gateway.log 2>&1 &
GATEWAY_PID=$!
./bin/compiler > /tmp/compiler.log 2>&1 &
COMPILER_PID=$!
./bin/testing > /tmp/testing.log 2>&1 &
TESTING_PID=$!

echo "Gateway PID: $GATEWAY_PID"
echo "Compiler PID: $COMPILER_PID"
echo "Testing PID: $TESTING_PID"

echo "=== 7. Platform Services started! ==="
echo "Contestant UI / Dashboard: http://localhost:3000"
echo "MinIO Console:             http://localhost:9001"
echo "Prometheus Target:         http://localhost:9090"
echo "Grafana Dashboard:         http://localhost:3001"
echo ""
echo "Press Ctrl+C to stop the services."

# Block until user cancels, then clean up
cleanup() {
  echo ""
  echo "=== Shutting down platform services ==="
  kill $GATEWAY_PID $COMPILER_PID $TESTING_PID 2>/dev/null || true
  exit 0
}
trap cleanup INT TERM

# Keep the script active to prevent background processes from detaching and leaking
while true; do
  sleep 1
done
