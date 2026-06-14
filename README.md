# IICPC-2026-BenchGrid: Distributed Benchmarking and Hosting Platform

An event-driven, microservice-based distributed evaluation platform designed to compile, isolate, benchmark, and score contestant-submitted matching engines under high concurrency. Built to scale to 100K concurrent viewers, 5K submitters, and 50K leaderboard contestants, the platform models a real-time trading competition environment with robust security controls and high-precision telemetry.

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
| **PostgreSQL** | Pod (EBS-backed) | `postgres-service:5432` in cluster |
| **Redis** | Pod (in-memory) | `redis-service:6379` in cluster |
| **S3 Storage** | AWS S3 | Bucket: `iicpc-benchgrid-submissions` |
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

---

## 4. Live Access URLs (Production)

| Service | URL | Credentials |
|---|---|---|
| **Contestant Dashboard / Dev Console** | `http://k8s-default-submissi-f6910ca3a8-2090885288.us-east-1.elb.amazonaws.com/dashboard` | `admin` / `Admin123!` |
| **Grafana Observability** | `http://k8s-monitori-grafanai-267ec7424a-1315873619.us-east-1.elb.amazonaws.com` | `admin` / `iicpc-admin-2026` |
| **API Base** | `http://k8s-default-submissi-f6910ca3a8-2090885288.us-east-1.elb.amazonaws.com/api/v1/` | — |

### Admin Credentials
| Role | Username | Email | Password |
|---|---|---|---|
| Admin | `admin` | `admin@iicpc.dev` | `Admin123!` |

---

## 5. Deployment Guide: Start-to-End

### Prerequisites
Ensure the following tools are installed and available on your system path:
- **Go** (1.22+ recommended)
- **Docker** & **Docker Compose**
- **Kubectl** + **AWS CLI** (configured with `us-east-1`)
- **Terraform** ≥ 1.5
- **Helm** ≥ 3.12
- **Kind** (for local Kubernetes mode only)
- **JQ** (JSON CLI Parser)

---

### Option A: Local Development Mode (Standalone)

This mode runs the persistent stateful components in Docker Compose and launches the Go microservices directly on the host. It is the fastest loop for debugging code changes.

#### Step 1: Start Databases & Infrastructure
Launch PostgreSQL, Redis, MinIO, Prometheus, and Grafana in the background:
```bash
docker compose up -d postgres redis minio prometheus grafana init-db
```
*Wait ~10 seconds for the databases to complete initialization and database schema migrations to run.*

#### Step 2: Compile and Launch Microservices
Start the gateway, compiler, and testing workers:
```bash
./scripts/start_dev_services.sh
```
This script compiles the Go source code, runs background threads for the services, and binds to host ports.

#### Step 3: Run Local Verifications
In a separate terminal, execute a smoke test submission or run the Go integration tests:
```bash
# Run a quick smoke test submission:
./scripts/local_smoke.sh go_optimized

# Run the full integration and database E2E suite:
./scripts/run_e2e_tests.sh
```

