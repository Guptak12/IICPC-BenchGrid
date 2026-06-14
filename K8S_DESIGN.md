# IICPC-BenchGrid — Kubernetes Design Document

> Every manifest, every critical decision, every trade-off — grounded in the actual
> files under `k8s/` and the service code that drives them.

---

## 1. Cluster Topology

### 1.1 Two-Environment Strategy: Kind (local) → EKS (production)

The system runs on two different cluster types with identical manifests where possible.

```
┌─────────────────────────────────────────────────────────────────┐
│  LOCAL (Kind)                   PRODUCTION (AWS EKS)            │
│  ─────────────────────          ────────────────────────        │
│  kind-config.yaml               eksctl / Terraform              │
│  docker sock mount              Managed nodegroups              │
│  kind load docker-image         ECR registry + IRSA             │
│  MinIO (docker host)            S3 + ElastiCache + RDS          │
│  calico CNI (injected)          VPC CNI (built-in)              │
│  configmap.yaml                 eks-configmap.yaml              │
└─────────────────────────────────────────────────────────────────┘
```

**Why Kind for local development**: Kind (Kubernetes in Docker) runs a full
Kubernetes control plane inside a Docker container. This allows developers to
test the exact same `kubectl apply` manifests locally without needing a cloud
account. The critical limitation is that Kind uses the host Docker daemon for
its own bootstrap — not for workload containers. Container images must be
explicitly loaded into the cluster with `kind load docker-image`.

**Why EKS for production**: EKS provides managed nodegroups, native IRSA
(IAM Roles for Service Accounts), VPC CNI with native pod networking,
and the AWS Load Balancer Controller for internet-facing Ingress. These are
all prerequisites for the production security model.

---

### 1.2 Kind Cluster Configuration

**File**: [`k8s/kind-config.yaml`](k8s/kind-config.yaml)

```yaml
nodes:
- role: control-plane
  extraMounts:
  - hostPath: /var/run/docker.sock
    containerPath: /var/run/docker.sock
```

**Critical decision — Docker socket mount**: The Kind control-plane node mounts
the host Docker socket. This enables the testing worker (running inside Kind) to
invoke Docker CLI commands to start contestant sandbox containers on the **host**
Docker daemon rather than inside Kubernetes. This is the Docker-out-of-Docker
(DooD) pattern.

**Why DooD for local mode**: Creating real Kubernetes Pods for every submission
locally would require a full registry (ECR equivalent), Calico CNI for
NetworkPolicy enforcement, and pod IP routing — all of which work but add
setup friction for a developer laptop. DooD lets the testing worker fall back
to the Docker path while the rest of the system (Gateway, Compiler, Redis,
Postgres) runs inside Kubernetes as normal.

**Why not Docker-in-Docker (DinD)**: DinD requires a privileged container
running its own Docker daemon, which means nested container layers and
cache isolation — images built by the compiler inside the inner Docker daemon
are invisible to the outer daemon. DooD shares the host daemon, so `kind load`
and the compiler's `docker build` all operate on the same image cache.

---

### 1.3 Calico CNI for NetworkPolicy on Kind

**Why Kind doesn't support NetworkPolicy by default**: Kind ships with the
kindnet CNI plugin, which does not implement the `NetworkPolicy` resource.
Without a NetworkPolicy controller, any `NetworkPolicy` manifest is accepted by
the API server but silently ignored — sandbox pods would have unrestricted
network access.

**Decision**: The deploy script (`scripts/deploy_k8s.sh:57-61`) checks for
Calico and installs it if missing:

```bash
if ! kubectl get daemonset -n kube-system calico-node >/dev/null 2>&1; then
  kubectl apply -f https://raw.githubusercontent.com/.../calico.yaml
fi
```

Calico replaces kindnet and adds a full NetworkPolicy controller that enforces
the `strict-sandbox-policy` in the `iicpc-sandboxes` namespace. On EKS, the
VPC CNI already supports NetworkPolicy natively (EKS 1.25+), so no extra step
is needed.

---

## 2. Namespace Architecture

```
default namespace                         iicpc-sandboxes namespace
──────────────────                        ─────────────────────────
submission-gateway (Deployment)           contestant-* pods (created dynamically)
compilation-worker (Deployment)           NetworkPolicy: strict-sandbox-policy
testing-worker     (Deployment)
leaderboard-generator (Deployment)
postgres  (Deployment + PVC)
redis     (Deployment + PVC)
redpanda  (StatefulSet + VolumeClaimTemplate)
kaniko-build-* (Jobs, ephemeral)
```

**Why two namespaces**: Isolating sandbox pods in `iicpc-sandboxes` allows a
Kubernetes `NetworkPolicy` to apply exclusively to contestant containers without
affecting control-plane services. The `namespaceSelector` in the NetworkPolicy
uses `kubernetes.io/metadata.name: default` to allow the testing worker
(in `default`) to reach sandbox pods (in `iicpc-sandboxes`) on TCP :8000
while blocking all other ingress and all egress from the sandbox namespace.

---

