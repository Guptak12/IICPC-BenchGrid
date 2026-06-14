# IICPC-BenchGrid — Achievements & Future Roadmap

> A ground-truth audit of what is fully built, what is partially built,
> and what is designed but not yet implemented.

---

## 1. What We Built — Full Achievement Inventory

### 1.1 End-to-End Submission Pipeline (Fully Implemented)

The complete path from a contestant's ZIP upload to a scored leaderboard entry is operational in both local (Kind) and production (EKS) environments.

```
Contestant ZIP upload
  → Gateway: auth check + Redis rate-limit (1/min/user, SETNX+TTL)
  → S3/MinIO: source stored at submissions/{id}.zip
  → PostgreSQL: submission row created (status: queued)
  → Redis Stream: job published to compilation_queue
  → Compiler Worker: XREADGROUP dequeue → Kaniko Job (EKS) / Docker build (local)
  → ECR/local registry: image pushed as contestant-{id}:latest
  → Redis Stream: job published to pretest_queue
  → Testing Worker: K=3 sandbox pods spawned → bot fleet → shadow validator
  → PostgreSQL: score + verdict written (status: completed)
  → SSE hub: leaderboard rebroadcast within 5s
```

Every step has been end-to-end verified via `scripts/run_e2e_tests.sh` and `scripts/local_smoke.sh`.

---

### 1.2 Microservices (3 Go Services + Shared Library)

| Service | Language | Key capabilities |
|---|---|---|
| `services/gateway` | Go (Fiber) | REST API, auth, rate limiting, SSE leaderboard hub, developer dashboard, zip→tar.gz normalization, static JSON serving, Prometheus metrics `:9093` |
| `services/compiler` | Go | Redis Streams consumer, Docker/Kaniko toggle, 5-min deadline, S3 upload/download, Prometheus metrics `:9091` |
| `services/testing` | Go | Dynamic K8s pod lifecycle, bot fleet runner, MMPP scheduler, shadow validator, orphan sweeper, PEL recovery, Prometheus metrics `:9092` |
| `services/common` | Go (shared lib) | Prometheus metrics registry, Redis helpers, S3 client, seccomp profile, PEL recovery, retry logic, DB pool configurator |

---

### 1.3 Security Sandboxing (4-Layer, Fully Deployed)

| Layer | Implementation | Status |
|---|---|---|
| cgroup v2 compute limits | `PidsLimit: 128`, `Memory: 512MiB`, `NanoCPUs: 2×10⁹` | ✅ Deployed |
| Kernel hardening | `CapDrop ALL`, custom seccomp (`fork`→KILL, `connect`→ERRNO, `clone`→ALLOW) | ✅ Deployed |
| Network zero-egress | `NetworkPolicy` `egress: []` in `iicpc-sandboxes` ns | ✅ Deployed (Calico CNI) |
| Node isolation | `sandbox-executions` nodegroup taint `sandbox-only=true:NoSchedule` | ✅ Deployed (EKS) |

---

### 1.4 Bot Fleet & Shadow Validator (Fully Implemented)

| Feature | Implementation detail |
|---|---|
| 500-goroutine in-process fleet | Spawned as `errgroup` goroutines — no container overhead |
| MMPP regime switching | Calm (1K TPS) → Elevated (20K TPS) → Panic (500K TPS target) with Markov transition probabilities |
| HDR Histogram telemetry | P50/P90/P99 computed via `hdrhistogram-go`; additive across trials |
| Shadow order book | Red-Black tree per symbol shard; independently replays every order and diffs actual fills |
| 3 anomaly types | Priority Violations, Phantom Fills, Trade Discrepancies |
| Protocol adapters | TCP/Protobuf (default), WebSocket, REST, FIX — auto-detected via `ENGINE_PROTOCOL` env var |
| 8 reference engines | `go_optimized`, `cpp_basic`, `python_slow`, `rust_crash`, `go_ws`, `go_rest`, `go_fix`, `node_scammer` — all pass CI |

**Pre-test**: 50 bots × 200 orders = 10 000 orders  
**System test**: 500 bots × 2 000 orders = **1 000 000 orders per evaluation**