#### Step 4: Access Interfaces (Local Dev)
- **Contestant Arena / Developer Dashboard**: [http://localhost:3000](http://localhost:3000)
- **MinIO Console**: [http://localhost:9001](http://localhost:9001) (User: `minioadmin` / Pass: `minioadmin`)
- **Prometheus Dashboard**: [http://localhost:9090](http://localhost:9090)
- **Grafana Metrics**: [http://localhost:3001](http://localhost:3001) (User: `admin` / Pass: `admin`)

---

### Option B: Local Kubernetes Mode (Kind Cluster)

This mode deploys all microservice components inside a simulated multi-node Kubernetes cluster (Kind) with Horizontal Pod Autoscaling (HPA) enabled.

#### Step 1: Create a Kind Cluster with Docker Socket Sharing
We share `/var/run/docker.sock` to let the Kubernetes worker pods execute contestant sandbox builds.
Create the cluster using the config:
```bash
kind create cluster --name iicpc-cluster --config k8s/kind-config.yaml
```

#### Step 2: Run the Deployment Script
The deployment script builds the Go binaries for Linux, creates the Docker images, loads them into Kind, and applies the YAML manifests:
```bash
./scripts/deploy_k8s.sh
```

#### Step 3: Start Host-Level Monitoring Infrastructure
Prometheus, Grafana, and MinIO run on the host via Docker Compose so they are easily accessible and persistent:
```bash
docker compose up -d minio prometheus grafana
```

#### Step 4: Establish Kubernetes Port-Forwards
Because Kind runs on an isolated network, you must establish port forwards from your local host machine to the cluster services. Run these in the background:
```bash
# 1. Gateway & Frontend Web interface (maps to port 3002 on host)
kubectl port-forward svc/submission-gateway 3002:3000 &

# 2. Database (PostgreSQL - maps to port 5433 on host)
kubectl port-forward svc/postgres 5433:5432 &

# 3. Redis Broker (maps to port 6380 on host)
kubectl port-forward svc/redis 6380:6379 &

# 4. Service Metrics Scrapers (For Prometheus Host Scraping)
kubectl port-forward deployment/submission-gateway 9093:9093 &
kubectl port-forward deployment/compilation-worker 9091:9091 &
kubectl port-forward deployment/testing-worker 9092:9092 &
```

#### Step 5: Verify Deployment Status
You can run the Kubernetes verification script to ensure all API endpoints and database states are aligned:
```bash
python3 verify_k8s.py
```

#### Step 6: Access Interfaces (Kubernetes Mode)
- **Contestant Arena / Developer Dashboard**: [http://localhost:3002](http://localhost:3002)
- **Grafana Observability Dashboard**: [http://localhost:3001](http://localhost:3001) (Scraping metrics from forwards `:9091`, `:9092`, `:9093` via `host.docker.internal`)

---

### Option C: AWS EKS Production Deployment

Full cloud deployment on Amazon EKS with Terraform-managed infrastructure, ECR container registry, ALB ingress, and Cluster Autoscaler.

#### Prerequisites
```bash
# Authenticate with AWS
aws configure  # set region=us-east-1

# Authenticate Docker to ECR
aws ecr get-login-password --region us-east-1 | \
  docker login --username AWS --password-stdin \
  445711599575.dkr.ecr.us-east-1.amazonaws.com

# Update kubeconfig
aws eks update-kubeconfig --name iicpc-benchgrid --region us-east-1
```

#### Step 1: Provision Infrastructure (Terraform)
```bash
cd terraform
terraform init
terraform apply
```
This provisions: VPC, EKS cluster, 2 node groups (`core-workloads`, `sandbox-executions`), ECR repositories, IAM roles, and security groups.

#### Step 2: Bootstrap Cluster RBAC & Namespaces
```bash
kubectl apply -f build_k8s/eks-rbac.yaml
```

#### Step 3: Install Supporting Helm Charts
```bash
# AWS Load Balancer Controller
helm repo add eks https://aws.github.io/eks-charts
helm upgrade --install aws-load-balancer-controller eks/aws-load-balancer-controller \
  -n kube-system --set clusterName=iicpc-benchgrid

# kube-prometheus-stack (Prometheus + Grafana)
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm upgrade --install kube-prometheus-stack prometheus-community/kube-prometheus-stack \
  -n monitoring --create-namespace \
  -f k8s/prometheus-values.yaml

# Grafana ingress
kubectl apply -f k8s/grafana-ingress.yaml  # (already managed)
```

#### Step 4: Build & Push All Service Images
```bash
REGISTRY="445711599575.dkr.ecr.us-east-1.amazonaws.com"

for SERVICE in gateway compiler testing; do
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" \
    -o bin/$SERVICE ./services/$SERVICE
  docker buildx build --platform linux/amd64 -f Dockerfile.services \
    --build-arg SERVICE=$SERVICE -t "${REGISTRY}/iicpc-${SERVICE}:latest" --push .
done
```

#### Step 5: Deploy Application Manifests
```bash
# Core application
kubectl apply -f k8s/

# HPA (autoscaling)
kubectl apply -f k8s/hpa/compiler-hpa.yaml
kubectl apply -f k8s/hpa/testing-hpa.yaml

# Cluster Autoscaler
kubectl apply -f k8s/cluster-autoscaler.yaml
```

#### Step 6: Run Database Migrations
```bash
# Port-forward to postgres and apply migrations
kubectl port-forward svc/postgres-service 5433:5432 &
sleep 3
for f in migrations/*.sql; do
  psql "postgresql://iicpc:iicpc_secret@localhost:5433/iicpc_benchgrid" < "$f"
done
```

#### Step 7: Verify Deployment
```bash
# Check all pods are running
kubectl get pods -A

# Check HPA is reading metrics
kubectl get hpa

# Check ingress addresses
kubectl get ingress -A

# Check Cluster Autoscaler logs
kubectl logs -n kube-system -l app=cluster-autoscaler --tail=20
```

---

## 6. Observability & Monitoring

### Grafana Dashboards
Access Grafana at: `http://k8s-monitori-grafanai-267ec7424a-1315873619.us-east-1.elb.amazonaws.com`

| Dashboard | Description |
|---|---|
| **IICPC BenchGrid — Submission Pipeline** | Submission throughput, HPA replica counts, CPU%, sandbox pod count, pretest p50/p95 latency, node count |
| **Kubernetes / Compute Resources / Cluster** | Cluster-wide CPU and memory utilization by namespace |
| **Kubernetes / Compute Resources / Pod** | Per-pod resource usage for any deployment |
| **Kubernetes / Views / Global** | High-level cluster health view |
| **Node Exporter / Nodes** | Raw EC2 node CPU, memory, disk, and network metrics |
| **Kubernetes / Networking** | Pod and namespace network traffic |

### Prometheus Metrics Exposed
Each service exposes Prometheus metrics on a dedicated port:

| Service | Port | Key Metrics |
|---|---|---|
| `submission-gateway` | `:9093` | `iicpc_active_submissions`, HTTP request counts |
| `compilation-worker` | `:9091` | Build duration, queue lag |
| `testing-worker` | `:9092` | `iicpc_pretest_run_duration_seconds` (histogram), run success/failure |

### ServiceMonitors
The `kube-prometheus-stack` installation includes ServiceMonitors for all three IICPC services (`iicpc-compiler`, `iicpc-gateway`, `iicpc-testing`), automatically scraping metrics into Prometheus.

---

## 7. Troubleshooting Manual & Common Errors

### 1. Error: "port already exists" / "address already in use"
This happens when another process (like a stale standalone Go binary or a previously running container) is already bound to one of our target ports:
- Standalone Dev Gateway: `3000`
- Grafana: `3001`
- K8s Port-Forward Gateway: `3002`
- Postgres: `5432` (host/dev) or `5433` (k8s-forward)
- Redis: `6379` (host/dev) or `6380` (k8s-forward)
- Metrics Scrapers: `9090` (Prometheus), `9091`, `9092`, `9093`

#### Solution A: Identify and Kill Host Processes
Find the Process ID (PID) listening on the conflicting port and kill it:
```bash
# Find what is listening on port 3000 (replace 3000 with the conflicting port):
lsof -i :3000

# Kill the process (replace <PID> with the actual process ID returned):
kill -9 <PID>

# Alternatively, kill all running standalone service binaries:
killall gateway compiler testing 2>/dev/null || true
```

#### Solution B: Clean up Conflicting Docker Containers
If the port is occupied by a Docker container:
```bash
# List all running containers and search for the port:
docker ps --filter "publish=3000"

# Stop and remove the conflicting container:
docker stop <container_name_or_id>
docker rm <container_name_or_id>

# If Docker Compose is stuck, force-down and prune the networks:
docker compose down --remove-orphans
```

---

### 2. Error: "lost connection to pod" / "network namespace closed"
When running in Kubernetes mode, port-forward commands will terminate with `error: lost connection to pod` if a deployment rolls over, restarts, or autoscales (since the specific pod instance the tunnel was connected to gets deleted).

#### Solution: Kill Stale Port-Forwards and Re-Establish Tunnels
```bash
# 1. Kill any dangling or stale port-forward processes:
pkill -f "kubectl port-forward"

# 2. Re-run the port-forwarding scripts:
kubectl port-forward svc/submission-gateway 3002:3000 &
kubectl port-forward svc/postgres 5433:5432 &
kubectl port-forward svc/redis 6380:6379 &
kubectl port-forward deployment/submission-gateway 9093:9093 &
kubectl port-forward deployment/compilation-worker 9091:9091 &
kubectl port-forward deployment/testing-worker 9092:9092 &
```

---

### 3. Error: "unsupported Unicode escape sequence (22P05)" inside Postgres
Contestant container logs or compile errors can output raw binary characters or null bytes (`\x00`). If stored unsanitized, PostgreSQL throws this insertion failure.

#### Solution: Sanitize Inputs Before Database Writes
Ensure your service (specifically in `compiler/main.go` and `testing/main.go`) filters logs before writing to the database:
```go
// Clean string before serialization
sanitizedLogs := strings.ToValidUTF8(rawLogs, "")
sanitizedLogs = strings.ReplaceAll(sanitizedLogs, "\x00", "")
```

---

### 4. Error: Redpanda/Kafka "UNKNOWN_TOPIC_OR_PARTITION" during System Test
During post-contest system tests, the worker node bootstraps and attempts to fetch metadata before the Redpanda docker broker completes topic creation.

#### Solution: Pre-Create the Topic
Force topic creation directly inside the Redpanda container during startup:
```bash
docker exec -i iicpc-redpanda rpk topic create order-events -p 6
```

---

### 5. Error: HPA metrics show `<unknown>`
When running `kubectl get hpa`, target resource utilization displays `<unknown>`.

#### Solution: Deploy the Kubernetes Metrics Server
HPAs rely on the metrics-server API to fetch CPU/Memory utilization. If missing, deploy it and disable TLS check for EKS:
```bash
# Download and install metrics server:
kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml

# Patch the deployment to allow insecure TLS (required for EKS kubelet certs):
kubectl patch deployment metrics-server -n kube-system --type='json' \
  -p='[{"op": "add", "path": "/spec/template/spec/containers/0/args/-", "value": "--kubelet-insecure-tls"}]'
```

---

### 6. Error: Code updates are not reflected in Kubernetes
You modified the frontend layout or service logic, rebuilt the image, but the pod inside Kind is still serving the old version.

#### Solution (Kind): Load Local Image to Kind and Restart Deployment
Kind does not pull from the host Docker registry dynamically unless the image is explicitly pushed to the cluster nodes:
```bash
# 1. Rebuild the target service docker image:
docker build -f Dockerfile.services --build-arg SERVICE="gateway" -t iicpc-gateway:latest .

# 2. Force load the image into Kind cluster:
kind load docker-image iicpc-gateway:latest --name iicpc-cluster

# 3. Rollout restart the deployment to force Kubernetes to pull the new image:
kubectl rollout restart deployment/submission-gateway
```

#### Solution (EKS): Build, Push to ECR, and Rollout
```bash
REGISTRY="445711599575.dkr.ecr.us-east-1.amazonaws.com"
SERVICE="gateway"  # or: compiler, testing

# 1. Build binary for Linux AMD64
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" \
  -o bin/$SERVICE ./services/$SERVICE

# 2. Build and push Docker image to ECR
docker buildx build --platform linux/amd64 -f Dockerfile.services \
  --build-arg SERVICE=$SERVICE \
  -t "${REGISTRY}/iicpc-${SERVICE}:latest" --push .

# 3. Rollout restart
kubectl rollout restart deployment/submission-gateway  # adjust deployment name
kubectl rollout status deployment/submission-gateway --timeout=120s
```

---

### 7. Error (EKS): "gzip: invalid header" in Kaniko compilation logs
The submission upload was packaged as a `.zip` file. Kaniko expects a `tar.gz` context when pulling from S3.

#### Root Cause & Fix
The gateway's upload handler (`services/gateway/zip_normalize.go`) must convert the uploaded `.zip` to `tar.gz` before storing to S3. The `normalizeZipToTarGz` function handles this. Ensure the `Content-Type` header is set to `application/gzip` on the S3 `PutObject` call.

---

### 8. Error (EKS): Sandbox pod "already exists" during system tests
```json
{"error": "run 1 sandbox failed: failed to create sandbox pod: pods \"contestant-{id}-run-0\" already exists"}
```

#### Root Cause & Fix
After pretests complete, the sandbox pod enters `Terminating` state. If a systest runs the same submission before termination completes, pod creation fails. The testing worker (`services/testing/main.go`) now performs a **delete-before-create**: it force-deletes any existing pod with `gracePeriodSeconds=0` and waits up to 10 seconds for full removal before creating a fresh pod.

---

### 9. Error (EKS): HPA scales pods but they stay `Pending`
HPA scaled up replicas but new pods are stuck in `Pending` due to insufficient node capacity.

#### Root Cause
The Cluster Autoscaler (CA) is not installed, or the node group ASGs are not tagged for CA discovery.

#### Solution: Deploy Cluster Autoscaler
```bash
# Tag ASGs for CA discovery (replace ASG names with your actual names):
aws autoscaling create-or-update-tags --tags \
  "ResourceId=<core-asg-name>,ResourceType=auto-scaling-group,Key=k8s.io/cluster-autoscaler/enabled,Value=true,PropagateAtLaunch=false" \
  "ResourceId=<core-asg-name>,ResourceType=auto-scaling-group,Key=k8s.io/cluster-autoscaler/iicpc-benchgrid,Value=owned,PropagateAtLaunch=false"

# Deploy CA (IRSA role required for EKS credential injection):
kubectl apply -f k8s/cluster-autoscaler.yaml
```

The CA IRSA role (`iicpc-cluster-autoscaler-irsa`) is pre-configured in the repository with the correct OIDC trust policy and autoscaling permissions.

---

## 8. Key File Reference

| Path | Purpose |
|---|---|
| `services/gateway/` | HTTP gateway, submission handler, dashboard, zip→tar.gz normalization |
| `services/compiler/` | Kaniko build orchestrator, compilation queue consumer |
| `services/testing/` | Sandbox pod lifecycle, bot fleet runner, scoring |
| `services/common/` | Shared Prometheus metrics, proto definitions |
| `terraform/` | EKS cluster, node groups, VPC, ECR, IAM |
| `k8s/` | Kubernetes manifests (deployments, services, ingresses, HPAs, CA) |
| `k8s/hpa/` | HPA configs for compilation-worker and testing-worker |
| `k8s/cluster-autoscaler.yaml` | Cluster Autoscaler deployment with IRSA |
| `k8s/grafana-iicpc-dashboard.json` | Custom IICPC Grafana dashboard definition |
| `migrations/` | PostgreSQL schema migrations (applied in order) |
| `frontend/` | Contestant-facing leaderboard UI, admin console |
| `scripts/deploy_k8s.sh` | End-to-end EKS build + deploy script |
| `build_k8s/eks-rbac.yaml` | RBAC for Kaniko, sandbox pods, node roles |
| `Dockerfile.services` | Multi-service Dockerfile (uses pre-built `bin/$SERVICE` binary) |