## 3. Identity & RBAC Model

### 3.1 Per-Service ServiceAccounts with IRSA

**File**: [`k8s/eks-rbac.yaml`](k8s/eks-rbac.yaml)

Every microservice gets its own Kubernetes `ServiceAccount` annotated with a
distinct IAM Role ARN. This is the **IRSA (IAM Roles for Service Accounts)**
model — the EKS OIDC provider vends short-lived STS tokens that the AWS SDK
automatically picks up via `AWS_WEB_IDENTITY_TOKEN_FILE`.

| ServiceAccount | IAM Role | Permissions |
|---|---|---|
| `iicpc-gateway` | `gateway-pod-role` | S3 PutObject (upload ZIPs), S3 GetObject (serve artifacts) |
| `iicpc-compiler` | `compiler-pod-role` | S3 GetObject (download ZIPs), ECR PushImage (push built images) |
| `kaniko-sa` | `compiler-pod-role` (shared) | ECR GetAuthorizationToken + PushImage (for Kaniko jobs) |
| `iicpc-testing` | `testing-pod-role` | ECR PullImage (pull contestant images for sandbox pods) |

**Why not a single shared role**: A single role with all permissions means a
compromised compiler pod can pull contestant images, a compromised gateway can
push arbitrary images to ECR, and so on. The principle of least privilege
requires per-service scope. The `kaniko-sa` shares the compiler role because
Kaniko jobs are spawned by the compiler worker — they need push access to the
same ECR repository.

### 3.2 Kubernetes RBAC for API Server Access

Each microservice also needs Kubernetes API server access (beyond AWS IAM).
This is handled via separate `Role` + `RoleBinding` pairs:

**Gateway** (`gateway-pod-reader`):
- `get`, `list` on `pods` in `default` namespace
- Used to check sandbox pod status for the build monitor UI

**Compiler** (`compiler-job-manager`):
- `create`, `get`, `list`, `watch`, `delete` on `batch/jobs`
- `get`, `list`, `watch` on `pods/log`
- Used to schedule and poll Kaniko `Job` resources

**Testing Worker** (`sandbox-manager`):
- `create`, `get`, `list`, `watch`, `delete` on `pods` and `pods/log` in `iicpc-sandboxes`
- Used to create/poll/delete contestant sandbox pods

**Critical design**: The `sandbox-manager` role is bound in the `iicpc-sandboxes`
namespace only. The testing worker pod runs in `default`. This is a
**cross-namespace RoleBinding** — the `subjects:` entry specifies
`namespace: default` while the `RoleBinding` itself lives in `iicpc-sandboxes`.
This is the minimal permission set: the testing worker can manage pods in the
sandbox namespace only, and cannot create pods in `default`.

### 3.3 Kaniko Docker Registry Credential

**File**: `eks-rbac.yaml` (ConfigMap `kaniko-docker-config`)

```yaml
data:
  config.json: |
    { "credsStore": "ecr-login" }
```

This ConfigMap is mounted at `/kaniko/.docker/config.json` inside every
Kaniko Job pod. It tells the Docker credential helper to use `ecr-login`
(the Amazon ECR Credential Helper), which calls `ecr:GetAuthorizationToken`
using the pod's IRSA token. No static ECR password is ever stored in the cluster.

---

## 4. ConfigMap Strategy: Local vs. Production

**Two configmaps, identical key names**:

| Key | `configmap.yaml` (local / Kind) | `eks-configmap.yaml` (production) |
|---|---|---|
| `REDIS_ADDR` | `redis:6379` (in-cluster) | ElastiCache endpoint |
| `DB_ADDR` | in-cluster Postgres URL | RDS endpoint |
| `S3_ENDPOINT` | `host.docker.internal:9000` (MinIO) | `s3.amazonaws.com` |
| `S3_USE_SSL` | `false` | `true` |
| `REGISTRY_URL` | `""` (empty → Docker mode) | ECR registry URL |
| `SANDBOX_NAMESPACE` | `iicpc-sandboxes` | `iicpc-sandboxes` |
| `KAFKA_BROKERS` | `host.docker.internal:9092` | `redpanda:9092` |
| `SYSTEST_NUM_BOTS` | — | `500` |
| `SYSTEST_ORDERS_PER_BOT` | — | `2000` |

**Critical decision — REGISTRY_URL as mode selector**: The compiler worker
uses a single check to decide which build backend to invoke:

```go
// services/compiler/worker.go:29
if os.Getenv("KUBERNETES_SERVICE_HOST") != "" && os.Getenv("REGISTRY_URL") != "" {
    return buildImageWithKaniko(ctx, s3Path, githubURL, submissionID)
}
// else: Docker SDK path (local)
```

- `KUBERNETES_SERVICE_HOST` is auto-injected by Kubernetes into every pod —
  non-empty when running inside the cluster.
- `REGISTRY_URL` is empty in `configmap.yaml` and set in `eks-configmap.yaml`.