---

### 1.5 Scoring System (Fully Implemented)

| Component | Status |
|---|---|
| K=3 trial averaging | ✅ |
| Composite formula (50% correctness, 30% latency, 20% throughput) | ✅ |
| Verdict gate cascade (Logic Violation → TLE → Throughput Degradation → Accepted) | ✅ |
| Stability bonus (+5 pts if StdDev < 2.0%) | ✅ |
| Engine archetype classification (Latency-Optimized / Accuracy-Optimized / Balanced / Low-Throughput) | ✅ |

---

### 1.6 Database (PostgreSQL, 7 Migrations, Goose Runner)

| Feature | Migration | Status |
|---|---|---|
| Core `submissions` table with scoring columns | 00001 | ✅ |
| `submission_sources` TOAST split | 00003 | ✅ |
| GIN index on JSONB diagnostics | 00003 | ✅ |
| `users` + `arenas` + `arena_registrations` | 00005 | ✅ |
| `CREATE INDEX CONCURRENTLY` leaderboard index | 00006 | ✅ |
| Fix btree row-size overflow (JSONB removed from INCLUDE) | 00007 | ✅ |
| `updated_at` trigger (unconditional, crash-safe) | 00001 | ✅ |

---

### 1.7 Infrastructure as Code

| Component | Tool | Status |
|---|---|---|
| EKS cluster (1.35), VPC, subnets, security groups | Terraform (`eks.tf`, `vpc.tf`) | ✅ |
| RDS PostgreSQL, ElastiCache Redis | Terraform (`rds_elasticache.tf`) | ✅ |
| S3 bucket, ECR registry | Terraform (`s3_ecr.tf`) | ✅ |
| IRSA roles (per-service IAM least-privilege) | Terraform (`iam_irsa.tf`) | ✅ |
| K8s manifests (15 YAML files) | `k8s/` | ✅ |
| HPA (compiler 60% CPU, testing 80% CPU, 1→20 replicas) | `k8s/hpa/` | ✅ |
| Cluster Autoscaler (`least-waste`, 2-min scale-down) | `k8s/cluster-autoscaler.yaml` | ✅ |
| Calico CNI (auto-injected for Kind) | `scripts/deploy_k8s.sh` | ✅ |
| Kaniko daemonless build (no privileged pods) | `k8s/compiler.yaml` | ✅ |
| One-shot DB migration Kubernetes Job | `k8s/migration-job.yaml` | ✅ |

---

### 1.8 Observability (Fully Deployed)

| Component | Details |
|---|---|
| **Grafana** (live on EKS ALB) | 14 panels: stat tiles, queue depths, HPA replicas, fleet TPS, P99 latency, correctness, CPU %, DB pool |
| **Prometheus** | 15 metrics across 3 services; 15s scrape via `kube-prometheus-stack` ServiceMonitors |
| **Developer Dashboard** (`/dashboard`) | K8s pod counts, queue depths, recent submission table, mock pretest/systest triggers |
| **Contestant Diagnostic Drawer** | Radar chart, latency histogram, TPS trend, anomaly badges, strategy breakdown, stability score |
| **SSE Leaderboard** | ≤5s push latency; non-blocking hub; `EventSource` auto-reconnect |

---

### 1.9 Fault Tolerance (Fully Implemented)

| Failure mode | Recovery mechanism |
|---|---|
| Worker OOM-killed mid-evaluation | PEL `XCLAIM` reaper every 30s (`common/pel_recovery.go`) |
| Contestant engine crash mid-run | Per-bot independent TCP socket; others continue unaffected |
| 3 consecutive worker failures | Dead-letter queue stream for operator |
| Redis partition | 2s blocking read + sleep-retry loop |
| Sandbox pod "already exists" | Delete-before-create with 10s grace wait |
| Stale orphan sandbox pods | Background sweeper: 30-min TTL, label selector `app=contestant-sandbox` |

---

### 1.10 Testing & Verification

