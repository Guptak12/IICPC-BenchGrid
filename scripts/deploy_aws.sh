#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

echo "=== 1. Checking Prerequisites & Context ==="
# Ensure aws cli and kubectl are present
if ! command -v aws &> /dev/null; then
  echo "Error: aws CLI is not installed."
  exit 1
fi

if ! command -v kubectl &> /dev/null; then
  echo "Error: kubectl is not installed."
  exit 1
fi

# Check AWS authentication status
echo "Checking AWS caller identity..."
aws sts get-caller-identity > /dev/null || {
  echo "Error: AWS CLI is not authenticated. Please run 'aws configure' first."
  exit 1
}

# Check Kubernetes cluster access
echo "Checking Kubernetes connection..."
kubectl cluster-info > /dev/null || {
  echo "Error: Unable to connect to Kubernetes cluster. Please ensure your EKS context is active."
  exit 1
}

echo "=== 2. Fetching Terraform Outputs ==="
if [ ! -d "terraform" ]; then
  echo "Error: terraform directory not found at $ROOT_DIR/terraform"
  exit 1
fi

cd terraform

echo "Running terraform output..."
GATEWAY_ROLE_ARN=$(terraform output -raw gateway_role_arn)
COMPILER_ROLE_ARN=$(terraform output -raw compiler_role_arn)
TESTING_ROLE_ARN=$(terraform output -raw testing_role_arn)
S3_BUCKET=$(terraform output -raw s3_bucket_name)
RDS_ENDPOINT=$(terraform output -raw rds_endpoint)
REDIS_ENDPOINT=$(terraform output -raw redis_endpoint)
REGISTRY_URL=$(terraform output -raw ecr_registry_url)
AWS_REGION=$(terraform output -raw aws_region)

cd "$ROOT_DIR"

echo "Using configuration:"
echo "  AWS Region:          $AWS_REGION"
echo "  ECR Registry URL:    $REGISTRY_URL"
echo "  S3 Submissions:      $S3_BUCKET"
echo "  RDS Endpoint:        $RDS_ENDPOINT"
echo "  Redis Endpoint:      $REDIS_ENDPOINT"
echo "  Gateway Pod Role:    $GATEWAY_ROLE_ARN"
echo "  Compiler Pod Role:   $COMPILER_ROLE_ARN"
echo "  Testing Pod Role:    $TESTING_ROLE_ARN"

echo "=== 3. Authenticating Docker with AWS ECR ==="
aws ecr get-login-password --region "$AWS_REGION" | docker login --username AWS --password-stdin "$REGISTRY_URL"

echo "=== 4. Compiling Microservices on Host (linux/amd64 for EKS nodes) ==="
SERVICES=("gateway" "compiler" "testing")

mkdir -p bin
for svc in "${SERVICES[@]}"; do
  echo "Compiling iicpc-${svc}..."
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o bin/${svc} ./services/${svc}
done

echo "=== 5. Building & Pushing Docker Images (linux/amd64 for EKS nodes) ==="
# IMPORTANT: Apple Silicon builds arm64 by default. Must cross-compile for linux/amd64 (EKS node arch).
for svc in "${SERVICES[@]}"; do
  echo "Building and pushing iicpc-${svc} to ECR..."
  docker buildx build --platform linux/amd64 -f Dockerfile.services --build-arg SERVICE="$svc" \
    -t "${REGISTRY_URL}/iicpc-${svc}:latest" --push .
done

echo "Building and pushing iicpc-init-db to ECR..."
docker buildx build --platform linux/amd64 -f Dockerfile.init-db \
  -t "${REGISTRY_URL}/iicpc-init-db:latest" --push .