This means the same binary runs both paths. In Kind (local), `REGISTRY_URL`
is empty so the Docker SDK path runs — it communicates with the host Docker
daemon via the mounted socket. In EKS (production), both env vars are set,
so the Kaniko path runs and schedules a Kubernetes `Job`.

---

## 5. Compilation Pipeline: Kaniko (Daemonless Image Builds)

### 5.1 Why Kaniko Instead of Docker-in-Docker

**The problem**: Building OCI images from contestant source code inside
Kubernetes requires a container build tool. The naive approach (running a
privileged Docker-in-Docker container) has three problems:
1. Requires `--privileged` mode — a full container escape risk
2. Each build spins up a separate Docker daemon — high memory overhead
3. Image layer caches are isolated per pod — no cross-build cache reuse

**The decision**: Use **Kaniko** (`gcr.io/kaniko-project/executor`). Kaniko
builds Docker images from a `Dockerfile` entirely in userspace without a
Docker daemon. It reads the build context from S3 or a Git URL, builds the
image layer-by-layer, and pushes to ECR — all without privileged access.

**Source**: [`services/compiler/worker.go:168-338`](services/compiler/worker.go)

### 5.2 Kaniko Job Specification

The compiler worker creates a `batch/v1 Job` via `client-go`:

```go
job := &batchv1.Job{
    Spec: batchv1.JobSpec{
        BackoffLimit:          pointerInt32(0),      // no retries — fail fast
        ActiveDeadlineSeconds: pointerInt64(300),    // 5-minute hard timeout
        Template: corev1.PodTemplateSpec{
            Spec: corev1.PodSpec{
                RestartPolicy:      corev1.RestartPolicyNever,
                ServiceAccountName: kanikoSA,        // IRSA for ECR push
                Containers: []corev1.Container{{
                    Name:  "kaniko",
                    Image: "gcr.io/kaniko-project/executor:latest",
                    Args: []string{
                        "--context=s3://submissions/{id}.zip",
                        "--dockerfile=Dockerfile",
                        "--destination=ECR_URL/iicpc-contestants:contestant-{id}",
                        "--cache=true",              // layer cache in ECR
                    },
                    VolumeMounts: []corev1.VolumeMount{{
                        Name:      "docker-config",
                        MountPath: "/kaniko/.docker",  // ecr-login credential helper
                    }},
                }},
            },
        },
    },
}
```

**Key decisions**:
- `BackoffLimit: 0` — a compilation failure should fail immediately, not retry.
  Retrying a build that failed due to a syntax error wastes cluster resources.
- `ActiveDeadlineSeconds: 300` — 5 minutes is generous for any reasonable
  contestant codebase. An engine requiring more than 5 minutes to compile
  almost certainly has a broken Dockerfile.
- `--cache=true` — Kaniko caches layers in ECR. If multiple contestants use
  the same base image (e.g., `golang:1.21-alpine`), the base layers are pulled
  once from ECR cache rather than from Docker Hub every time.
- `RestartPolicy: Never` — Kubernetes should not restart a failed build pod.
  The job controller handles the failure count via `BackoffLimit`.

### 5.3 Kaniko Job Lifecycle (Polling + Cleanup)

```
Compiler Worker
    │
    ├─ Creates kaniko-build-{submissionID} Job
    │
    ├─ Polls BatchV1().Jobs().Get() every 2 seconds
    │       Status.Succeeded > 0 → break (success)
    │       Status.Failed > 0    → break (failure)
    │       timeout 5 min        → break (timeout)
    │
    ├─ Collects logs via CoreV1().Pods().GetLogs()
    │   (uses LabelSelector: "job-name=kaniko-build-{id}")
    │
    └─ defer: Delete job (PropagationPolicy: Background)
             Background propagation deletes the Job object
             immediately; pod GC happens asynchronously.
```

**Why defer for cleanup**: Using `defer` ensures the Kaniko Job is deleted
regardless of whether the build succeeded, failed, or timed out. A leaked
Kaniko Job would accumulate in the `default` namespace and consume the
`compiler-job-manager` role's quota.

---

## 6. Sandbox Pod Lifecycle

### 6.1 Dynamic Pod Creation via client-go

**Source**: [`services/testing/main.go:730-864`](services/testing/main.go)

The testing worker creates contestant sandbox pods programmatically using the
Kubernetes Go client (`k8s.io/client-go`). There is no static sandbox pod
manifest — each evaluation creates and destroys its own pod.

```go
pod := &corev1.Pod{
    ObjectMeta: metav1.ObjectMeta{
        Name:      "contestant-" + runID,
        Namespace: "iicpc-sandboxes",
        Labels: map[string]string{
            "app":           "contestant-sandbox",   // NetworkPolicy selector
            "submission-id": runID,                  // Sweeper + log selector
        },
    },
    Spec: corev1.PodSpec{
        RestartPolicy: corev1.RestartPolicyNever,   // never restart on crash
        NodeSelector:  {"eks.amazonaws.com/nodegroup": "sandbox-executions"},
        Tolerations:   [{Key: "sandbox-only", Value: "true", Effect: NoSchedule}],
        Containers: [{
            Image:           imageTag,               // ECR URL from compiler
            ImagePullPolicy: corev1.PullIfNotPresent,
            Ports:           [{ContainerPort: 8000}],
            Resources: {
                Limits:   {CPU: "2", Memory: "512Mi"},
                Requests: {CPU: "1", Memory: "256Mi"},
            },
            SecurityContext: {
                AllowPrivilegeEscalation: false,
                RunAsNonRoot:             true,
                RunAsUser:                10001,
                Capabilities: {Drop: ["ALL"]},
            },
        }],
    },
}
```

