# IICPC-2026-BenchGrid: Distributed Benchmarking and Hosting Platform

An event-driven, microservice-based distributed evaluation platform designed to compile, isolate, benchmark, and score contestant-submitted matching engines under high concurrency. Built to scale to 100K concurrent viewers, 5K submitters, and 50K leaderboard contestants, the platform models a real-time trading competition environment with robust security controls and high-precision telemetry.

---

## 🔗 Live Deployment (Production)

| Service | URL | Credentials |
|---|---|---|
| **Dev Console / Dashboard** | [`http://k8s-default-submissi-f6910ca3a8-2090885288.us-east-1.elb.amazonaws.com/dashboard`](http://k8s-default-submissi-f6910ca3a8-2090885288.us-east-1.elb.amazonaws.com/dashboard) | `admin` / `Admin123!` |
| **Grafana Observability** | [`http://k8s-monitori-grafanai-267ec7424a-1315873619.us-east-1.elb.amazonaws.com`](http://k8s-monitori-grafanai-267ec7424a-1315873619.us-east-1.elb.amazonaws.com) | `admin` / `iicpc-admin-2026` |
| **API Base URL** | [`http://k8s-default-submissi-f6910ca3a8-2090885288.us-east-1.elb.amazonaws.com/api/v1/`](http://k8s-default-submissi-f6910ca3a8-2090885288.us-east-1.elb.amazonaws.com/api/v1/) | — |

### Admin Credentials
| Role | Username | Email | Password |
|---|---|---|---|
| Admin | `admin` | `admin@iicpc.dev` | `Admin123!` |
| Grafana Admin | `admin` | — | `iicpc-admin-2026` |

---

## 1. System Architecture

The platform uses a completely decoupled, event-driven, and highly resilient microservices pipeline connected via **Redis Streams**, backed by **PostgreSQL**, **S3-compatible Object Storage (MinIO / AWS S3)**, and **Apache Kafka (Redpanda)**.

```mermaid
flowchart TD
    Client[Contestant Submission] -->|HTTP POST /submit| Gateway[Submission Gateway]
    Gateway -->|Rate limit check| RedisRL[(Redis Rate Limit TTL)]
    Gateway -->|Save source code| S3[S3 Object Storage: tar.gz via Kaniko]
    Gateway -->|Save submission meta| PG[(PostgreSQL)]
    Gateway -->|Publish compilation job| R1[Redis Stream: compilation_queue]

    subgraph Compiler Service
        R1 --> Compiler[Compiler Worker]
        Compiler -->|Download tar.gz| S3
        Compiler -->|Kaniko in-cluster build| KanikoJob[Kaniko Pod: Dynamic Build]
        KanikoJob -->|Push image contestant-ID| ECR[AWS ECR]
        Compiler -->|Publish testing job| R2[Redis Stream: testing_queue]
    end

    subgraph Testing Service
        R2 --> Testing[Testing Worker]
        Testing -->|Instantiate sandboxes K=3| SandboxPod[Contestant Pod: iicpc-sandboxes ns]
        Testing -->|Run bot fleet in parallel| Shadow[Shadow Book Validator]
        Shadow <-->|TCP / Protobuf| SandboxPod
        Testing -->|Save score & verdict| PG
    end

    subgraph Leaderboard Generation
        PG -->|Query rankings every 3s| Standings[Leaderboard Generator]
        Standings -->|Write JSON Standing| staticLeaderboard[/frontend/leaderboard.json/]
        Standings -->|SSE Broadcaster| LeaderboardStream[SSE Stream]
    end

    subgraph Observability
        Gateway -->|Prometheus metrics :9093| Prom[Prometheus]
        Compiler -->|Prometheus metrics :9091| Prom
        Testing -->|Prometheus metrics :9092| Prom
        Prom --> Grafana[Grafana Dashboards]
    end

    Gateway -->|Serve JSON / Leaderboard| staticLeaderboard
    ClientViewer[Contestant Browser Standing UI] -->|Listen SSE / WebSockets| Gateway
```

### Subsystems and Decompositions
* **Submission Gateway** (`services/gateway/`): Stateless Fiber web server handling submission uploads, rate limiting, and dashboard UI telemetry. Intercepts requests for `leaderboard.json` and reads standings from the configured volume path.
* **Compiler Service** (`services/compiler/`): Event loop polling `compilation_queue` stream, executing isolated Kaniko in-cluster builds from user-submitted `tar.gz` archives under strict timeouts. Pushes built images to AWS ECR.
* **Testing Service** (`services/testing/`): Ephemeral sandbox runner instantiating contestant Kubernetes pods in the `iicpc-sandboxes` namespace (on dedicated `sandbox-executions` node group) in parallel ($K=3$ runs), executing trade-matching bots via raw TCP using little-endian length-prefixed Protobuf messages, and verifying correctness in real-time.
* **Developer Diagnostics Console**: Accessible directly at `/dashboard` to inspect live container instances, view active queue depths, run automated mock pretest/systest submissions, and inspect contestant code/telemetry drawers.

---

## 2. Secure Sandboxing & Network Model

### Local / Kind Mode (Docker)
```
       +-----------------------------------------------+
       |                   Host OS                     |
       |  +-----------------------------------------+  |
       |  |          gVisor (runsc) Sandbox         |  |
       |  |  +-----------------------------------+  |  |
       |  |  |       Contestant Container        |  |  |
       |  |  |  [cgroups: 1 CPU, 256MB RAM]      |  |  |
       |  |  |  [NetworkPolicy: Egress Deny]     |  |  |
       |  |  |  [seccomp: blocked fork/vfork]    |  |  |
       |  |  |  [Protocol: TCP / Protobuf]       |  |  |
       |  |  |  Bind: Port 8000                  |  |  |
       |  |  +-----------------------------------+  |  |
       |  +-------------------|---------------------+  |
       |                      | (Dynamic Host Port Mapped)
       |                      v
       |           tcp://127.0.0.1:{random}
       +-----------------------------------------------+
```

### Production / EKS Mode (Kubernetes Pods)
Contestant sandboxes run as isolated Kubernetes pods in the dedicated `iicpc-sandboxes` namespace on the `sandbox-executions` node group (tainted `sandbox-only=true:NoSchedule`):

```
+--------------------------------------------------------------+
|  EKS Node Group: sandbox-executions (tainted)               |
|                                                              |
|  +--------------------------------------------------------+  |
|  |  Pod: contestant-{submissionID}-run-{N}               |  |
|  |  Namespace: iicpc-sandboxes                           |  |
|  |  [runAsNonRoot: true, runAsUser: 10001]               |  |
|  |  [AllowPrivilegeEscalation: false]                    |  |
|  |  [Capabilities: DROP ALL]                             |  |
|  |  [CPU: 1 req / 2 limit, Memory: 256Mi req / 512Mi]   |  |
|  |  Port: 8000                                           |  |
|  +--------------------------------------------------------+  |
+--------------------------------------------------------------+
```

Security properties:
1. **Non-root execution**: `runAsUser: 10001`, `runAsNonRoot: true`
2. **Capability drop**: `DROP ALL` Linux capabilities
3. **No privilege escalation**: `AllowPrivilegeEscalation: false`
4. **Dedicated node group**: Tainted node group prevents system pods from co-scheduling
5. **Pod cleanup**: Force-deleted with `gracePeriodSeconds=0` after each run; defensive delete-before-create prevents "already exists" conflicts between pretest and systest phases

---

## 3. Infrastructure Overview (AWS EKS Production)

### Architecture
| Component | Type | Details |
|---|---|---|
| **EKS Cluster** | Kubernetes 1.35 | `iicpc-benchgrid`, `us-east-1` |
| **Core Node Group** | `t3.medium` ×2–8 | `core-workloads`, runs gateway/workers/monitoring |
| **Sandbox Node Group** | `t3.medium` ×1–5 | `sandbox-executions`, tainted, contestant pods only |
| **PostgreSQL** | AWS RDS (private subnet) | `iicpc-benchgrid-db.cepaasao0lur.us-east-1.rds.amazonaws.com:5432` |
| **Redis** | AWS ElastiCache | `iicpc-benchgrid-cache.*.use1.cache.amazonaws.com:6379` |
| **S3 Storage** | AWS S3 | Bucket: `iicpc-benchgrid-submissions-bucket` |
| **Container Registry** | AWS ECR | `445711599575.dkr.ecr.us-east-1.amazonaws.com` |
| **Load Balancer** | AWS ALB (Ingress) | Via `aws-load-balancer-controller` |

### Autoscaling
| Component | Type | Min | Max | Trigger |
|---|---|---|---|---|
| `compilation-worker` | HPA (CPU) | 1 | 20 | 60% CPU avg |
| `testing-worker` | HPA (CPU) | 1 | 20 | 80% CPU avg |
| `core-workloads` ASG | Cluster Autoscaler | 1 | 8 | Pending pods |
| `sandbox-executions` ASG | Cluster Autoscaler | 1 | 5 | Pending pods |

HPA scale-down stabilization: **30 seconds** (fast cooldown after submission bursts).

### Load Configuration
| Test Type | Bots | Orders/Bot | Total Orders |
|---|---|---|---|
| **Pretest** | 50 | 200 | 10,000 |
| **System Test** | 500 | 2,000 | **1,000,000** |

Configurable via `SYSTEST_NUM_BOTS` / `SYSTEST_ORDERS_PER_BOT` env vars in the `iicpc-config` ConfigMap.

---

## 4. Setup & Deployment Guide

### Prerequisites

Ensure the following tools are installed:

```bash
# macOS (Homebrew)
brew install go docker kubectl helm kind terraform jq

# Verify versions
go version          # 1.22+
docker --version
kubectl version --client
helm version        # 3.12+
kind version        # 0.20+
terraform version   # 1.5+
```

---

### Option A: Local Development Mode (Standalone Go + Docker)

Fastest iteration loop — stateful services in Docker, Go binaries run on host.

```bash
# 1. Start databases and infrastructure
docker compose up -d postgres redis minio prometheus grafana init-db

# Wait ~10s for DB migrations to complete
sleep 10

# 2. Start all microservices
./scripts/start_dev_services.sh

# 3. Smoke test (in a new terminal)
./scripts/local_smoke.sh go_optimized

# 4. Full E2E suite
./scripts/run_e2e_tests.sh
```

**Access:**
| Service | URL | Credentials |
|---|---|---|
| Dev Console / Dashboard | http://localhost:3000/dashboard | `admin` / `Admin123!` |
| API | http://localhost:3000/api/v1/ | — |
| MinIO Console | http://localhost:9001 | `minioadmin` / `minioadmin` |
| Prometheus | http://localhost:9090 | — |
| Grafana | http://localhost:3001 | `admin` / `admin` |

---

### Option B: Local Kubernetes Mode (Kind Cluster)

Runs everything inside a local multi-node Kind cluster — mirrors production topology.

#### Step 1 — Install prerequisites
```bash
# Install Kind if not already installed
brew install kind

# Verify Docker is running
docker info
```

#### Step 2 — Create the Kind cluster
```bash
kind create cluster --name iicpc-cluster --config k8s/kind-config.yaml
```

#### Step 3 — Build images and deploy to cluster
```bash
# One command: compiles Go binaries, builds Docker images,
# loads them into Kind, and applies all K8s manifests
./scripts/deploy_k8s.sh
```

> This script automatically detects the Kind context and loads images directly — no registry push needed.

#### Step 4 — Apply the local configmap
```bash
kubectl apply -f k8s/configmap.yaml
```

#### Step 5 — Start host-level MinIO (S3 storage)
```bash
docker compose up -d minio
```

#### Step 6 — Establish port-forwards to localhost

Open a new terminal and run all port-forwards:

```bash
# Kill any stale port-forwards first
pkill -f "kubectl port-forward" 2>/dev/null || true

# Gateway & Frontend (localhost:3002)
kubectl port-forward svc/submission-gateway 3002:3000 &

# PostgreSQL (localhost:5433)
kubectl port-forward svc/postgres 5433:5432 &

# Redis (localhost:6380)
kubectl port-forward svc/redis 6380:6379 &

# Prometheus metrics scrape ports
kubectl port-forward deployment/submission-gateway 9093:9093 &
kubectl port-forward deployment/compilation-worker 9091:9091 &
kubectl port-forward deployment/testing-worker    9092:9092 &
```

#### Step 7 — Verify deployment
```bash
# All pods should be Running
kubectl get pods -A

# Check HPA is registered
kubectl get hpa

# Run verification script
python3 verify_k8s.py
```

**Access (Kind mode):**
| Service | URL | Credentials |
|---|---|---|
| Dev Console / Dashboard | http://localhost:3002/dashboard | `admin` / `Admin123!` |
| API | http://localhost:3002/api/v1/ | — |
| Grafana | http://localhost:3001 | `admin` / `admin` |
| Prometheus | http://localhost:9090 | — |
| MinIO Console | http://localhost:9001 | `minioadmin` / `minioadmin` |

#### Rebuild a single service (Kind)
```bash
SERVICE=gateway  # or: compiler, testing

# Recompile
CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o bin/$SERVICE ./services/$SERVICE

# Rebuild image
docker build -f Dockerfile.services --build-arg SERVICE=$SERVICE -t iicpc-$SERVICE:latest .

# Load into Kind
kind load docker-image iicpc-$SERVICE:latest --name iicpc-cluster

# Rolling restart
kubectl rollout restart deployment/submission-gateway  # adjust name
kubectl rollout status  deployment/submission-gateway --timeout=60s
```

---

### Option C: AWS EKS Production Deployment

Full cloud deployment on Amazon EKS with Terraform-managed infrastructure.

#### Prerequisites — AWS Authentication
```bash
# Configure AWS CLI (region: us-east-1)
aws configure

# Authenticate Docker to ECR
aws ecr get-login-password --region us-east-1 | \
  docker login --username AWS --password-stdin \
  445711599575.dkr.ecr.us-east-1.amazonaws.com

# Connect kubectl to the EKS cluster
aws eks update-kubeconfig --name iicpc-benchgrid --region us-east-1

# Verify connection
kubectl cluster-info
kubectl get nodes
```

#### Step 1 — Provision Infrastructure (Terraform)
```bash
cd terraform
terraform init
terraform plan    # review the plan
terraform apply   # provisions VPC, EKS, RDS, ElastiCache, ECR, IAM
cd ..
```

#### Step 2 — Bootstrap RBAC & Namespaces
```bash
kubectl apply -f build_k8s/eks-rbac.yaml
```

#### Step 3 — Install Helm dependencies
```bash
# AWS Load Balancer Controller (routes ALB ingress)
helm repo add eks https://aws.github.io/eks-charts
helm repo update
helm upgrade --install aws-load-balancer-controller eks/aws-load-balancer-controller \
  -n kube-system \
  --set clusterName=iicpc-benchgrid

# Metrics Server (required for HPA)
kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml
kubectl patch deployment metrics-server -n kube-system --type='json' \
  -p='[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]'
```

#### Step 4 — Install Prometheus + Grafana
```bash
# One-command monitoring stack deploy
./scripts/deploy_monitoring.sh
```
> Installs `kube-prometheus-stack` with IICPC ServiceMonitors, Grafana ALB ingress, and persistent EBS storage.

#### Step 5 — Build & Push Service Images to ECR
```bash
# Full build + push (all 3 services)
REGISTRY="445711599575.dkr.ecr.us-east-1.amazonaws.com"

for SERVICE in gateway compiler testing; do
  echo "=== Building $SERVICE ==="
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" \
    -o bin/$SERVICE ./services/$SERVICE
  docker buildx build --platform linux/amd64 -f Dockerfile.services \
    --build-arg SERVICE=$SERVICE \
    -t "${REGISTRY}/iicpc-${SERVICE}:latest" --push .
done
```

#### Step 6 — Deploy to EKS
```bash
# Automated full-platform deploy (reads Terraform outputs automatically)
./scripts/deploy_aws.sh
```

> **What `deploy_aws.sh` does:**
> 1. Reads live Terraform outputs (RDS endpoint, Redis endpoint, ECR URL, IAM roles)
> 2. Patches the live ConfigMap with actual values
> 3. Applies all K8s manifests (`k8s/`)
> 4. Deploys HPAs and Cluster Autoscaler
> 5. Runs database migrations via a one-off Job
> 6. Verifies pod readiness

#### Step 7 — Manual Apply (if needed)
```bash
# Apply all manifests individually
kubectl apply -f k8s/eks-configmap.yaml
kubectl apply -f k8s/gateway.yaml
kubectl apply -f k8s/compiler.yaml
kubectl apply -f k8s/testing.yaml
kubectl apply -f k8s/postgres.yaml      # if using in-cluster DB
kubectl apply -f k8s/redis.yaml
kubectl apply -f k8s/redpanda.yaml
kubectl apply -f k8s/leaderboard.yaml
kubectl apply -f k8s/volume.yaml
kubectl apply -f k8s/hpa/compiler-hpa.yaml
kubectl apply -f k8s/hpa/testing-hpa.yaml
kubectl apply -f k8s/cluster-autoscaler.yaml
kubectl apply -f k8s/sandbox-networkpolicy.yaml
```

#### Step 8 — Verify Deployment
```bash
# All pods healthy
kubectl get pods -A

# HPA reading metrics
kubectl get hpa

# Ingress addresses (ALB URLs)
kubectl get ingress -A

# Cluster Autoscaler
kubectl logs -n kube-system -l app=cluster-autoscaler --tail=20

# Check service endpoints
kubectl get svc
```

#### Rebuild & Redeploy a Single Service (EKS)
```bash
REGISTRY="445711599575.dkr.ecr.us-east-1.amazonaws.com"
SERVICE="gateway"   # or: compiler, testing

# 1. Build binary
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" \
  -o bin/$SERVICE ./services/$SERVICE

# 2. Build & push to ECR
docker buildx build --platform linux/amd64 -f Dockerfile.services \
  --build-arg SERVICE=$SERVICE \
  -t "${REGISTRY}/iicpc-${SERVICE}:latest" --push .

# 3. Rolling restart
kubectl rollout restart deployment/submission-gateway  # adjust name
kubectl rollout status  deployment/submission-gateway --timeout=120s
```

---

## 5. Observability & Monitoring

### Grafana Dashboards
**Live URL:** [`http://k8s-monitori-grafanai-267ec7424a-1315873619.us-east-1.elb.amazonaws.com`](http://k8s-monitori-grafanai-267ec7424a-1315873619.us-east-1.elb.amazonaws.com)

| Dashboard | Description |
|---|---|
| **IICPC BenchGrid — Submission Pipeline** | TPS, p50/p90/p99 latency, fleet TPS/correctness, queue depths, HPA replicas, CPU%, DB pool |
| **Kubernetes / Compute Resources / Cluster** | Cluster-wide CPU and memory utilization by namespace |
| **Kubernetes / Compute Resources / Pod** | Per-pod resource usage for any deployment |
| **Node Exporter / Nodes** | Raw EC2 node CPU, memory, disk, and network metrics |
| **Kubernetes / Networking** | Pod and namespace network traffic |

### Prometheus Metrics Exposed
Each service exposes Prometheus metrics on a dedicated port:

| Service | Port | Key Metrics |
|---|---|---|
| `submission-gateway` | `:9093` | `iicpc_active_submissions`, `iicpc_http_requests_total`, `iicpc_http_request_duration_seconds` |
| `compilation-worker` | `:9091` | `iicpc_queue_depth{queue=compilation_queue}`, `iicpc_db_pool_active_connections` |
| `testing-worker` | `:9092` | `iicpc_pretest_run_duration_seconds`, `iicpc_fleet_tps`, `iicpc_fleet_p99_us`, `iicpc_fleet_correctness` |

### ServiceMonitors (automatic scraping)
```bash
# Verify Prometheus is scraping IICPC targets
kubectl get servicemonitor -n monitoring | grep iicpc
# iicpc-gateway, iicpc-compiler, iicpc-testing
```

---

## 6. Troubleshooting Manual

### 1. Port already in use
```bash
# Find the conflicting process
lsof -i :3000

# Kill it
kill -9 <PID>

# Or kill all service binaries
killall gateway compiler testing 2>/dev/null || true
```

### 2. Lost connection to pod (port-forward died)
Happens when a pod restarts or HPA rolls pods. Re-establish:
```bash
pkill -f "kubectl port-forward"

kubectl port-forward svc/submission-gateway 3002:3000 &
kubectl port-forward svc/postgres 5433:5432 &
kubectl port-forward svc/redis 6380:6379 &
kubectl port-forward deployment/submission-gateway 9093:9093 &
kubectl port-forward deployment/compilation-worker 9091:9091 &
kubectl port-forward deployment/testing-worker    9092:9092 &
```

### 3. Submissions stuck in `running`/`building`
Worker pods killed mid-job (e.g. during a rollout) leave orphaned DB records. Fix manually:
```bash
kubectl run pg-client --image=postgres:15-alpine --restart=Never --rm -i \
  --env="PGPASSWORD=iicpc_secret_production" \
  -- psql "postgres://iicpc@iicpc-benchgrid-db.cepaasao0lur.us-east-1.rds.amazonaws.com:5432/iicpc_db" -c "
UPDATE submissions
SET status='failed', verdict='System Error', updated_at=NOW()
WHERE status IN ('running','building','compiling','pending')
  AND updated_at < NOW() - INTERVAL '10 minutes';
"
```

### 4. HPA shows `<unknown>` metrics
```bash
# Deploy metrics-server
kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml
kubectl patch deployment metrics-server -n kube-system --type='json' \
  -p='[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]'
```

### 5. Kaniko: "gzip: invalid header"
The submission was uploaded as a `.zip` file. The gateway's `zip_normalize.go` converts it to `tar.gz` automatically. If this error appears, verify the submission upload went through the `/api/v1/submit` endpoint (not direct S3 upload).

### 6. Sandbox pod "already exists"
```json
{"error": "run 1 sandbox failed: failed to create sandbox pod: pods \"contestant-{id}-run-0\" already exists"}
```
The testing worker (`services/testing/main.go`) performs delete-before-create with a 10s wait. If this persists, manually delete:
```bash
kubectl delete pod contestant-<submission-id>-run-0 -n iicpc-sandboxes --grace-period=0 --force
```

### 7. Grafana metrics show many lines / overpopulated
This happens when:
- HPA scaled to many replicas during a load test — stale pod series remain visible for ~5 min then auto-disappear
- All pods report all metrics globally — dashboard uses `sum()`/`max()` aggregation + pod selector filters to show clean single lines

### 8. Code changes not reflected in Kind
```bash
docker build -f Dockerfile.services --build-arg SERVICE=gateway -t iicpc-gateway:latest .
kind load docker-image iicpc-gateway:latest --name iicpc-cluster
kubectl rollout restart deployment/submission-gateway
```

---

## 7. Key File Reference

| Path | Purpose |
|---|---|
| `services/gateway/` | HTTP gateway, submission handler, dashboard, zip→tar.gz normalization |
| `services/compiler/` | Kaniko build orchestrator, compilation queue consumer |
| `services/testing/` | Sandbox pod lifecycle, bot fleet runner, scoring |
| `services/common/` | Shared Prometheus metrics, Redis helpers, proto definitions |
| `bot-fleet/` | Distributed bot fleet runner (MMPP scheduler, order protocol) |
| `terraform/` | EKS cluster, node groups, VPC, ECR, IAM, RDS, ElastiCache |
| `k8s/` | Kubernetes manifests (deployments, services, ingresses) |
| `k8s/hpa/` | HPA configs for compilation-worker and testing-worker |
| `k8s/eks-configmap.yaml` | EKS environment config (DB, Redis, S3, load params) |
| `k8s/grafana-iicpc-dashboard.json` | Custom IICPC Grafana dashboard JSON |
| `k8s/cluster-autoscaler.yaml` | Cluster Autoscaler deployment with IRSA |
| `migrations/` | PostgreSQL schema migrations (applied in order) |
| `scripts/deploy_k8s.sh` | Local Kind: build images + deploy to cluster |
| `scripts/deploy_aws.sh` | EKS: full end-to-end build + deploy (reads Terraform outputs) |
| `scripts/deploy_monitoring.sh` | EKS: installs kube-prometheus-stack + Grafana ALB |
| `scripts/start_dev_services.sh` | Standalone: compile & launch Go services locally |
| `scripts/local_smoke.sh` | Quick smoke test submission |
| `scripts/run_e2e_tests.sh` | Full integration + E2E test suite |
| `Dockerfile.services` | Multi-service Dockerfile (uses pre-built `bin/$SERVICE` binary) |
| `Dockerfile.init-db` | One-shot DB migration runner |
| `build_k8s/eks-rbac.yaml` | RBAC for Kaniko, sandbox pods, node roles |

---

## ⚡ Quick Deploy Reference

### Local (Kind) — One Command
```bash
kind create cluster --name iicpc-cluster --config k8s/kind-config.yaml && \
./scripts/deploy_k8s.sh && \
docker compose up -d minio && \
kubectl port-forward svc/submission-gateway 3002:3000 &
# → http://localhost:3002/dashboard
```

### EKS (Production) — Full Deploy
```bash
# 1. Provision infra
cd terraform && terraform apply && cd ..

# 2. Connect kubectl
aws eks update-kubeconfig --name iicpc-benchgrid --region us-east-1

# 3. Deploy everything (monitoring + app)
./scripts/deploy_monitoring.sh
./scripts/deploy_aws.sh

# → http://k8s-default-submissi-f6910ca3a8-2090885288.us-east-1.elb.amazonaws.com/dashboard
```

### EKS — Redeploy a Single Service
```bash
SERVICE=gateway   # gateway | compiler | testing
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o bin/$SERVICE ./services/$SERVICE
docker buildx build --platform linux/amd64 -f Dockerfile.services \
  --build-arg SERVICE=$SERVICE \
  -t "445711599575.dkr.ecr.us-east-1.amazonaws.com/iicpc-${SERVICE}:latest" --push .
kubectl rollout restart deployment/submission-gateway   # adjust name
kubectl rollout status  deployment/submission-gateway --timeout=120s
```