echo "=== 6. Generating In-Cluster Manifests ==="
BUILD_K8S_DIR="build_k8s"
rm -rf "$BUILD_K8S_DIR"
mkdir -p "$BUILD_K8S_DIR"
cp k8s/*.yaml "$BUILD_K8S_DIR"/

# Dynamically replace ECR registry reference in all files.
# Source k8s/*.yaml use ACCOUNT_ID.dkr.ecr.AWS_REGION.amazonaws.com as placeholder.
for file in "$BUILD_K8S_DIR"/*.yaml; do
  sed -i '' "s|ACCOUNT_ID.dkr.ecr.AWS_REGION.amazonaws.com|${REGISTRY_URL}|g" "$file"
  # Always force re-pull on :latest tag so nodes never use stale cached images
  sed -i '' "s|imagePullPolicy: IfNotPresent|imagePullPolicy: Always|g" "$file"
done

# Replace IAM role ARNs in rbac configuration
sed -i '' "s|arn:aws:iam::123456789012:role/iicpc-benchgrid-gateway-pod-role|${GATEWAY_ROLE_ARN}|g" "$BUILD_K8S_DIR"/eks-rbac.yaml
sed -i '' "s|arn:aws:iam::123456789012:role/iicpc-benchgrid-compiler-pod-role|${COMPILER_ROLE_ARN}|g" "$BUILD_K8S_DIR"/eks-rbac.yaml
sed -i '' "s|arn:aws:iam::123456789012:role/iicpc-benchgrid-testing-pod-role|${TESTING_ROLE_ARN}|g" "$BUILD_K8S_DIR"/eks-rbac.yaml

# Replace variables in cluster ConfigMap
# NOTE: REDIS_ENDPOINT from Terraform is hostname-only; append :6379 for the Go redis client
sed -i '' "s|iicpc-benchgrid-cache.xxxxxx.0001.use1.cache.amazonaws.com:6379|${REDIS_ENDPOINT}:6379|g" "$BUILD_K8S_DIR"/eks-configmap.yaml
sed -i '' "s|iicpc-benchgrid-db.xxxxxx.us-east-1.rds.amazonaws.com:5432|${RDS_ENDPOINT}|g" "$BUILD_K8S_DIR"/eks-configmap.yaml
sed -i '' "s|iicpc-benchgrid-submissions-bucket|${S3_BUCKET}|g" "$BUILD_K8S_DIR"/eks-configmap.yaml

echo "=== 7. Deploying Core Cluster Resources ==="
kubectl apply -f "$BUILD_K8S_DIR"/eks-rbac.yaml
kubectl apply -f "$BUILD_K8S_DIR"/eks-configmap.yaml
kubectl apply -f "$BUILD_K8S_DIR"/volume.yaml
kubectl apply -f "$BUILD_K8S_DIR"/redpanda.yaml

echo "=== 8. Waiting for Redpanda Telemetry Broker ==="
kubectl rollout status statefulset/redpanda --timeout=120s

echo "=== 9. Running Database Migrations Job ==="
kubectl delete configmap migrations-config --ignore-not-found
kubectl create configmap migrations-config --from-file=migrations/

kubectl delete job iicpc-migration-job --ignore-not-found
kubectl apply -f "$BUILD_K8S_DIR"/migration-job.yaml

echo "Waiting for migrations to complete..."
kubectl wait --for=condition=complete --timeout=90s job/iicpc-migration-job

echo "=== 10. Deploying Services & Ingress ==="
kubectl apply -f "$BUILD_K8S_DIR"/sandbox-networkpolicy.yaml
kubectl apply -f "$BUILD_K8S_DIR"/compiler.yaml
kubectl apply -f "$BUILD_K8S_DIR"/testing.yaml
kubectl apply -f "$BUILD_K8S_DIR"/gateway.yaml
# NOTE: leaderboard-generator is not yet deployed (no source code in services/leaderboard/)
# Uncomment when the leaderboard service is implemented:
# kubectl apply -f "$BUILD_K8S_DIR"/leaderboard.yaml

echo "=== 11. Deployment Complete! Checking Status ==="
kubectl get pods -o wide
kubectl get ingress -o wide