### 6.2 Node Isolation via Taint + Toleration

**Critical decision**: Sandbox pods are pinned to the `sandbox-executions`
nodegroup via two mechanisms:

1. **NodeSelector**: `eks.amazonaws.com/nodegroup: sandbox-executions`
   — the pod will only be scheduled on nodes that belong to this nodegroup.

2. **Toleration**: `sandbox-only=true:NoSchedule`
   — nodes in `sandbox-executions` carry this taint. Only pods that explicitly
   tolerate it can be scheduled there. Control-plane pods (Gateway, Compiler,
   Redis) do not have this toleration and are therefore **blocked** from
   the sandbox nodegroup.

**Why both**: The `NodeSelector` ensures sandbox pods go to the right nodes.
The taint ensures control-plane pods cannot accidentally land on those nodes.
Without the taint, a burst of Gateway replicas could consume CPU on the sandbox
node, introducing noise into evaluation latency measurements.

### 6.3 Pod IP Polling (No Service Resource)

After creating the pod, the testing worker polls for a running pod IP:

```go
for i := 0; i < 60; i++ {              // 60 × 500ms = 30s timeout
    pod, _ := clientset.CoreV1().Pods(ns).Get(ctx, podName, ...)
    if pod.Status.Phase == Running && pod.Status.PodIP != "" {
        endpoint = pod.Status.PodIP + ":8000"
        break
    }
    if pod.Status.Phase == Failed || pod.Status.Phase == Unknown {
        return error("sandbox pod failed")
    }
    time.Sleep(500 * time.Millisecond)
}
```

**Why no Kubernetes Service for sandbox pods**: A `Service` with a stable
virtual IP requires a selector, DNS registration, and `kube-proxy` update
propagation — all of which add 1–5 seconds of latency before the endpoint
is routable. The testing worker connects directly to the pod IP, bypassing
the service mesh entirely. Since the bot fleet connects from the `default`
namespace (allowed by NetworkPolicy), this is safe.

**Why PodIP instead of PodName DNS**: Pod DNS (e.g.,
`contestant-abc.iicpc-sandboxes.pod.cluster.local`) requires CoreDNS to
update its records, which introduces another propagation delay. The pod IP
is available as soon as the pod transitions to `Running` phase — it is
the fastest possible way to get a routable endpoint.

### 6.4 Defensive Cleanup Before Re-creation

```go
existing, getErr := clientset.CoreV1().Pods(ns).Get(ctx, podName, ...)
if getErr == nil {
    // Pod already exists — force-delete with grace period 0
    gracePeriod := int64(0)
    clientset.CoreV1().Pods(ns).Delete(ctx, podName, metav1.DeleteOptions{
        GracePeriodSeconds: &gracePeriod,
    })
    // Wait up to 10s for pod to be fully gone (20 × 500ms polls)
    for i := 0; i < 20; i++ {
        time.Sleep(500 * time.Millisecond)
        _, err := clientset.CoreV1().Pods(ns).Get(ctx, podName, ...)
        if err != nil { break }  // pod is gone
    }
}
```

**Why**: The pod name is deterministic (`contestant-{runID}`). If a pretest
pod is still in `Terminating` state when the system test (same submission)
tries to create a pod with the same name, the `Create` call returns
`AlreadyExists`. Force-deleting with `GracePeriodSeconds: 0` bypasses the
normal 30-second graceful shutdown window. The 10-second wait loop ensures
the API server has deregistered the pod before the new `Create` call goes out.

---

## 7. Orphan Pod Sweeper

**Source**: [`services/testing/main.go:866-919`](services/testing/main.go)

A background goroutine runs every 30 seconds in the testing worker and
deletes any sandbox pod older than `SWEEPER_TIMEOUT_MINUTES` (default: 30):

```go
func startK8sSweeper(ctx context.Context) {
    ticker := time.NewTicker(30 * time.Second)
    for range ticker.C {
        pods, _ := clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
            LabelSelector: "app=contestant-sandbox",
        })
        for _, p := range pods.Items {
            if time.Since(p.CreationTimestamp.Time) > timeoutDur {
                clientset.CoreV1().Pods(ns).Delete(ctx, p.Name, metav1.DeleteOptions{
                    PropagationPolicy: &Background,
                })
            }
        }
    }
}
```