| Script / Test | What it verifies |
|---|---|
| `scripts/local_smoke.sh <engine>` | Single submission through full pipeline; checks verdict + score |
| `scripts/run_e2e_tests.sh` | All 8 reference engines; checks correctness, TLE, crash handling |
| `scripts/run_systest.sh` | Full 500-bot × 2000-order system test load |
| `scripts/test_two_engines.sh` | Parallel concurrent submission (race condition check) |
| `bot-fleet/runner_test.go` | Unit tests for MMPP scheduler and fleet telemetry aggregation |
| `bot-fleet/god_test.go` | Shadow validator correctness against deterministic order sequences |
| `verify_k8s.py` | Post-deploy pod readiness and HPA registration check |

---

## 2. Current State: What Works in Production (EKS)

The platform is **live** at the production ALB endpoint. As of the last deployment:

- ✅ **19 submissions** processed (visible in dashboard)
- ✅ **Max composite score: 90.00** (`go_optimized` engine, 100% correctness, P99 ≈ 350µs)
- ✅ **Grafana dashboards** showing real HPA scale-out during load tests (compiler-hpa spiked to 15 replicas, testing-hpa to 10 during a 20-concurrent-submission test)
- ✅ **Bot fleet TPS spike to ~4 000 orders/min** captured in Grafana during system test runs
- ✅ **K8s in-cluster mode** confirmed (dashboard shows "K8S IN-CLUSTER MODE" badge with 2 gateway pods, 1 compiler, 1 testing pod)

---

## 3. Known Limitations (Current)

| Limitation | Root cause | Impact |
|---|---|---|
| Fleet TPS ceiling ~15K (vs 500K MMPP Panic target) | Single testing-worker on 2 vCPU; 500 goroutines compete for CPU | Panic regime never actually reached during evaluation |
| Leaderboard query degrades at ~10K submissions | `ORDER BY composite_score DESC` full table scan every 5s | Not a concern for hackathon scale; future issue at platform scale |
| Redis single-shard | All queues on one instance | ~100K ops/s ceiling |
| HPA on CPU only | No queue-depth-based scaling | Compiler pods don't scale until CPU is high, not when queue depth spikes |
| No gVisor (`runsc`) on EKS | `runtimeClassName: gvisor` requires DaemonSet installation on all nodes | Seccomp + CapDrop + NetworkPolicy serve as the kernel-layer boundary instead |
| No read replicas for PostgreSQL | RDS single writer | High leaderboard query frequency hits the primary |
| Kaniko cache TTL not set | `--cache-ttl` defaults to 2 weeks | Stale base layers may be reused after major base image updates |

---

## 4. Future Roadmap

### Phase 1 — Vertical Scale + HPA Queue-Depth Trigger (Near-term)

> Target: handle 20+ concurrent system tests without TPS ceiling

- **Move testing-worker to `c6i.4xlarge`** (16 vCPU) — more goroutine parallelism, lower per-goroutine scheduler delay
- **HPA on `iicpc_queue_depth`** via Prometheus Adapter / KEDA:
  ```yaml
  # Current: CPU-based
  targetCPUUtilizationPercentage: 80
  # Future: queue-depth-based
  external:
    metric: { name: iicpc_queue_depth, selector: { queue: systest_queue } }
    target: { type: AverageValue, averageValue: "5" }
  ```
- **PodDisruptionBudget** for gateway (minAvailable: 1) to survive node drains during autoscaling

---

### Phase 2 — Distributed Bot Fleet Executor (Medium-term)

> Target: 5 000 concurrent bots, true Panic regime TPS

```
Coordinator Pod
  ├── Bot-Shard-0 (500 bots) → contestant pod :8000
  ├── Bot-Shard-1 (500 bots) → contestant pod :8000
  ├── ...
  └── Bot-Shard-N (500 bots) → contestant pod :8000
        ↓ HDR histograms (additive merge)
Fleet-Aggregator Pod → final PretestResult
```

- HDR histograms are **additive** — partial results from N shards merge without information loss
- Analogous to Locust master-worker distributed load testing model
- gRPC-based coordinator (already partially scaffolded in `bot-fleet/worker_node.go`)

