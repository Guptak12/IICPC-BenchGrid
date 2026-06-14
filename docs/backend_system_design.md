# IICPC 2026: BenchGrid Backend System Design
## Distributed Benchmarking Platform for High-Performance Matching Engines

This document provides a comprehensive system architecture blueprint and details the engineering decisions made for the **IICPC-BenchGrid** backend platform—a high-scale, distributed benchmarking engine designed to compile, sandbox, validate, and rank contestant-submitted C++ matching engines under stress.

---

## 1. Architectural Overview & Component Decomposition

BenchGrid is structured as a collection of stateless, event-driven microservices connected via **Redis Streams** and backed by **PostgreSQL** and **MinIO / S3 Object Storage**. This decouples the ingress submission lifecycle from the compute-heavy compilation and sandboxed testing pipelines.

```mermaid
flowchart TD
    Contestant([Contestant Browser])
    
    subgraph Gateway Layer
        GW["Submission Gateway API<br/>(Fiber Web Server)"]
        S3[("Object Storage<br/>(MinIO / S3)")]
    end
    
    subgraph Queue Layer (Redis Streams)
        CQ["Compilation Queue"]
        TQ["Pretest Queue"]
        SQ["System Test Queue"]
    end
    
    subgraph Worker Pool
        CW["Compiler Workers<br/>(Auto-scaled)"]
        PW["Testing Workers (Pretests / Systests)<br/>(Auto-scaled)"]
    end
    
    subgraph State & Ranking
        DB[("PostgreSQL")]
        LG["Leaderboard Generator<br/>(Gateway Task)"]
        CDN["Static JSON (leaderboard.json)"]
    end
    
    Contestant -->|1. POST /submit| GW
    GW -->|2. Upload ZIP| S3
    GW -->|3. Record submission| DB
    GW -->|4. Push Job| CQ
    CQ --> CW
    
    CW -->|5. Build image| S3
    CW -->|6. Compile success| TQ
    CW -->|7. Compile fail| DB
    
    TQ --> PW
    SQ --> PW
    PW -->|8. Run K-sandboxes| Sandbox["Contestant Sandbox Container"]
    PW -->|9. Telemetry & Scoring| DB
    
    DB --> LG
    LG -->|10. Materialize every 3s| CDN
    CDN -->|11. Cache / Static read| Contestant
```

---

## 2. Component Design & Key Engineering Decisions

### Component 1: Submission Gateway Service

The Submission Gateway handles contestant uploads, authentication checks, and job queuing. It is designed to be completely stateless to scale horizontally behind a load balancer.

*   **Redis TTL-Based Rate Limiting**: Enforces a strict rate limit of **1 submission per minute per user** using `Redis` keys with an expiry TTL. This prevents spam attacks from flooding the compilation queues without requiring database lookups.
*   **Object Storage (S3/MinIO) for Source Code Storage**: Instead of storing binary payloads or C++ source blobs directly in PostgreSQL (which would cause massive database bloat and slow down indexes), the Gateway uploads submissions as ZIP archives directly to S3. Only the S3 metadata key is written to the database.
*   **Immediate 202 Accepted Handshake**: The Gateway responds to submissions in $<50\text{ms}$ with a unique submission ID and a status polling endpoint, keeping the client connection short and allowing the client to poll asynchronously.

---

### Component 2: Compilation Service

The Compilation Worker processes compilation tasks sequentially from the `compilation_queue` stream. It isolates build environments by executing contestant builds inside dynamic containers.

*   **Ephemeral Workspace Isolations**: For each build, a temporary directory is created inside the scratch disk space. After the build completes, the workspace is immediately deleted to conserve local storage.
*   **Dynamic Containerized Compilations**: The worker invokes Docker to build a container image tagged `contestant-<submissionID>` using the contestant's included `Dockerfile`. This ensures that compilers (such as `g++` or `clang`) run in complete isolation from the host OS.
*   **Kaniko Blueprint for Production Security**:
    *   *Decision*: In production Kubernetes environments, mounting `/var/run/docker.sock` (Docker-out-of-Docker / DooD) is a high-risk security vulnerability. We plan to transition compilation tasks to **Kaniko Jobs**.
    *   *Mechanism*: Kaniko runs inside an unprivileged Kubernetes pod, fetches the contestant ZIP context from S3, compiles the executable in user space, and pushes the final container image to a secure private registry (GCR/ECR) without requiring root host capabilities.

---

### Component 3: Testing & Validation Service

The Testing Service is the core execution runner. It handles both **Pretests** (triggered immediately upon submission) and **Post-Contest System Tests** (heavyweight batch tests).

*   **Dual-Queue Processing**: A single testing worker polls both `pretest_queue` and `systest_queue` streams. It branches execution parameters based on the stage:
    *   *Pretests*: Small, fast deterministic runs (5 bots, 100 orders, fixed seed) designed to complete in under 5 seconds.
    *   *System Tests*: Full concurrency benchmarks (50–200 bots, up to 2K orders per bot, mixed trading strategies) simulating production-level network loads.