**Why needed**: A testing worker crash (OOM-kill, node failure) during an
evaluation leaves the sandbox pod running indefinitely — it holds 2 vCPU
and 512 MiB on the sandbox nodegroup forever. The sweeper ensures these
orphans are cleaned up within 30 minutes at most. The sweeper runs in
every testing worker replica, so even after a worker restart, new replicas
will sweep orphans from the previous incarnation.

**Why label selector `app=contestant-sandbox`**: This is why the pod creation
code sets `Labels: {"app": "contestant-sandbox"}`. The sweeper needs a way
to identify contestant pods exclusively (not Kaniko pods or any other pods)
without hard-coding namespace-wide assumptions.

---

## 8. Network Policy

**File**: [`k8s/sandbox-networkpolicy.yaml`](k8s/sandbox-networkpolicy.yaml)

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: strict-sandbox-policy
  namespace: iicpc-sandboxes
spec:
  podSelector:
    matchLabels:
      app: contestant-sandbox
  policyTypes: [Ingress, Egress]
  ingress:
  - from:
    - namespaceSelector:
        matchLabels:
          kubernetes.io/metadata.name: default
    ports:
    - protocol: TCP
      port: 8000
  egress: []              # zero egress — all outbound blocked
```

**Three guarantees**:

1. **Zero egress**: `egress: []` (an empty list, not omitted) blocks
   all outbound traffic from sandbox pods. A contestant engine cannot
   make HTTP calls to external APIs, connect to cloud metadata endpoints
   (`169.254.169.254`), or communicate with other sandbox pods.

2. **Ingress from `default` only**: Only pods in the `default` namespace
   (the testing worker and bot fleet) can reach sandbox pods on TCP :8000.
   No other namespace or external IP can initiate connections.

3. **Applied by label**: The `podSelector: {app: contestant-sandbox}` means
   any future pod in `iicpc-sandboxes` without this label (e.g., an admin
   debug pod) is not subject to this policy — the policy is scoped exactly
   to contestant execution pods.

**Why `namespaceSelector` instead of `podSelector` for ingress**: The testing
worker's pod IPs are ephemeral and unknown at policy definition time.
Using `namespaceSelector` allows all pods in `default` to reach sandboxes —
including the testing worker, bot fleet goroutines, and any future services
that need to communicate with sandboxes. A `podSelector` would require
updating the NetworkPolicy every time a new service is added.

---

## 9. Service Deployments

### 9.1 Gateway (`k8s/gateway.yaml`)

```
replicas: 2          ← HA: 2 replicas behind ClusterIP Service
strategy: RollingUpdate (default)
resources: 1 CPU / 512 MiB (limits), 250m / 256 MiB (requests)
ports: 3000 (HTTP), 9093 (Prometheus metrics)
Ingress: ALB (internet-facing, target-type: ip)
```

**Key decisions**:
- **2 replicas**: The gateway is the only user-facing service. A single-replica
  gateway means a pod restart (for any reason) causes a brief outage. 2 replicas
  with `RollingUpdate` strategy ensure zero-downtime deployments.
- **ALB Ingress with `target-type: ip`**: The AWS Load Balancer Controller
  routes traffic directly to pod IPs rather than node IPs. This bypasses
  `kube-proxy` and eliminates an extra network hop. It also requires the ALB
  to be aware of pod readiness via the readiness probe.
- **Readiness probe** on `GET /api/v1/builds`: The gateway is not marked
  ready until its HTTP server is accepting requests. This prevents the ALB
  from sending traffic to a pod that is still initializing its Redis/DB
  connections.
- **`public-html-pvc` volume**: The gateway mounts a shared PVC at `/app/data`
  where the leaderboard generator writes `leaderboard.json`. This shared volume
  is the inter-process communication mechanism between the leaderboard generator
  and the gateway — no service call required.

### 9.2 Compiler Worker (`k8s/compiler.yaml`)

```
replicas: 1
strategy: Recreate    ← critical decision
resources: 1 CPU / 1 GiB (limits)
```

**Critical decision — `Recreate` strategy**: The compiler worker uses a
`Recreate` deployment strategy instead of the default `RollingUpdate`.
The comment in the manifest explains why:

> Recreate strategy avoids CPU surge on 1-replica deployments (kills old
> pod before new one starts)

During a rolling update, Kubernetes creates the new pod while the old one
is still running. For CPU-bound workloads on single-replica deployments,
this momentarily doubles CPU consumption on the same node — which can cause
the node to throttle both pods. `Recreate` eliminates this by terminating
the old pod first, then starting the new one with full CPU budget.

**Why 1 GiB memory limit**: Kaniko runs inside a separate Kubernetes Job,
so the compiler worker itself only needs memory for the Go binary and Redis
stream client. The 1 GiB limit is headroom for cloning large GitHub
repositories (the Docker build path) before the Kaniko path was fully ready.

### 9.3 Testing Worker (`k8s/testing.yaml`)

```
replicas: 1
strategy: Recreate
resources: 1 CPU / 1 GiB (limits)
env: SWEEPER_TIMEOUT_MINUTES=30
```

**Same Recreate rationale as the compiler**: The testing worker runs a
500-goroutine bot fleet during evaluations. A rolling update during an active
evaluation would split the fleet across old and new pods, causing partial
measurements. `Recreate` ensures evaluations always run in a single pod.

**Why 1 CPU limit**: The testing worker's bottleneck during the Calm regime
is I/O wait (Redis reads, TCP connects), not CPU. 1 CPU is sufficient for
1,000–15,000 TPS. Increasing to 2 CPUs is the first scaling lever when
approaching the Panic regime TPS targets.

### 9.4 Leaderboard Generator (`k8s/leaderboard.yaml`)

```
replicas: 1
resources: 250m CPU / 128 MiB
volume: public-html-pvc (shared with gateway)
```

The leaderboard generator is a lightweight service that periodically queries
PostgreSQL and writes `leaderboard.json` to the shared PVC. The gateway
serves this file as a static JSON endpoint. **No direct DB call from the
gateway** — the leaderboard generator is the single writer.

**Design decision — file-based communication over API call**: An alternative
design would have the gateway call the leaderboard generator's REST API.
The file-based approach is simpler (no service discovery needed), has lower
latency (filesystem read vs. HTTP round trip), and is resilient to leaderboard
generator restarts (the file persists on the PVC).

### 9.5 Redis (`k8s/redis.yaml`)

```
replicas: 1
command: redis-server --appendonly yes --appendfsync everysec
PVC: 256 MiB (ReadWriteOnce)
resources: 250m CPU / 256 MiB
```

**Critical configuration — AOF persistence**: `--appendonly yes --appendfsync everysec`
enables Append-Only File (AOF) persistence with 1-second fsync intervals.
This means at most 1 second of Redis Streams messages can be lost on a
crash. Without this, a Redis pod restart would lose all pending queue messages —
including compilation and evaluation jobs that contestants are waiting for.

**Why not Redis Cluster**: Redis Cluster adds complexity (slot assignment,
cross-slot query restrictions, multiple pods) for no benefit at the current
scale. A single Redis node can handle ~100K ops/second — far beyond the
contest's submission rate.

### 9.6 Redpanda (`k8s/redpanda.yaml`)

```
kind: StatefulSet                 ← ordered pod identity + stable storage
replicas: 1
storageClassName: gp2
volumeClaimTemplate: 5 GiB
initContainer: fix-data-permissions (chown 101:101)
args: --mode=dev-container --smp=1 --memory=1Gi
```

**Critical decision — StatefulSet**: Redpanda requires persistent storage
with a stable pod identity. A `Deployment` with a PVC would work for a single
replica but would fail to scale to multiple brokers because all replicas
would attempt to bind the same `ReadWriteOnce` PVC. `StatefulSet` with
`volumeClaimTemplates` creates a dedicated PVC per pod, which is the correct
pattern for stateful workloads and makes future multi-broker expansion trivial.

**initContainer for EBS volume permissions**: EBS volumes mount as `root:root`.
Redpanda runs as UID 101 (the `redpanda` user inside the container). Without the
`chown -R 101:101` initContainer, Redpanda fails with a `Permission Denied`
error when trying to write to `/var/lib/redpanda/data`. The initContainer runs
as root specifically to fix this — the main Redpanda container then runs
non-root as intended.

---

## 10. Persistent Storage Strategy

### 10.1 Volume Inventory

| PVC | Access Mode | Size | Used By |
|---|---|---|---|
| `postgres-pvc` | ReadWriteOnce | 1 GiB | Postgres data directory |
| `redis-pvc` | ReadWriteOnce | 256 MiB | Redis AOF file |
| `redpanda-datadir-0` | ReadWriteOnce | 5 GiB | Redpanda topic data |
| `public-html-pvc` | ReadWriteOnce | 100 MiB | leaderboard.json (shared gateway↔leaderboard) |

**Why `ReadWriteOnce` everywhere**: `ReadWriteOnce` PVCs are backed by EBS
volumes (gp2 StorageClass). `ReadWriteMany` would require EFS (NFS-based),
which adds latency and cost. Since every stateful service runs as a single
replica, `ReadWriteOnce` is sufficient — only one pod needs to write at a time.

**Why `gp2` StorageClass**: `gp2` (General Purpose SSD) provides baseline 3
IOPS/GiB with burst to 3,000 IOPS. For Redis AOF (sequential writes) and
Postgres WAL (sequential + random reads), gp2 is adequate. A future upgrade
to `gp3` would provide consistent 3,000 IOPS baseline with lower cost.

### 10.2 `public-html-pvc` as Inter-Process Channel

```
leaderboard-generator pod          gateway pods (×2)
     │                                    │
     └─── writes leaderboard.json ───→   └─── reads + serves leaderboard.json
               (public-html-pvc)                   (public-html-pvc)