---

### Phase 3 — Redis Cluster + Queue Sharding (Medium-term)

> Target: 50 000 submissions/day

- Replace single Redis with Redis Cluster (3 shards, 3 replicas)
- Shard `compilation_queue_{0..N}` by `hash(submission_id) % N`
- Gateway routes to the correct shard; each compiler worker reads from its assigned shard
- PEL recovery must be shard-aware (scan each shard independently)

---

### Phase 4 — Leaderboard CQRS (Medium-term)

> Target: <100ms push latency at 50K submissions

Replace the `ORDER BY composite_score DESC` full-table scan with a Redis Sorted Set:

```
On submission complete:
  ZADD leaderboard:{arena_id} <composite_score> <submission_id>

On leaderboard broadcast:
  ZREVRANGE leaderboard:{arena_id} 0 99 WITHSCORES  → O(log N + M)
```

- Per-contestant best-of-N: maintain a separate `ZADD leaderboard:best:{arena_id}` updated only if new score > current best
- Push latency drops from ~5s (DB query + SSE) to <100ms (Redis ZREVRANGE + SSE)

---

### Phase 5 — gVisor Kernel Sandbox (Long-term)

> Target: defence-in-depth Layer 5 — kernel syscall interception

Install the gVisor `runsc` runtime DaemonSet on `sandbox-executions` nodes:

```yaml
# Sandbox pod spec
runtimeClassName: gvisor
```

gVisor intercepts all syscalls in user space via `ptrace` or KVM, presenting a fake kernel surface to the contestant process. Combined with the existing seccomp profile and NetworkPolicy, this would make the sandbox resistant to **kernel 0-day exploits** — even if a contestant's code finds a syscall not blocked by seccomp, gVisor's interceptor handles it safely.

**Why not implemented yet**: requires `containerd` `runsc` shim installation on every EKS worker node via a privileged DaemonSet — additional operational complexity deferred to post-hackathon hardening.

---

### Phase 6 — Multi-Protocol Leaderboard & Public API (Long-term)

> Target: platform usable as a general HFT benchmarking SaaS

- **WebSocket leaderboard** (bidirectional, reconnect-safe) alongside SSE
- **Public REST API** (`/api/v1/arenas`, `/api/v1/submissions/{id}/results`) with OAuth2 token auth
- **Arena templates** — pre-configured test parameters (mini-hackathon, full-day, 24h endurance)
- **Contestant SDK** — Go/C++/Rust client libraries with reference matching engine implementations
- **Multi-region EKS** — us-east-1 (primary) + ap-south-1 (replica) for global participant latency fairness

---

## 5. Summary Scorecard

| Category | Implemented | Partial | Planned |
|---|---|---|---|
| End-to-end submission pipeline | ✅ | | |
| Secure 4-layer sandboxing | ✅ | | |
| Kaniko daemonless builds | ✅ | | |
| Bot fleet (500 bots, MMPP) | ✅ | | |
| Shadow validator | ✅ | | |
| K=3 trial scoring + archetypes | ✅ | | |
| Real-time SSE leaderboard | ✅ | | |
| Prometheus + Grafana (14 panels) | ✅ | | |
| Developer + contestant dashboards | ✅ | | |
| PostgreSQL (7 migrations, Goose) | ✅ | | |
| EKS + Terraform IaC | ✅ | | |
| HPA (CPU-based, 1→20 replicas) | ✅ | | |
| Cluster Autoscaler | ✅ | | |
| IRSA per-service least-privilege | ✅ | | |
| PEL fault recovery | ✅ | | |
| Distributed bot fleet (gRPC shards) | | 🔶 scaffolded | |
| HPA queue-depth trigger (KEDA) | | | 📋 Phase 1 |
| Redis Cluster sharding | | | 📋 Phase 3 |
| Leaderboard CQRS (Redis sorted set) | | | 📋 Phase 4 |
| gVisor kernel sandbox | | | 📋 Phase 5 |
| Public API + SDK + multi-region | | | 📋 Phase 6 |