*   **Parallelized K=3 Sandboxing**: To benchmark matching engines reliably, BenchGrid runs $K=3$ consecutive runs. By executing these K-runs in parallel using `golang.org/x/sync/errgroup`, we cut the evaluation loop time down from 15 seconds to under 5 seconds.
*   **Dynamic Host Port Allocation (`HostPort: "0"`)**:
    *   *Problem*: Host-mode network sharing (`NetworkMode: "host"`) inside sandboxes with dropped root privileges triggers kernel `SIGSYS` (exit code 159) startup traps due to strict seccomp filtering on namespace creation.
    *   *Solution*: The sandbox runs in an isolated bridge network (`sandbox-net`). The runner publishes port `8000` (raw TCP) to a dynamic port on the host loopback `127.0.0.1` by specifying `"HostPort": "0"`. The worker inspects the running container to find the allocated port, ensuring zero port collisions and absolute process-level isolation.

---

### Component 4: Bot Fleet & Shadow Validator

The Load Generator simulates trading activity by spawning a virtual fleet of bots that interact with the contestant's matching engine.

```
                  ┌──────────────────────────────────────────┐
                  │              Testing Service             │
                  │                                          │
                  │   ┌───────────────┐  websocket/TCP  ┌────┴──────────────┐
                  │   │   Bot Fleet   │ <─────────────> │ Contestant Engine │
                  │   └───────────────┘                 │     (Sandbox)     │
                  │           │                         └────┬──────────────┘
                  │           ▼                               │
                  │     [Order Execs]                         │ Order Updates
                  │           │                               │
                  │           ▼                               │
                  │   ┌───────────────┐                       │
                  │   │  Shadow Book  │ <─────────────────────┘
                  │   │   Validator   │ (Priority & Wash checks)
                  │   └───────────────┘
                  └──────────────────────────────────────────┘
```

*   **In-Process Client Simulation**: Spawning external clients (e.g. via separate containers) adds excessive startup overhead and network jitter. Spawning bots in-process as Go routines inside the Testing runner guarantees high-precision telemetry and keeps pretest loops extremely fast.
*   **Shadow Order Book Oracle**: The bot fleet telemetry channel streams matching events into an in-memory Go order book validator (`bot-fleet/shadow/validator.go`). This serves as the ground-truth oracle to verify:
    *   *Price-Time Priority*: Ensuring resting orders at better prices are filled first.
    *   *Phantom Fills*: Ensuring the engine never fills orders that do not exist or mismatch quantities.
    *   *Self-Crossing / Wash Trades*: Confirming that bots do not execute transactions with themselves.

---

### Component 5: Scoring & Leaderboard Engine

The Scoring Engine writes results to PostgreSQL and updates the leaderboard.

*   **Graduated Score SLA Matrix**: Submissions are graded across 3 axes:
    $$\text{composite\_score} = (\text{throughput\_score} \times 0.3) + (\text{latency\_score} \times 0.3) + (\text{correctness\_score} \times 0.4)$$
    *   *Correctness*: Shadow validator score (0 to 100). Any priority violation results in a demoted verdict (`Logic Violation (LV)`).
    *   *Latency*: $100$ points if P99 latency $\le 500\mu\text{s}$, scaling down linearly to $0$ if P99 $\ge 5\text{ms}$.
    *   *Throughput*: Evaluated based on actual transactions processed per second against target thresholds.
*   **Static Leaderboard JSON Cache**:
    *   *Decision*: A live SQL query fetching the best submissions of 50K contestants on every user refresh would immediately crush PostgreSQL under a 100K-viewer load.
    *   *Solution*: The Gateway runs a periodic background loop (`broadcastActiveLeaderboards()`) every 3 seconds. It runs the window-ranked SQL query, writes a flat JSON file (`leaderboard.json`), and streams updates to active pages via **Server-Sent Events (SSE)**. The frontend reads directly from this static file (which can be cached at the CDN or Nginx level), reducing database queries to near-zero.

---

## 3. Production Deployment & Observability Blueprint

### Kubernetes Resource Layout

Workers scale dynamically based on the load in Redis Streams using standard horizontal pod autoscalers (HPAs) or KEDA:

*   **Stateful Services (Fixed)**: PostgreSQL, Redis, MinIO (pinned to SSD storage classes).
*   **State-Free Services (Auto-scaled)**:
    *   `submission-gateway`: Scales on CPU/Memory usage.
    *   `compilation-worker`: Scales on the number of pending tasks in `compilation_queue`.
    *   `testing-worker`: Scales on the number of pending tasks in `pretest_queue`.

### Observability Telemetry

We utilize a structured custom Prometheus registry to collect metrics at high resolution:
1.  `gateway_http_requests_total`: Tracks request rates and latency across all API routes.
2.  `compilation_queue_depth`: Tracks backlog length of compiled jobs.
3.  `testing_queue_depth`: Tracks backlog of test executions.
4.  `pretest_duration_seconds`: A histogram of total runtimes for sandbox evaluations.
5.  `db_pool_open_connections`: Monitored to prevent database connection exhaustion.