```

This volume is mounted `ReadWriteOnce`, but **two pods mount it simultaneously**:
the leaderboard generator writes and the gateway reads. This works on Kind
(hostPath) because both pods run on the same node. On EKS, this requires both
pods to land on the same AZ as the EBS volume. The `public-html-pvc` volume
claim does not specify `storageClassName`, so it uses the cluster default —
ensuring the volume and its consumer pods are in the same AZ.

---

## 11. Database Migration Job

**File**: [`k8s/migration-job.yaml`](k8s/migration-job.yaml)

```yaml
kind: Job
spec:
  backoffLimit: 5
  template:
    spec:
      restartPolicy: OnFailure
      containers:
      - command:
        - sh
        - -c
        - /usr/local/bin/goose -dir /migrations postgres "$DB_ADDR" up
```

**Key decisions**:

- **Job, not InitContainer**: Migrations run as a standalone `batch/v1 Job`
  rather than an initContainer on the Postgres pod. This allows migrations to
  be re-run independently without restarting Postgres. The deploy script waits
  for the Job to complete (`kubectl wait --for=condition=complete`) before
  deploying application pods.

- **`sh -c` wrapper**: The Goose migration binary reads `$DB_ADDR` from the
  environment. Kubernetes's `$(VAR)` substitution in `command:` arrays only
  expands `env:` entries, not `envFrom:` entries. Using `sh -c` causes the
  shell to expand `$DB_ADDR` from the container environment (which includes
  both `env:` and `envFrom:` sources).

- **`backoffLimit: 5`**: If the DB is not yet ready when the Job starts,
  Goose will fail with a connection error. `backoffLimit: 5` allows up to
  5 retries with exponential backoff — giving the Postgres deployment up to
  ~30 seconds to become healthy.

- **ConfigMap-mounted migrations**: The migration SQL files are loaded into
  a ConfigMap (`migrations-config`) and mounted into the Job pod. The deploy
  script recreates this ConfigMap on every deploy (`kubectl delete configmap
  migrations-config --ignore-not-found && kubectl create configmap
  migrations-config --from-file=migrations/`), ensuring the Job always runs
  with the latest migration files.

---

## 12. Horizontal Pod Autoscaling

**Files**: [`k8s/hpa/compiler-hpa.yaml`](k8s/hpa/compiler-hpa.yaml),
[`k8s/hpa/testing-hpa.yaml`](k8s/hpa/testing-hpa.yaml)

| HPA | Target Deployment | Min | Max | CPU Threshold |
|---|---|---|---|---|
| `compilation-worker-hpa` | compilation-worker | 1 | 20 | 60% |
| `testing-worker-hpa` | testing-worker | 1 | 20 | 80% |

**Why different CPU thresholds**:
- **Compiler at 60%**: Docker/Kaniko builds are bursty — a build starts at
  100% CPU and drops to 0% when done. A 60% threshold means a second compiler
  replica starts before the first is fully saturated, reducing queue wait time
  during submission surges.
- **Testing worker at 80%**: The testing worker's CPU usage is more sustained
  during bot fleet execution (goroutine scheduling, TCP I/O). 80% is a more
  efficient threshold — scaling too early wastes cluster resources since each
  testing-worker pod runs evaluations sequentially (one per Redis message).

**Why max 20 replicas**: The EKS nodegroup is sized for ~20 concurrent
evaluations at baseline. The Cluster Autoscaler will add nodes if pods are
pending, but the max replica count prevents a thundering-herd scenario where
200 submissions simultaneously trigger 200 Kaniko jobs and exhaust ECR API
rate limits.

**Limitation**: The CPU-based HPA does not know about Redis queue depth.
A more precise HPA would scale on the `iicpc_queue_depth` custom metric from
Prometheus. The current CPU-based approach is sufficient because compilation
is CPU-bound — a high queue depth always corresponds to high CPU utilization
on the existing replicas.

---

## 13. Cluster Autoscaler

**File**: [`k8s/cluster-autoscaler.yaml`](k8s/cluster-autoscaler.yaml)

The Cluster Autoscaler (CA) watches for `Pending` pods and triggers AWS Auto
Scaling Group scale-out events when pods cannot be scheduled.

**Key configuration**:
```
--expander=least-waste          # prefer nodegroup that wastes fewest resources
--balance-similar-node-groups   # distribute pods across AZs
--scale-down-delay-after-add=2m # wait 2 minutes before considering scale-in
--scale-down-unneeded-time=2m   # scale in after 2 minutes idle
--skip-nodes-with-system-pods=false
--skip-nodes-with-local-storage=false
```

**Critical settings**:
- `--expander=least-waste`: When multiple nodegroups can host a pending pod,
  pick the one that wastes the least CPU/memory. This prevents the CA from
  spinning up a large node when a small one would suffice.
- `scale-down-delay-after-add=2m` and `scale-down-unneeded-time=2m`: Both set
  to 2 minutes. The CA will scale in aggressively (after 2 minutes idle) to
  minimize cost during contest off-hours. During the contest, the HPA continuously
  creates pods, which prevents the CA from triggering scale-in.
- `--node-group-auto-discovery`: Uses ASG tag-based discovery
  (`k8s.io/cluster-autoscaler/iicpc-benchgrid`). Any nodegroup in the EKS
  cluster tagged with this label is managed by the CA — no hardcoded ASG names.
- `priorityClassName: system-cluster-critical`: The CA pod itself is protected
  from eviction. If the CA were evicted, nodes would not scale down, accumulating
  cost.

---

## 14. Deployment Script Flow (`scripts/deploy_k8s.sh`)

The 9-step deployment script encodes the correct dependency order:

```
Step 1: Detect Kubernetes context (Kind / Minikube / Generic EKS)
    │
