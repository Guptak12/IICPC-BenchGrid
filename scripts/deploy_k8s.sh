#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

echo "=== 1. Detecting Kubernetes Context ==="
CONTEXT=$(kubectl config current-context || echo "")
if [ -z "$CONTEXT" ]; then
  echo "Error: No active Kubernetes context found."
  exit 1
fi
echo "Active context: $CONTEXT"

# Determine cluster provider (kind, minikube, or generic)
PROVIDER="generic"
if [[ "$CONTEXT" =~ ^kind- ]]; then
  PROVIDER="kind"
  CLUSTER_NAME="${CONTEXT#kind-}"
  echo "Detected Kind cluster: $CLUSTER_NAME"
elif [[ "$CONTEXT" == "minikube" ]]; then
  PROVIDER="minikube"
  echo "Detected Minikube cluster"
fi

echo "=== 2. Building Docker Images ==="
# Setup isolated docker and kubeconfig contexts
export DOCKER_HOST="unix:///var/run/docker.sock"
export HOME="/tmp/empty-home-for-docker"
mkdir -p "$HOME"

# Build contestant sandbox image
echo "Building sandbox image..."
docker build -f Dockerfile.sandbox -t iicpc-sandbox:v1 .

# Build microservices
SERVICES=("gateway" "compiler" "pretest" "leaderboard")

echo "Compiling microservices on host..."
mkdir -p bin
for svc in "${SERVICES[@]}"; do
  CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o bin/${svc} services/${svc}/*.go
done

for svc in "${SERVICES[@]}"; do
  echo "Building iicpc-${svc}:latest..."
  docker build -f Dockerfile.services --build-arg SERVICE="$svc" -t "iicpc-${svc}:latest" .
done

echo "=== 3. Loading Images into Kubernetes Cluster ==="
if [ "$PROVIDER" = "kind" ]; then
  echo "Loading images into Kind cluster '$CLUSTER_NAME'..."
  kind load docker-image iicpc-sandbox:v1 --name "$CLUSTER_NAME"
  for svc in "${SERVICES[@]}"; do
    kind load docker-image "iicpc-${svc}:latest" --name "$CLUSTER_NAME"
  done
elif [ "$PROVIDER" = "minikube" ]; then
  echo "Loading images into Minikube..."
  minikube image load iicpc-sandbox:v1
  for svc in "${SERVICES[@]}"; do
    minikube image load "iicpc-${svc}:latest"
  done
else
  echo "Generic Kubernetes context. Assuming registry pushing or local node availability."
fi

echo "=== 4. Applying Core Volumes & Services ==="
kubectl apply -f k8s/volume.yaml
kubectl apply -f k8s/postgres.yaml
kubectl apply -f k8s/redis.yaml

echo "=== 5. Waiting for Postgres Database to be Healthy ==="
kubectl rollout status deployment/postgres --timeout=90s

echo "=== 6. Deploying Migration ConfigMap & running Job ==="
# Recreate configmap to ensure fresh migrations are copied
kubectl delete configmap migrations-config --ignore-not-found
kubectl create configmap migrations-config --from-file=migrations/

# Run migration job (delete old one if exists)
kubectl delete job iicpc-migration-job --ignore-not-found
kubectl apply -f k8s/migration-job.yaml

echo "Waiting for migration job to complete..."
kubectl wait --for=condition=complete --timeout=60s job/iicpc-migration-job

echo "=== 7. Deploying Microservice Workers and Gateway ==="
kubectl apply -f k8s/compiler.yaml
kubectl apply -f k8s/pretest.yaml
kubectl apply -f k8s/leaderboard.yaml
kubectl apply -f k8s/gateway.yaml

echo "=== 8. Deploying Horizontal Pod Autoscalers (HPAs) ==="
kubectl apply -f k8s/hpa/compiler-hpa.yaml
kubectl apply -f k8s/hpa/pretest-hpa.yaml

echo "=== 9. Deployment Complete! Checking Status ==="
kubectl get pods -o wide