Step 2: Build Go binaries (CGO_ENABLED=0, linux/amd64) and Docker images
    │
Step 3: Load images into cluster
    │   Kind:     kind load docker-image
    │   Minikube: minikube image load
    │   EKS:      images already in ECR (pushed separately)
    │
Step 4: Apply RBAC, ConfigMap, Volumes, Postgres, Redis
    │   (stateful infrastructure first)
    │
Step 5: Wait for Postgres to be healthy (kubectl rollout status, timeout 90s)
    │   (ensures migrations don't run against an unready DB)
    │
Step 6: Create migrations ConfigMap → delete old Job → apply migration Job
    │   Wait for Job completion (timeout 60s)
    │   (schema must exist before application pods start)
    │
Step 7: Apply NetworkPolicy → compiler → testing worker (if not HYBRID) → gateway
    │   (NetworkPolicy before sandbox pods; workers before gateway)
    │
Step 8: Apply HPAs for compiler (and testing if not HYBRID)
    │
Step 9: kubectl get pods -o wide (final status check)
```

**HYBRID mode** (`HYBRID=true`): Skip deploying the testing worker inside
Kubernetes. Used when the testing worker runs on the host (or a beefier
machine) but communicates with the K8s gateway and Redis. This was critical
during development: it allows the testing worker to run with Docker DooD
while the gateway and Redis run inside Kind — a staged migration path.

---

## 15. Build Mode Detection Summary

```
┌────────────────────────────────────────────────────────────────────┐
│  Runtime environment           Build backend used                  │
│  ──────────────────────────    ──────────────────────────          │
│  KUBERNETES_SERVICE_HOST=""    Docker SDK (host daemon via socket) │
│  REGISTRY_URL=""               → build local image, run container  │
│                                                                     │
│  KUBERNETES_SERVICE_HOST≠""    Kaniko Job (k8s batch/v1)          │
│  REGISTRY_URL≠""               → build OCI image, push to ECR     │
│                                                                     │
│  KUBERNETES_SERVICE_HOST≠""    Error: REGISTRY_URL required        │
│  REGISTRY_URL=""               → fail fast with clear message      │
└────────────────────────────────────────────────────────────────────┘
```

The same compiled binary handles all three cases. The environment variables
are the only configuration knobs — no compile-time flags, no separate binaries.

---

## 16. Critical Decision Summary

| Decision | Choice Made | Alternatives Rejected | Reason |
|---|---|---|---|
| Local cluster | Kind + Docker socket mount | Minikube, k3d | Kind supports multi-node, full K8s API, and is CI-friendly |
| Local CNI | Calico (installed via script) | kindnet (default) | kindnet ignores NetworkPolicy |
| Production CNI | VPC CNI (EKS default) | Calico on EKS | VPC CNI gives each pod a real VPC IP — no overlay network latency |
| Image building | Kaniko Job (K8s) | Docker DinD (privileged) | No privileged pods; ECR layer caching; no daemon memory overhead |
| Sandbox networking | Direct pod IP | Kubernetes Service | Avoids kube-proxy propagation latency (~1-5s); Service DNS add further delay |
| Namespace isolation | `iicpc-sandboxes` separate namespace | Same namespace as workers | NetworkPolicy `namespaceSelector` requires namespace boundary |
| Zero-egress enforcement | `egress: []` in NetworkPolicy | Seccomp `connect` blocking | NetworkPolicy is the authoritative network layer; seccomp is defence-in-depth |
| Sandbox node isolation | NodeSelector + Taint/Toleration | Node affinity only | Taint prevents control-plane pods from landing on sandbox nodes |
| Compiler strategy | Recreate | RollingUpdate | Avoids dual-pod CPU spike on 1-replica compiler |
| Testing strategy | Recreate | RollingUpdate | Prevents split-fleet evaluation during rolling updates |
| Redis persistence | AOF every 1s | No persistence (default) | At-most-1s message loss on crash; required for PEL recovery |
| Redpanda | StatefulSet | Deployment + PVC | StatefulSet `volumeClaimTemplates` supports future multi-broker expansion |
| EBS permissions | initContainer (root chown) | SecurityContext fsGroup | fsGroup on EBS volumes has known issues on some EKS versions |
| Migration execution | standalone Job | InitContainer | Re-runnable independently; survives DB restart without pod restart |
| HPA metric | CPU utilization | Custom Redis queue depth | CPU is a reliable proxy; custom metrics require KEDA or Prometheus adapter |
| Autoscaler expander | least-waste | random, most-pods | Minimizes wasted node capacity; critical for cost efficiency |
