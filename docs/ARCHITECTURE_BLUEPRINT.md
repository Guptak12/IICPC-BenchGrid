# IICPC 2026 Summer Hackathon
## Distributed Benchmarking & Hosting Platform — End-to-End Design Document
### Architecture Blueprint, Engineering Strategy, and Implementation Reference
*Prepared as the platform's submission deliverable: Architecture Blueprint (per the IICPC problem statement, Deliverable #2).*

---

## 1. Executive Summary
This document is the architecture blueprint for the IICPC 2026 Summer Hackathon platform: a Codeforces-style distributed benchmarking system that ingests contestant-submitted matching-engine binaries (“Bring Your Own Server” — BYOS), builds and isolates them in containers, bombards them with a distributed fleet of trading bots, and produces a graded, multi-axis composite score that drives a live leaderboard.

The system is organized as a set of loosely-coupled services connected through Redis Streams (submission pipeline) and Kafka/Redpanda (high-volume telemetry), backed by PostgreSQL for durable state and S3/MinIO for binary artifacts. Every architectural choice in this document was made against one guiding principle from the judging rubric: every design decision must have an explicit, defensible strategy — not a default.

### 1.1 What Has Been Built
- **Submission Gateway (Fiber/Go)** with auth, rate limiting, arena management, and a real-time SSE leaderboard.
- **Compiler service** that performs Docker image builds from contestant ZIP/Git submissions under a 5-minute timeout.
- **Testing worker (services/testing)** that boots an isolated sandbox container, auto-detects the contestant's wire protocol (TCP/Protobuf, WebSocket, REST+SSE, or FIX 4.4), and drives an in-process bot fleet.
- **Distributed gRPC master/worker Bot Fleet** for post-contest system tests, with three trading strategies (Market Maker, Momentum, Noise) and HDR-Histogram-based latency telemetry.
- **Kafka/Redpanda telemetry pipeline** with a sequence-ordered jitter buffer feeding a sharded shadow order-book validator for correctness scoring.
- **A 0–100 composite scoring engine** (Correctness 40% / Latency 30% / Throughput 30%) with graduated verdicts (Accepted, Logic Violation, Tail Latency Exceeded, Throughput Degradation, etc.).
- **Defense-in-depth sandboxing**: seccomp syscall filtering, dropped Linux capabilities, cgroup CPU/memory caps, dynamic port mapping, and an isolated Docker/K8s network.
- **Infrastructure as Code** for both Docker Compose (local/dev) and Kubernetes (HPA-driven autoscaling for compile and testing workers).

### 1.2 Document Roadmap
Section 2 maps every requirement in the official problem statement and judging criteria to the concrete subsystem that satisfies it. Sections 3–10 describe each major subsystem, the strategic rationale behind it, and how it is implemented. Section 11 documents the reliability engineering pass (bugs found and fixed via cross-reference audits). Section 12 covers IaC/deployment, Section 13 covers the testing strategy, and Section 14 is a consolidated engineering decision log intended to make the “why” behind every choice explicit and easy to defend in front of judges.

---

## 2. Mapping to the Problem Statement & Judging Criteria
The official judging criteria states: “Engineering Design — what choices you make while designing, Strategy and Implementation — and very Important: Clearly Define the Strategy you use for anything.” The table below is the top-level traceability matrix from the problem statement's required components to the implemented subsystem and its governing strategy.

| Problem Statement Requirement | Implemented Subsystem | Governing Strategy (see section) |
| :--- | :--- | :--- |
| **Submission & Sandboxing Engine** — secure, isolated containerized hosting of contestant code | Gateway → Compiler → Testing workers; Docker/gVisor sandboxes with seccomp + cgroups | §4 Submission & Sandboxing |
| **Distributed Load Generator** — thousands of bots, FIX/REST/WebSocket, Limit/Market/Cancel orders | bot-fleet master/worker gRPC cluster; BYOS multi-protocol adapters (TCP_PROTOBUF, WS, REST, FIX) | §5 Bot Fleet & §7 Protocol |
| **Telemetry & Validation Ingester** — p50/p90/p99 latency, max TPS, price-time priority correctness | Kafka/Redpanda pipeline → jitter buffer → HDR Histogram + Shadow Validator (Red-Black tree order book) | §6 Telemetry & Validation |
| **Real-Time Leaderboard & Analytics** — live composite ranking | Postgres ranked queries, SSE broadcaster + static leaderboard.json, dark-themed analytics frontend | §8 Leaderboard & Analytics |
| **Architecture Blueprint deliverable** | This document | — |
| **Infrastructure as Code deliverable** | docker-compose*.yml, k8s/*.yaml, HPA manifests, deploy_k8s.sh | §12 Infrastructure as Code |
| **Working Infrastructure Prototype** (Upload → Deploy → Load Test → Score) | End-to-End pipeline verified via scripts/local_smoke.sh, run_systest.sh, and tests/e2e_platform_test.go | §13 Testing Strategy |

---

## 3. System Architecture Overview
The platform follows an event-driven microservices pattern. Two queueing technologies are used deliberately for two different jobs: Redis Streams for the low-volume, durable submission pipeline (compile → testing), and Kafka/Redpanda for the high-volume, ordered telemetry stream produced during system tests (hundreds of thousands of order/ack/fill events per run).

### 3.1 Service Decomposition
- **Submission Gateway (services/gateway)**: Fiber HTTP API: auth, arena/contest management, rate-limited submission intake, source storage hand-off, leaderboard SSE/JSON, developer dashboard. *Scales on HTTP request rate (replicas: 3, K8s HPA-ready).*
- **Compilation Worker (services/compiler)**: Pulls `compilation_queue`, fetches source (S3 or git clone), runs docker build under a 5-minute timeout, pushes to pretest/systest queue. *Scales on compilation queue depth (HPA: CPU 60%, 1–20 replicas).*
- **Testing Worker (services/testing)**: Pulls `pretest_queue` / `systest_queue`, boots an isolated contestant sandbox container with dynamic port mapping, protocol-detects, runs the in-process bot fleet (k=3 parallel trials), evaluates verdict, writes results. *Scales on Pretest queue depth (HPA: CPU 80%, 1–20 replicas).*
- **Bot Fleet Master/Workers (bot-fleet)**: gRPC master shards a system-test load across N workers; workers run concurrent strategy bots over raw TCP/Protobuf, stream HDR histograms and Kafka telemetry back. *Admin-triggered batch (/admin/rejudge), worker pool sized per system test.*
- **Leaderboard Generator (gateway SSE background tasks)**: Periodically (3s) computes ranked standings from PostgreSQL and republishes via SSE + static JSON for CDN-style scaling. *Fixed; read-path is O(1) via CDN/static file.*
- **PostgreSQL**: Source of truth: submissions, users, arenas, registrations, scores, diagnostics JSONB. *Read replicas (future).*
- **Redis**: Submission-pipeline Streams + consumer groups, rate limiting (SETNX/TTL), bot-fleet job store (write-through cache + rehydration). *Single instance, AOF persistence.*
- **Kafka / Redpanda**: `order-events` topic (6 partitions); carries `ORDER_SENT` / `ORDER_ACK` / `FILL` / `WORKER_DONE` events for systest telemetry. *Partition count tuned for worker parallelism.*
- **S3 / MinIO**: Contestant source ZIPs and compiled artifacts. *Object storage; horizontally scalable.*

### 3.2 End-to-End Flow
A contestant submission moves through the system as follows:
1. Contestant POSTs a ZIP (with Dockerfile) or a GitHub URL to `/api/v1/submit`. The gateway checks a per-contestant Redis rate limit (1/min via SETNX+TTL), persists submission metadata to Postgres, uploads source to S3, and pushes a job onto the `compilation_queue` Redis Stream.
2. The Compilation Worker dequeues the job (consumer-group `XREADGROUP`), fetches the source, runs `docker build -t contestant-<id>` under a 5-minute deadline, and on success pushes a job to the `pretest_queue` (or `systest_queue` for re-judging).
3. The Testing Worker creates an isolated sandbox container on `sandbox-net` with seccomp + cgroup limits, dynamic host-port mapping, inspects the container's `ENGINE_PROTOCOL` env var to select a protocol adapter, and runs k=3 parallel trial fleets against the contestant's port-8000 listener.
4. Each trial's orders/acks/fills are fed into a Red-Black-tree shadow order book (Validator) which independently re-derives the expected fills and compares them to the contestant's actual reports, producing a 0–100 correctness score.
5. Latency (HDR Histogram p50/p90/p99) + correctness + throughput are combined via the composite formula (§9) into a verdict and score, persisted to Postgres.
6. The Leaderboard Generator/SSE broadcaster (every 3s) re-ranks all completed submissions per arena and republishes `leaderboard.json` and an SSE stream consumed by the frontend dashboard.
7. For post-contest System Tests, an admin triggers `/admin/arena/:id/rejudge`. The bot-fleet master shards a much larger load (20–500 bots × 100–20,000 orders) across gRPC workers, streams telemetry via Kafka, and the master's Kafka consumer + shadow validator computes the final official score, written back to the same submissions row.

**Why two queueing systems?**
Redis Streams give the submission pipeline durability, consumer groups, and a Pending-Entry-List (PEL) recovery mechanism (`services/common/pel_recovery.go` reclaims messages idle > N minutes) at very low operational cost — ideal for the relatively low-throughput compile/testing queues (a few submissions per minute, rate-limited at the gateway). Kafka/Redpanda, by contrast, is purpose-built for the systest telemetry firehose: tens to hundreds of thousands of small JSON events per run, requiring OrderID-keyed partitioning (so all events for one order land on the same partition and preserve ordering), async batched producers that never block the bot hot path, and a single consumer group per job for clean replay semantics. Using Kafka for the submission pipeline would be operational overkill; using Redis Streams for systest telemetry would create a single-threaded bottleneck on the consumer side and lose partition-level ordering guarantees.

---

## 4. Submission & Sandboxing Engine
This subsystem satisfies the problem statement's first requirement: a secure pipeline where contestants upload source/binaries (C++, Rust, Go, etc.), and the platform containerizes and deploys them in strictly isolated environments with CPU pinning and memory limits.

### 4.1 Bring-Your-Own-Server (BYOS) Strategy
**Strategy**: rather than constrain contestants to a fixed SDK/header file, the platform accepts a self-contained container image (ZIP with Dockerfile, or a Git URL). This maximizes the "complete freedom to select their tech stack" requirement from the problem statement while keeping the platform language-agnostic. The only contract is network-level (`Protocol.md`): the contestant's container must bind `0.0.0.0:8000` and speak one of four wire protocols, selected via the `ENGINE_PROTOCOL` Docker ENV var (`TCP_PROTOBUF` [default], `WS`, `REST`, `FIX`). This is validated by five reference implementations under `test_payloads/` (Go, C++, Python, Rust, Node) plus `go_ws` / `go_rest` / `go_fix` adapters, all of which pass the full pipeline (§13).

### 4.2 Gateway: Intake, Rate Limiting, Storage
- **Rate limiting** (`services/gateway/ratelimit.go`): an atomic Redis `SETNX` with a 60s TTL keyed on `contestant_id` (or `user_id` when authenticated). This is O(1), survives gateway restarts (no in-memory token buckets to lose), and the TTL doubles as the natural “retry-after” value returned to the client.
- **Source storage**: ZIPs are streamed to MinIO/S3 under `submissions/<build_id>/submission.zip`; only the S3 key (not the binary) is stored in Postgres, keeping the hot table (`submissions`) lean — see migration `00003_separate_source_code.sql`, which moved raw source text into a dedicated `submission_sources` TOAST table.
- **Auth**: JWT-based (HS256) with optional GitHub OAuth; `contestant_id` defaults to the authenticated handle so the same person's resubmissions are correctly attributed for leaderboard de-duplication (§8).

### 4.3 Compilation Worker
- **Bounded build time**: `docker build` runs under `context.WithTimeout(5*time.Minute)`. On timeout the verdict is explicitly “Build Timeout” rather than a generic failure, giving contestants actionable feedback.
- **Zip-slip protection**: `extractZip()` canonicalizes every archive path with `filepath.Clean` and verifies it remains inside the build directory before writing — a deliberate defense against path-traversal attacks via crafted ZIPs.
- **Retry / Dead-Letter**: on system error, `ShouldRetry()` (`services/common/retry.go`) re-queues up to `MaxRetries=3` with an incrementing `retry_count` field; beyond that the message is moved to a `dead_letter_queue` stream for manual inspection, rather than being silently dropped or retried forever.
- **Optional registry push**: if `REGISTRY_URL` is set, the built image is tagged and pushed, enabling the production Kubernetes pivot (§12.3) where pretest workers schedule pods instead of local containers.

### 4.4 Sandbox Isolation Model
Defense is layered — no single control is relied upon exclusively:
- **Process**: seccomp profile (`services/common/seccomp.go`): `SCMP_ACT_KILL_PROCESS` on `fork`/`vfork`; `SCMP_ACT_ERRNO` on `connect`/`socketpair`, `ptrace`, `setuid`/`setgid` family, `mount`/`umount2`/`pivot_root`/`chroot`, module loading, `iopl`/`ioperm`. `fork`/`vfork` killed outright — contestant code cannot spawn subprocesses to escape monitoring. `connect`/`socketpair` return `EPERM` instead of killing the process, so `std::thread` (used by `hidden_server.cpp`) and normal `listen`/`accept` on 8000 keep working, but outbound connections are silently refused. `clone` is intentionally allowed because the harness uses `std::thread`.
- **Capabilities**: `CapDrop: ["ALL"]`. No raw sockets, no `CAP_NET_ADMIN`, no `CAP_SYS_*`; the contestant binary runs with the minimum privilege needed to bind one TCP port.
- **Resources (cgroups)**: 1–2 CPU cores, 256–512MB memory, `PidsLimit` 128–2048. Prevents fork-bomb / memory-exhaustion DoS against the host; CPU pinning ensures latency measurements aren't skewed by noisy-neighbor contention.
- **Network**: Isolated bridge network (`sandbox-net`); `NetworkPolicy` restricts ingress to TCP/8080 (K8s) or 8000 (Docker) from the `bot-fleet` namespace only; egress denied (`egress: []`). Contestant code cannot exfiltrate data, scan the internal network, or reach cloud metadata endpoints. The only legitimate traffic is `bot-fleet` ↔ `sandbox` on the contract port.
- **Filesystem**: Runtime image based on `gcr.io/distroless/cc-debian12` (no shell, no package manager). Even if a contestant binary is compromised by malicious input, there is no `/bin/sh` to pivot to.
- **Networking model**: Dynamic Port Mapping: container exposes `8000/8080` → bound to host port `"0"` (Docker allocates a free ephemeral port). Eliminates port collisions for concurrent pretest runs (k=3 parallel trials per submission, many submissions in flight) without requiring host networking, which was empirically shown to trigger `SIGSYS` (exit 159) under the seccomp profile (§11.1).

### 4.5 Resolved Issue — SIGSYS / Exit 159
Early pretest runs exited immediately with status 159 (SIGSYS, “Bad System Call”) and no logs. Root cause: host-network mode combined with `CapDrop:["ALL"]` and the seccomp profile caused runc/glibc's namespace-setup path to invoke a blocked syscall before `main()` was reached. The fix was architectural, not a seccomp exception: move to the isolated `sandbox-net` bridge network with Dynamic Port Mapping (§4.4) and split the sandbox image into a `CompileImage` (Debian Bookworm, has g++) and a slimmer `RuntimeImage` (Distroless). This both fixed the crash and tightened the runtime attack surface.

### 4.6 Identified Gap — Compiler Host-UID Regression (Open)
A proposed architecture upgrade (currently under review) suggested running the compile step under the host UID to resolve a permission conflict between the contestant's build output and the pretest worker's bind-mount. This was flagged during audit as a security regression: running `docker build` as the host's UID widens the blast radius of a malicious Dockerfile (e.g. a build step that writes setuid binaries to a mounted path could affect the host user's files). The platform's current stance is to keep compile and runtime containers fully namespaced and resolve the permission mismatch via `chmod 777 submissions/` on the shared scratch directory (as already done in `local_smoke.sh` / `run_e2e_tests.sh`) rather than relaxing container UID isolation. This remains an open item for the production (S3-only, no shared filesystem) migration in §12.3.

---

## 5. Distributed Load Generator (Bot Fleet)
The bot fleet is the platform's “engine of the platform” per the problem statement: it must spawn thousands of distributed bots simulating diverse market participants. Two execution modes exist by design, scaled to two very different jobs:

| Metric / Attribute | Pretest (in-process) | System Test (distributed gRPC) |
| :--- | :--- | :--- |
| **Bots** | 5 | 20–500+ |
| **Orders/bot** | 100–200 | 100–20,000 |
| **Total orders** | 500–1,000 | 25K–400K+ |
| **Topology** | Single goroutine pool inside the testing worker | gRPC master shards work across N workers (worker-1..N) |
| **Telemetry** | None (zero infra dependency, fast feedback) | Kafka/Redpanda async pipeline + HDR Histogram streamed back via gRPC |
| **Goal** | Fast (~3–5s) deterministic correctness/latency signal during the contest | Authoritative, high-concurrency stress test post-contest, drives final ranking |

**Strategy rationale**: requiring Kafka for every 5-bot pretest would add a hard infrastructure dependency to the fast-feedback loop contestants see during the contest window. By keeping pretests fully in-process (D9 in the original decision log) and reserving the distributed/Kafka path for system tests, the platform gets both: sub-5-second pretest turnaround at near-zero infrastructure cost, and an authoritative, horizontally-scalable systest path for final grading.

### 5.1 Master/Worker gRPC Topology
The master (`mode=master`) exposes an HTTP API (`/run`, `/status/:id`, `/cancel/:id`) and shards a `FleetConfig` across the configured `WORKERS` (`worker-1:5001`, `worker-2:5002`, `worker-3:5003` in dev). Each worker runs a gRPC `WorkerService` with a single server-streaming RPC, `RunShard`, defined in `bot-fleet/proto/fleet.proto`.
- **Streaming protocol**: workers send a heartbeat `ShardResult` (`IsFinal=false`) every 2 seconds to keep idle gRPC/Docker connections alive during long load tests, then a final message (`IsFinal=true`) carrying the gob-serialized HDR Histogram (`bot-fleet/histogram.go`).
- **Global OrderID uniqueness**: each worker receives a `BotIdOffset` (workerIndex × botsPerWorker); a bot's `NumericID = offset + i + 1`, and `OrderIDs` are encoded as `(NumericID << 32) | sequence`. This guarantees collision-free, globally unique, bot-attributable order IDs across the whole distributed fleet — critical for the shadow validator's self-cross detection (§6.3).
- **Deterministic seeding**: a base `Seed` (from the request, or `time.Now().UnixNano()` if 0) is combined as `baseSeed + botIdOffset + i` per bot. Every bot gets a unique-but-reproducible RNG stream, enabling exact replay of a system test for debugging (verified by `god_test.go`'s `TestCryptographicReplayHash`).
- **HDR Histogram aggregation pattern**: per-bot → shard (worker-level `Merge` in `worker_node.go`) → global (master-level `Merge` in `runner.go`). This three-tier merge means the master never needs raw latency samples — only O(1)-sized histogram snapshots cross the network — while still producing mathematically correct global percentiles (HDR histograms are merge-associative).

### 5.2 Trading Strategies
Three `StrategyType` values drive the `Strategy` interface (`bot-fleet/strategy.go`), which controls WHEN a bot sends — never WHAT it sends (order generation is handled separately by `Bot.NextOrder` in `bot.go`). This separation lets the same order-generation logic be reused across pretest and systest.
- **Market Maker**: `rate.Limiter` with `burst=2` (paired BUY+SELL quotes). Always LIMIT, alternating BUY/SELL around mid-price ±spread/2 with small randomized variation. Simulates a passive liquidity provider quoting both sides of the book continuously.
- **Momentum Trader**: Custom `burst=16` cycle: sleeps for the exact refill time of 16 tokens (+ up to 20% jitter), then fires 16 orders back-to-back. 80% MARKET (aggressive, side picked 50/50), 20% LIMIT near mid; every 5th order is a CANCEL of an active resting order. Simulates a trend-follower that is quiet, then bursts aggressively — stress-tests burst handling and cancel-path correctness.
- **Noise Trader**: `rate.Limiter` with `burst=10`, randomized inter-arrival (jittered around 1/rate). 60% LIMIT (price = mid ± up to 10%), 25% MARKET, 15% CANCEL; quantity drawn from a Zipf-like distribution capped at 10,000. Simulates retail/noise flow — broad price dispersion and heavy-tailed order sizes that exercise many price levels of the book.

Strategy mix is configurable per run via `market_maker_pct` / `momentum_pct` / `noise_pct` (default 40/30/30), and the bot fleet's `StrategyBreakdown` is persisted into diagnostics so contestants can see per-strategy order counts and latency in the analytics dashboard (§8).

### 5.3 Two-Tier Latency Measurement
A core engineering decision was to measure latency from two independent vantage points so the platform (and contestants) can distinguish network/queueing overhead from engine-internal compute time:
- **Wire RTT (client-observed)**: `RecordSendTime()` captures `time.Now().UnixNano()` at the moment bytes leave the socket (`net.Buffers.WriteTo`), stored in a per-bot, append-only `SendTimes` slice indexed by sequence number. When the matching ack/fill frame is read back, latency = `receivedAt - sendTime`. This is the number used for the platform's p50/p90/p99 scoring.
- **Engine-internal (contestant-reported)**: the protobuf `ExecutionReport` carries `processing_ns`, which contestants populate using `CLOCK_THREAD_CPUTIME_ID` (excludes network I/O). The platform records this into a separate `engineHist` HDR Histogram (`telemetry/consumer.go`) and surfaces `engine_reported_p99_us` in diagnostics, letting contestants prove “my matching loop is fast even if the harness/network adds overhead.”

**Resolved bug — “time-travel” latency**: `SendTimes` was originally populated at order-generation time (inside `NextOrder`), but the order is only actually written to the socket later (after proto marshalling and pool round-trips). For CANCEL orders in particular this produced negative latencies. The fix moved `RecordSendTime()` to the exact point of `net.Buffers.WriteTo()` in the sender goroutine, establishing the invariant that latency must be measured at actual wire send, never at logical generation time.

**Open finding (cross-reference audit)**: the `SendTimes` slot is indexed by `seq & 0xFFFFFFFF`. Because CANCEL messages are generated with a fresh sequence number from the same ring (§5.4), a CANCEL and a later regular order can theoretically reuse the same low-32-bit slot within a single bot's `OrdersToSend` budget, causing the slot to be overwritten before its ack returns and producing a spurious negative latency on the cancel's ack. The recommended fix (not yet merged) is to size `SendTimes` to `OrdersToSend` and never reuse a slot, or to store `(sendTime, generation)` pairs and validate the generation on read.

### 5.4 Concurrency Architecture per Bot
Each bot (`runBot` in `bot-fleet/runner.go`) runs three goroutines plus a coordinator:
- **Sender**: marshals protobuf orders into a 4-byte little-endian length-prefixed frame using `sync.Pool`-backed buffers (`orderPool`, `payloadBufPool`) for near-zero allocation in the hot path (verified by `god_test.go`'s `TestStrictZeroAllocAssertion`), and tracks pending acks in a 16-way sharded map (`bot-fleet/pending.go`) to avoid mutex contention under high concurrency.
- **Receiver**: reads length-prefixed `ExecutionReport` frames, distinguishes ACK vs FILL frames (`isAckStatus` / `isFillStatus`), and only counts `ACK`/`REJECT`/`CANCEL` frames against `OrdersSent` — `FILL` frames are reported separately to Kafka but do not double-count as a completed order. This was explicitly unit-tested (`runner_test.go: TestRunBotDoesNotCountFillFramesAsAcks`) after `FILL` frames were initially mis-counted as additional acks.
- **Control**: a 100ms ticker that expires pending acks after a 5s `ackTimeout`, and terminates the connection once the sender is done, no acks are pending, and a 250ms fill-drain grace period has elapsed with no new data — avoiding both premature disconnects (losing in-flight fills) and indefinite hangs (a single late fill keeping the connection open forever).

**Resolved issue — send/receive deadlock**: an earlier synchronous send-then-receive-then-send loop could deadlock if the contestant engine buffered acks (TCP backpressure on the receive side blocked the send side). Splitting into independent sender/receiver/control goroutines per bot, communicating only via a buffered `latencyCh` and the sharded pending table, removed this class of deadlock entirely.

---

## 6. Telemetry & Validation Ingester
This subsystem is the platform's “low-latency tracking system” — it must accurately measure p50/p90/p99 ack latency, max sustained TPS, and price-time-priority correctness.

### 6.1 Kafka/Redpanda Pipeline
- **Single topic, partitioned by order**: `order-events` (6 partitions, pre-created via `rpk topic create order-events -p 6` to avoid an `UNKNOWN_TOPIC_OR_PARTITION` race on cold Redpanda startup).
- **Partition key = OrderID** (`kgo.StickyKeyPartitioner`): guarantees every `ORDER_SENT` / `ORDER_ACK` / `FILL` event for a given order lands on the same partition, preserving per-order ordering even with multiple producers (one per bot-fleet worker).
- **Producer** (`telemetry/producer.go`): async, fire-and-forget for `ORDER_SENT`/`ACK`/`FILL` (never blocks the bot hot path), 20ms linger for batch efficiency, 100K-record in-memory buffer with graceful degradation if Kafka is briefly unavailable (`KafkaUnavailable` flag surfaces this to the report). The only synchronous call is `PublishWorkerDone`, which flushes all buffers before sending the terminal `WORKER_DONE` marker — this is the signal the master's consumer uses to know a worker has fully drained.

**Resolved issue — wrong topic for fills**: `FillEvent` was originally published to a separate `fill-events` topic that no consumer subscribed to, silently breaking the jitter buffer (no `FILL` events ever arrived) and zeroing out correctness scores. The fix routes all event types (`ORDER_SENT`, `ORDER_ACK`, `FILL`, `WORKER_DONE`) to the single `order-events` topic that the consumer actually subscribes to (`kgo.ConsumeTopics(TopicOrderEvents)`).

### 6.2 Sequence-Ordered Jitter Buffer
Kafka guarantees ordering only within a partition, and even within a partition, network jitter across multiple producing workers can reorder events relative to the contestant engine's own monotonic `engine_seq_id`. The Consumer (`telemetry/consumer.go`) buffers incoming Ack/Fill events keyed by `EngineSeqID` in a `jitterBuffer` map and only feeds them to the HDR Histogram and Shadow Validator in strict ascending `nextSeqID` order.
- **Gap handling**: if the buffer accumulates ≥ `jitterGapThreshold` (100) entries while waiting for `nextSeqID`, the consumer assumes that sequence number was dropped by the contestant's engine (not merely delayed), logs it, and advances `nextSeqID` — preventing an unbounded buffer and an infinite stall.
- **Quiet-period finalization**: `Consume()` polls until all expected `WORKER_DONE` markers have arrived and 3 seconds pass with no new messages, then `flushRemainingBuffer()` drains any remaining buffered events in sequence-ID order (sorted) before computing final statistics — ensuring no buffered tail data is silently discarded.
- **Initialization detail**: `nextSeqID` starts at 1 (matching the C++ engine's 1-based `seq_id`), not 0 — an earlier off-by-one created a permanent 1-event gap at the start of every run that the gap-detection logic eventually papered over, but only after wasting the full jitter window.

### 6.3 Shadow Validator — Sharded Red-Black-Tree Order Book
The Validator (`bot-fleet/shadow/validator.go`) independently re-implements price-time-priority matching using a red-black tree per side (bids descending, asks ascending; `github.com/emirpasic/gods`), with FIFO linked lists at each price level. As the consumer feeds it `ORDER_SENT`/`ACK`/`FILL` events in engine-sequence order, it computes the expected fills and diff's them against the contestant's actual fills.
- **Sharding**: The Validator is sharded by symbol (currently a single “BTCUSD” shard, but the architecture supports N) via `SymbolShard`, each with its own mutex — a strategy chosen so future multi-symbol contests can validate independently without lock contention across symbols.
- **Self-Cross Prevention**: Both the shadow book and every reference engine (`go_optimized`, `cpp_basic`, `go_ws`, `go_rest`, `go_fix`) implement the same rule: `isSelfCross(incomingID, restingID) := botID(incoming) == botID(resting)`, where `botID(orderID) = orderID >> 32`. A bot can never trade against its own resting order; the matching loop skips such orders and continues to the next price-time-priority candidate (verified by `god_test.go`'s `TestAdversarialInjectionProfile` and `validator_test.go`'s `TestValidatorSelfCross{Prevented,SkipsToNextBot,InfiniteLoopPrevented,MixedLevelMatchesOtherBot}`).
- **Anti-Cheat Checks**: The validator was hardened against several classes of “cheating” engine behavior, each backed by a dedicated test:
  - `matched_with: 0` bypass — an engine reporting `matched_with=0` to skip counterparty verification is penalized (`TestValidatorMatchedWithZeroBypass`).
  - Wrong-counterparty fills — correct price/qty but wrong `matched_with` (violating time priority) reduces the priority sub-score (`TestValidatorMatchedWithWrongCounterparty`).
  - Phantom fills — fills reported for non-existent orders, or quantities exceeding what was expected, are tracked as `phantomQty` and subtract up to 25 points from the correctness score.
  - Folded partial fills — consecutive partial fills at the same price/counterparty are folded into one logical execution block (`foldPartials`) before comparison, so an engine that legitimately splits one match into several small TCP-frame-sized reports isn't penalized for “extra” fills (`TestValidatorFoldsConsecutivePartialFills`).
  - Cancel-Ack race — `ProcessOrder()` ignores `CANCEL` requests entirely for `pendingOrders` bookkeeping (a `CANCEL` is a book mutation, not a new in-flight placement); `ProcessAck()` handles "cancelled" status by directly removing the resting order. This eliminated a map-overwrite race where a fast `LIMIT`-then-`CANCEL` pair from the same bot caused the validator to lose track of the original `LIMIT` order (`TestValidatorCancelRemovesRestingOrder`).

**Correctness Score Formula**:
`GetCorrectnessScore()` combines four weighted components, each derived from per-order expected-vs-actual fill comparisons across all shards:
- Quantity Score (70%): `min(priceCorrectQty, expectedQty) / expectedQty` — fraction of expected fill volume that was filled at the correct price.
- Priority Score (20%): `min(priorityCorrectQty, expectedQty) / expectedQty` — fraction filled against the correct counterparty in the correct order.
- Value Score (10%): `1 - (|expectedValue - actualValue| / expectedValue)` — penalizes price/quantity mismatches that don't show up in the above two.
- Penalties: up to -25 points scaled by `phantomQty/expectedQty` for phantom fills, and -2 points each for ack violations, duplicate orders, and unknown acks.
If no fills were expected at all, the score is 100 unless the engine produced phantom fills or ack/duplicate/unknown violations, in which case it is 0 — ensuring an engine cannot earn a perfect score by simply doing nothing.

---

## 7. Multi-Protocol Contract (BYOS)
To satisfy “contestants have complete freedom to select their tech stack” while keeping the bot fleet's order-generation and scoring logic protocol-agnostic, the platform defines one logical message pair (Order, ExecutionReport) and four wire encodings, auto-detected from the contestant's Dockerfile `ENV ENGINE_PROTOCOL`.

- **TCP_PROTOBUF (default)**: Raw TCP, 4-byte LE length prefix + protobuf (`pkg/protocol/trading.proto`). Mapped to `TCPProtobufAdapter`. Latency Target: 50ms / Ceiling: 500ms.
- **WS**: WebSocket text frames, JSON Order/ExecutionReport. Mapped to `WebSocketAdapter`. Latency Target: 100ms / Ceiling: 1000ms.
- **REST**: POST `/api/v1/orders` (JSON) + GET `/api/v1/events` (SSE). Mapped to `RESTAdapter` (50 worker pool for POSTs). Latency Target: 150ms / Ceiling: 1500ms.
- **FIX**: FIX 4.4 over raw TCP; Logon(A)/NewOrderSingle(D)/Cancel(F)/ExecutionReport(8); custom tags 9000=processing_ns, 9001=matched_with. Mapped to `FIXAdapter`. Latency Target: 50ms / Ceiling: 500ms.

**Strategy**: per-protocol targets/ceilings reflect each transport's inherent overhead (a JSON+SSE round trip is structurally slower than raw protobuf over TCP), so the latency score (§9) doesn't unfairly punish a contestant for choosing REST over raw TCP — the grading curve shifts with the protocol. In addition, when ≥ 10% of accepted runs exist for a protocol, the target/ceiling dynamically recalibrate to 1× / 10× the current top-10% p99 for that protocol (`services/testing/verdict.go`), so the bar rises as the field improves, with absolute floors (target ≥ 100μs, ceiling ≥ target×10) preventing runaway contraction.

**Order ID contract**: `order_id = (bot_id << 32) | sequence` for both the bot fleet and every reference engine. This single convention is what makes self-cross detection (§6.3) work uniformly across all four protocols without per-protocol special-casing.

**Documented gap — Cancel protocol ambiguity**: `Protocol.md` and the FIX adapter define cancels via a fresh `ClOrdID` (`cancelClOrdID`) referencing the target via `OrigClOrdID` (tag 41), but the `TCP_PROTOBUF` / `WS` path sends a `CANCEL` Order whose own `order_id` is the ID of the order to be cancelled (no separate reference field). This inconsistency is called out explicitly as an open documentation item: the contestant-facing `Protocol.md` should state, for every protocol, whether “cancel” means “resend the target's order_id with type=CANCEL” or “send a new order_id referencing the target” — currently only the FIX section documents the latter.

---

## 8. Real-Time Leaderboard & Analytics
Per the problem statement, this is the “frontend interface that streams live metrics from the ongoing stress tests, ranking contestants dynamically based on a composite score of speed, stability, and algorithmic accuracy.”

### 8.1 Ranking Query & Best-of-N
`generateLeaderboardData()` (`services/gateway/main.go`) issues a single windowed SQL query per arena: `ROW_NUMBER() OVER (PARTITION BY COALESCE(user_id, contestant_id) ORDER BY (verdict = 'Accepted') DESC, composite_score DESC, updated_at ASC)`, keeping each contestant's best 2 submissions. The leaderboard shows rank 1 (their best run) and derives `DeltaScore` / `DeltaP99` against rank 2 only if that second run is < 10 minutes old — giving contestants a live “did my last change help?” signal without showing stale deltas from hours-old runs. Accepted runs are always ranked above any failed verdict regardless of raw score, matching the “gold standard” gate in §9.

**Resolved issue — ContestantID dropped on cancel**: an earlier `handleCancel` implementation constructed a replacement Job struct without copying `ContestantID`, so cancelled jobs vanished from per-contestant views. The fix explicitly copies every field (including `ContestantID`) when building the replacement Job — a recurring pattern in `main.go`'s “copy-on-write” job model (§8.2).

### 8.2 Copy-on-Write Job Store
The bot-fleet master's in-memory `jobStore` (`map[string]*Job`, guarded by `sync.RWMutex`) never mutates a Job in place. Every state transition (Pending → Running → Completed/Aborted) builds a new `*Job` value (copying all fields) and atomically replaces the map pointer via `replaceJob()`. This eliminates an entire class of pointer-aliasing races: any goroutine holding an old `*Job` continues to see a consistent snapshot, never a partially-updated struct.
- **Write-through + rehydration**: `replaceJob()` also (best-effort, non-blocking) writes the JSON-serialized Job to a Redis hash (`HSET jobs ...`). On master restart, `rehydrateFromRedis()` reloads all jobs and sanitizes any that were left in Pending/Running (“zombie” state — their owning goroutine died with the process) by marking them Aborted with “system restarted while job was active”, so a crash never leaves a job stuck “running” forever in the UI.

**Open structural item (architectural audit)**: this is currently a single in-memory store with a Redis write-through cache, which is adequate for the current single-master deployment but does not by itself support multiple master replicas. The migration path is to make Redis (or Postgres) the primary store and the in-memory map a read-through cache, removing the rehydration-on-boot step entirely.

### 8.3 Frontend
- **Public Standings**: dark “industrial utilitarian + luxury minimal” theme (`frontend/app.js`), real-time contestant-ID search, sortable columns, tick-flash animations on score change, per-row SVG sparklines synthesized from `score_history`/`delta_score`, and an anonymize toggle (`obfuscateId`) for privacy-sensitive public displays.
- **Personal Run Diagnostics**: a slide-out drawer with three radar-chart axis gauges (Correctness/Latency/Throughput vs. a 100-point SLA target overlay), a latency percentile line chart (this run vs. Top-10% average vs. SLA limit), a TPS-over-time chart, and an Orderbook Correctness Inspector listing priority violations, phantom fills, and trade discrepancies.
- **Delivery**: SSE for live updates (`handleGetLeaderboardStream`, 15s keep-alive pings) plus a periodically-rewritten static `leaderboard.json` for the 100K-viewer CDN read path — satisfying the scaled-platform design's requirement that “read requests are isolated using static CDN-hosted feeds to protect database infrastructure.”

---

## 9. Composite Scoring System
The canonical formula (`pkg/scoring/scoring.go`), applied identically to pretests and system tests:
$$\text{Composite Score} = \text{Correctness} \times 0.40 + \text{LatencyScore} \times 0.30 + \text{ThroughputScore} \times 0.30$$
All inputs and the output are bounded to [0, 100]; the result is rounded to 2 decimals.

### 9.1 Latency Score — Weighted Percentile Decay
Rather than scoring on p99 alone, `LatencyScore` computes an independent 0–100 “bucket” score for p50, p90, and p99 (100 at ≤ target, linear decay to 0 at ≥ ceiling, default target = 500μs / ceiling = 5000μs for systests), then combines them with a tail-weighted average:
$$\text{Latency Score} = 0.20 \times s(p50) + 0.30 \times s(p90) + 0.50 \times s(p99)$$
The 50% weight on p99 is a deliberate strategy choice: a matching engine that is fast on average but has an occasional 50ms GC pause or lock-contention spike (a “tail latency” problem that's invisible in p50) is heavily penalized, which mirrors real exchange SLAs where worst-case behavior matters more than average behavior. `DynamicLatencyScore()` generalizes this with the per-protocol target/ceiling described in §7.

### 9.2 Throughput Score
Two formulas exist for two contexts, both producing 0–100:
- **System Test** (`master.go` / scaled design): $\text{ThroughputScore} = (1 - \text{failRate}) \times 100$, where $\text{failRate} = \frac{\text{OrdersFailed}}{\text{OrdersSent}}$.
- **Pretest/Systest verdict engine** (`services/testing/verdict.go`): $\text{ThroughputScore} = 0.5 \times \text{StabilityScore} + 0.5 \times \text{MaxTPSScore}$. `StabilityScore` applies a logarithmic decay (100 → 0 as `failRate` goes from 0 to a max-allowed `failRate` — 0.1% for systests, or 5 orders for pretests) so the first few failures are penalized gently but the score collapses quickly past the SLA threshold. $\text{MaxTPSScore} = \min\left(\frac{\text{MaxSustainedTPS}}{\text{TargetTPS}}, 1.0\right) \times 100$, where `MaxSustainedTPS` is the highest 1-second bucket with zero failures — rewarding engines that can sustain bursts cleanly rather than just averaging out a few drops over a long window.

### 9.3 Verdict Gates (Strict Priority Order)
1. **Correctness**: `Correctness < 100%` $\rightarrow$ **Logic Violation (LV)** — “Order Book Math Mismatch”
2. **Failure rate**: Systest: `failRate > 0.1%`; Pretest: `OrdersFailed > 5` $\rightarrow$ **Correctness Error** — engine dropped/rejected orders
3. **Tail latency**: `p99 > ceiling` (protocol-specific, dynamically recalibrated) $\rightarrow$ **Tail Latency Exceeded (TLE)**
4. **Degradation**: `TPS degradation > 30%` from start to end of run $\rightarrow$ **Throughput Degradation**
5. **All pass** $\rightarrow$ **Accepted** — “Optimal Execution (Passes all SLAs)”

**Post-contest reduced-score policy**: if a system-test score is lower than the pretest score for the same submission, the leaderboard uses the system-test score — the contestant keeps their position/history, but rank drops naturally. This avoids disqualification for partial regressions under heavier load while still rewarding engines that hold up at scale.

### 9.4 Multi-Trial Stability Bonus
Each evaluation runs k=3 independent trials in parallel (`errgroup`), each with a different seed (`baseSeed + run×1000`) and its own ephemeral sandbox container. Metrics are averaged across trials for the final verdict; additionally, the standard deviation σ of the three trials' composite scores is computed, and if σ < 2.0, a +5.0 Stability Bonus is added (capped at 100). This rewards deterministic engines that avoid GC-pause or lock-contention variance — directly addressing the judging criteria's interest in engineering robustness, not just a single lucky run.

### 9.5 Engine Archetype Classification
`classifyArchetype()` (`services/testing/verdict.go`) assigns a behavioral label purely from the three sub-scores, surfaced on the leaderboard and in the analytics drawer:
- **Latency-Optimized**: LatencyScore ≥ 70 and Correctness < 85 — fast but cutting correctness corners.
- **Accuracy-Optimized**: Correctness ≥ 95 and LatencyScore < 30 — textbook-correct but slow.
- **Low-Throughput**: ThroughputScore < 70 — struggles under concurrent load regardless of correctness/latency.
- **Balanced**: Correctness ≥ 80, LatencyScore ≥ 30, ThroughputScore ≥ 80.
- **Unclassified**: anything else — deliberately conservative so the label set stays meaningful.

---

## 10. Consolidated Security Architecture
Security controls are introduced across multiple sections; this section consolidates them into a single threat-model view for the “strictly isolated environments” requirement.

| Threat | Mitigation | Where Implemented |
| :--- | :--- | :--- |
| **Contestant code spawns subprocesses / escapes container** | seccomp `SCMP_ACT_KILL_PROCESS` on `fork`/`vfork`; `CapDrop:["ALL"]`; Distroless runtime image with no shell | `services/common/seccomp.go`; `test_payloads/*/Dockerfile` (multi-stage builds) |
| **Contestant code exfiltrates data / scans internal network** | Isolated `sandbox-net` / `iicpc-sandboxes` namespace with `egress: []` `NetworkPolicy`; seccomp `ERRNO` on `connect`/`socketpair` as a second layer | `k8s/sandbox-networkpolicy.yaml`; `DESIGN_K8S_PRODUCTION.md` §5; `seccomp.go` |
| **Resource exhaustion** (fork bombs, memory leaks, infinite loops) | cgroups: 1–2 CPU / 256–512MB / `PidsLimit` 128–2048; 5-minute compile timeout; pretest sandbox sweeper (30-min orphan GC) | `services/testing/main.go` (`startContestantSandbox`, `startDockerSweeper`) |
| **Malicious ZIP (path traversal / zip-slip)** | `filepath.Clean` + prefix check before extraction | `services/compiler/worker.go` (`extractZip`) |
| **Rate-limit abuse / submission flooding** | Atomic Redis `SETNX`+`TTL` per contestant/user | `services/gateway/ratelimit.go` |
| **Unbounded payload from contestant** (DoS via huge frame) | *Open finding*: `readFrame()` in the bot-fleet receiver allocates a buffer sized directly from the untrusted 4-byte length prefix with no upper bound — recommended fix is a max-frame-size constant (e.g. 1MB) with immediate connection drop on violation | `bot-fleet/runner.go` receiver goroutine (remediation pending) |
| **Uncaught exceptions in contestant on_order() crash container** | *Open finding*: a single malformed order can take down the engine process, failing all subsequent orders. Recommended: harness exception isolation so one bad order degrades gracefully rather than zeroing the whole trial | Harness (`hidden_server.cpp`) — remediation pending |
| **Source code privacy** | Source code endpoint restricted to the owner or admin until the arena status = 'ended' | `services/gateway/main.go` (`handleGetSource`) |
| **Secrets in code** | JWT secret / S3 / DB creds via env variables with `GetEnv()` fallbacks; production deployment moves these to Kubernetes Secrets | `services/common/*.go`; `DESIGN_K8S_PRODUCTION.md` §6 |

---

## 11. Reliability Engineering: Audit Findings & Resolutions

### 11.1 Cross-Reference Audit — Resolved
- **Fill events misrouted to fill-events topic** $\rightarrow$ correctness always 0 (§6.1) — Resolved.
- **Baseline C++ engine only emitted taker-side fills; shadow validator expects both taker and maker fills** — Resolved by updating reference engines (`test_payloads/cpp_basic`, `go_optimized`, etc.) to emit a fill report to both sides of every match.
- **Cancel-Ack race overwriting pendingOrders** (§6.3) — Resolved.
- **Latency “time-travel” from SendTimes captured at generation vs. send time** (§5.3) — Resolved.
- **nextSeqID off-by-one creating a startup gap** (§6.2) — Resolved.
- **Mutex scope too wide in hidden_server.cpp**, including I/O inside the critical section, which made latency measurements for that harness meaningless — Resolved by narrowing the lock to only the order-book mutation.
- **FILL frames double-counted as ACKs in runBot** — Resolved, with regression test `runner_test.go::TestRunBotDoesNotCountFillFramesAsAcks`.
- **ContestantID dropped on job cancel** (§8.1) — Resolved.

### 11.2 Cross-Reference Audit — Open Items
- **SendTimes slot reuse on CANCEL orders can produce a negative latency** (§5.3) — needs slot-sizing or generation-tagged sends.
- **Uncaught C++ exceptions in on_order() terminate the entire harness container** (§10) — needs per-order exception isolation.
- **Unbounded readFrame() allocation from an untrusted length prefix** (§10) — needs a max-frame-size guard.
- **CurrentTps in ShardResult heartbeat messages** is defined in the proto but never populated by workers — the master's UI cannot show live per-shard TPS during a run, only at completion. Fix: compute a rolling `totalSent` delta inside the 2-second heartbeat goroutine in `worker_node.go` and set `ShardResult.CurrentTps` before each heartbeat Send().
- **Per-contestant rate limiting on /submit** — Resolved (`services/gateway/ratelimit.go`).

### 11.3 Senior-Architect Structural Audit — Findings & Status
1. **Monolithic main.go “god object” in the bot-fleet master**: mixes HTTP handling, job orchestration, Kafka wiring, DB writes, and scoring. *Status: Partially mitigated (scoring extracted to `pkg/scoring`, telemetry to `bot-fleet/telemetry`, remaining HTTP/orchestration split is planned).*
2. **In-memory-only job store vulnerable to crash data loss**: *Status: Mitigated via Redis write-through + rehydration (§8.2); full migration to Redis/Postgres-as-primary is the recommended next step.*
3. **Absent worker health checks (master cannot distinguish a hung worker from a slow one)**: *Status: Open — the 2s heartbeat (`ShardResult`, `IsFinal=false`) provides liveness but the master does not currently time out a shard if heartbeats stop; recommended: a per-shard heartbeat-staleness timeout that aborts and re-shards.*
4. **Single Kafka consumer (per job) cannot fan out across multiple master replicas**: *Status: Open — acceptable for a single-master deployment; multi-master would require a shared consumer group with partition assignment, or routing all telemetry through one designated “scoring” replica.*
5. **DB connections opened per-job rather than pooled**: *Status: Resolved — `main.go` now calls `db.SetMaxOpenConns(5)/SetMaxIdleConns(3)` once at `initDB()` and reuses the pool for every job's final `UPDATE`.*
6. **Synchronous, blocking pretest runs inside the compilation goroutine**: *Status: Resolved at the service-decomposition level — `services/compiler` and `services/testing` are separate processes connected by Redis Streams, so a slow pretest cannot block the compile queue; remaining risk is the `burn-CPU-30s` simulated load left in both workers for HPA testing, which should be removed before production.*
7. **Redis failure modes breaking the leaderboard**: *Status: Mitigated — `initRedis()` degrades to `rdb = nil` on connection failure and every call site checks for nil (rate limiting fails open with an internal-error response rather than panicking; job replication is skipped with a logged warning); the leaderboard itself is sourced from Postgres, not Redis, so Redis downtime does not break ranking, only job-store durability.*

*Note on re-scoping*: a fresh architectural audit against the latest codebase (post-fixes above) is queued but was not re-run as part of this document; items 1, 3, and 4 above should be treated as the current top-priority backlog for the next audit pass.

### 11.4 Evaluated & Rejected: Proposed Architecture Upgrade
A proposed “scale to 50K contestants” redesign was evaluated and rejected. The proposal aimed to run every single pretest execution as an independent distributed task using transient Kubernetes Pods and route all telemetry through a central Kafka/Redpanda cluster for processing by a multi-master distributed validator service. 

This alternative was rejected due to three critical engineering risks:
1. **Kubernetes API Server Churn & Scheduling Latency**: Spawning, scheduling, and cleaning up 50,000+ short-lived pretest sandbox containers on a multi-node K8s cluster during a submission surge would flood the Kubernetes control plane. It would cause severe CPU starvation in the `etcd` backend, increasing container scheduling and startup latency from less than 1 second to upwards of 15 seconds. This would violate the target `<5s` pretest feedback loop SLA.
2. **Telemetry Overhead on Lighter Pretest Loads**: Routing pretest telemetry (only 5 bots × 100 orders = 500 messages total) through Kafka adds significant message framing, serialization, and networking hops. The network queueing delays skew the High-Dynamic Range (HDR) latency metrics, making fine-grained latency benchmarking of contestant matching loops highly inaccurate.
3. **Database and WebSocket Exhaustion**: Pushing real-time database query results and WebSocket message broadcasts directly to 100K active viewers for the live leaderboard would saturate database connections and socket descriptors at the gateway. 

Instead, the platform selected a hybrid design: keep pretests in-process using dynamic local port mapping for sub-second feedback, and restrict distributed gRPC worker sharding and Kafka telemetry pipelines solely to the post-contest, batch system tests. The database is shielded by materializing standings every 3s to a static CDN-hosted `leaderboard.json` file.

---

## 12. Infrastructure as Code (IaC) & Deployment
The platform's deployment strategy transitions from local Docker Compose files for developers to cloud-ready multi-node Kubernetes clusters, leveraging automated horizontal scaling.

### 12.1 Local Developer Environment (Docker Compose)
For local development, the platform provides a complete environment orchestrated using Docker Compose (`docker-compose.yml`, `docker-compose.workers.yml`):
- **Core Infrastructure**: PostgreSQL (data persistence), Redis (queuing streams & rate limiting), MinIO (S3-compatible object storage), and Redpanda (high-performance Kafka-compatible broker).
- **Service Stack**: Submission Gateway API, Compiler worker, and Testing worker binaries running in bridge network modes.
- **Service Initialization**: An init container (`Dockerfile.init-db`) automatically applies Goose database migrations and creates the required S3 storage buckets upon bootstrap. Redpanda topics (specifically `order-events` with 6 partitions) are pre-allocated via `rpk` hooks.

### 12.2 Production-Grade Kubernetes Integration
The production deployment orchestrates all stateless and stateful elements natively within a Kubernetes cluster (e.g., GKE or EKS):
- **Stateful Sets**: PostgreSQL, Redis, and Kafka/Redpanda are deployed as StatefulSets utilizing persistent SSD storage classes to guarantee write IOPs and data safety.
- **Stateless Deployments**: The `submission-gateway`, `compiler-worker`, and `testing-worker` run as stateless Deployments, exposing standard Prometheus metrics endpoints for cluster-wide scraping.
- **Security Isolation (CNI)**: The contestant sandboxes are spawned as temporary Pods within an isolated namespace (`iicpc-sandboxes`). A strict Kubernetes `NetworkPolicy` isolates these pods:
  - **Egress**: Completely blocked (`egress: []`) to prevent credential exfiltration, internet scraping, or metadata service attacks.
  - **Ingress**: Restrained exclusively to TCP port `8000` from the `bot-fleet` pod selector, air-gapping the runtime process.

```mermaid
graph TD
    subgraph default Namespace
        GW[Gateway Pods] -->|HTTP /submit| CompileStream[Redis: compilation_queue]
        GW -->|rejudge Request| SystestStream[Redis: systest_queue]
        CW[Compiler Worker Pods] -->|Consume| CompileStream
        CW -->|Create Compile Job| K8sAPI[K8s API Server]
        PW[Testing Worker Pods] -->|Consume| SystestStream
        PW -->|Spawn Sandbox Pod| K8sAPI
        BF[Bot Fleet Master Pod] -->|gRPC Fleet Command| BW[Bot Fleet Worker Pods]
    end

    subgraph iicpc-sandboxes Namespace
        K8sAPI -->|Schedule Sandbox Pod| Sandbox[Contestant Sandbox Pod]
        BW -->|Stress Test TCP:8000| Sandbox
    end
```

### 12.3 Horizontal Scaling, Queue Depth Metrics & HPA
In production, workers scale dynamically based on the queue backlogs. Rather than relying on simple CPU/Memory thresholds (which lag behind traffic spikes), the Horizontal Pod Autoscalers (HPAs) query Custom Metric Exporters connected to Redis Streams:
- **Compiler HPA (`compiler-hpa.yaml`)**: Scales the compiler worker count (min: 2, max: 20) based on the length of the `compilation_queue` Stream.
- **Testing HPA (`testing-hpa.yaml`)**: Scales testing worker pods (min: 2, max: 100) based on the backlog of the `pretest_queue` Stream.
- **Kubernetes client-go Integration**: Testing workers use native `client-go` libraries to programmatically spin up sandbox pods and monitor execution, resolving local socket permission constraints and eliminating host-level `/var/run/docker.sock` daemon bindings.

---

## 13. Testing Strategy
Our testing paradigm ensures performance stability and matching correctness at every layer of the platform, dividing testing into unit, end-to-end, and high-load stress validation phases.

### 13.1 Unit Testing: Logic & Validation Assertions
- **Validator Unit Tests** (`bot-fleet/shadow/validator_test.go`): Direct assertions testing Red-Black tree priority matching, self-crossing prevention rules, priority score decays, and order value discrepancies.
- **Zero-Allocation Assertions** (`bot_test.go` / `TestStrictZeroAllocAssertion`): Checks that the hot-path buffer pools (`sync.Pool`) for Protobuf parsing do not allocate heap memory, protecting the load generator from garbage collection (GC) latency spikes.

### 13.2 Automated End-to-End Integration Suite
The Go integration suite (`tests/e2e_platform_test.go` and `scripts/run_e2e_tests.sh`) automates the full contestant lifecycle in an isolated loop:
1. **Build and Zip**: Packages a reference contestant matching engine on the fly.
2. **Submit**: Dispatches a multi-part HTTP POST upload request to the Gateway.
3. **Poll**: Queries the `/api/v1/build/:id` status endpoint until the status transitions to `completed`.
4. **Assert Database Consistency**: Connects to Postgres and asserts that the stored composite score, sub-scores, error messages, and raw telemetry diagnostics JSON match the expected values.
5. **Standings Index Verification**: Assures that the generated static file `/frontend/leaderboard.json` is correctly written and displays the contestant's rank.
6. **Graceful Cleanup Teardown**: Deletes all generated DB rows and object metadata, resetting the system to a clean state.

```bash
# Executing E2E suite
./scripts/run_e2e_tests.sh
```

### 13.3 Automated Post-Contest High-Load System Stress Tests
The system stress testing harness (`scripts/run_systest.sh`) tests the platform under massive concurrency:
- **Topology**: Bootstraps the distributed gRPC Bot Fleet.
- **Volatilities**: Configures the MMPP (Markov-Modulated Poisson Process) burst scheduler.
- **Load Size**: Fires up to 400,000 orders across 500 bots.
- **Telemetry Collection**: Captures raw order events in Kafka/Redpanda, streams latency metrics to HdrHistograms, and updates Postgres asynchronously.

```bash
# Running High-Load System Test
./scripts/run_systest.sh
```

### 13.4 Multi-Engine Diagnostics Suite
To prove the grading engine's accuracy across different contestant behaviors, five reference engines are kept under `test_payloads/`:
- `go_optimized`: Fully correct, high-performance engine. Resolves to `Accepted` with a 100% correctness score.
- `python_slow`: Correct logic, but executes a sleep statement per order. Triggers latency thresholds, resolving to `Partial — Latency`.
- `rust_crash`: Intentionally panics and exits with code 101 after handling 10 orders. Validates container crash detection and standard error logging.
- `node_scammer`: Deliberately returns wrong counterparties and phantom fills to test the shadow validator's grading system, resolving to `Logic Violation (LV)`.
- `cpp_basic`: Baseline C++ engine demonstrating standard order book matching.

---

## 14. Consolidated Engineering Decision Log

### 14.1 Key Architecture Decisions Traceability Table
This decision log lists every major trade-off and choice made during system design.

| ID | Engineering Decision | Alternatives Considered | Core Rationale & Trade-offs |
| :--- | :--- | :--- | :--- |
| **D1** | **Hybrid Static/Dynamic Standings** | • Real-Time WebSockets<br/>• Direct SQL Queries | **Database Protection**: Avoids Postgres resource exhaustion by rendering rankings to a static `leaderboard.json` cached at CDN edge every 3s, serving 100K viewers at near-zero marginal cost. |
| **D2** | **Redis Streams for Queueing** | • RabbitMQ<br/>• Apache Kafka | **Operational Simplicity**: Provides PEN-based message durability, consumer groups, and PEL recovery at minimal cost for the low-volume compile/testing pipeline. |
| **D3** | **Kafka for Telemetry Ingestion** | • Redis Pub/Sub<br/>• Direct DB writes | **High-Throughput Partitioning**: Handles 500K+ TPS telemetry events. Keying by `OrderID` guarantees strict sequence order conservation per partition across workers. |
| **D4** | **In-Process Pretest Simulation** | • Independent containerized clients | **Low Overhead**: Eliminates Docker API scheduling latency, network bridging hops, and host CPU spikes, ensuring pretests run under the 5s feedback SLA. |
| **D5** | **Dynamic Port Mapping (HostPort: "0")** | • Host network sharing (`host`) | **Security & Collision Prevention**: Host network sharing with seccomp blocks triggers `SIGSYS` (exit code 159). Mapped ports prevent collisions under parallel runs. |
| **D6** | **Weighted Latency Decay (50% p99)** | • Average latency<br/>• Pure p99 scoring | **Tail-Latency Enforcement**: Linear decay starting at SLA target. Weighting p99 at 50% penalizes lock-contention and GC pauses that degrade market efficiency. |
| **D7** | **Multi-Trial Sandbox Execution ($K=3$)** | • Single trial runs<br/>• $K \ge 5$ runs | **Jitter Immunity**: Averages out virtualization CPU jitter. Standard deviation $\sigma < 2.0$ receives a +5.0 Stability Bonus, rewarding deterministic code. |
| **D8** | **Strict SLA Verdict Gates** | • Raw composite score averages | **Cheat Prevention**: Strict priority evaluation. Mismatched order book logic triggers a `Logic Violation` (LV) verdict, blocking leaderboard entry. |
| **D9** | **Protocol-Aware Benchmarking Curves** | • Uniform latency thresholds | **Transport Fairness**: Custom target/ceiling curves for TCP, WebSocket, REST, and FIX prevent framing overhead from penalizing REST/WebSocket engines. |
| **D10** | **Sharded Red-Black Trees for Validator** | • Single mutex order book | **Validation Scaling**: Mutex sharding per symbol allows the validator to process telemetry streams concurrently without lock contention. |
| **D11** | **Kaniko for Container Builds** | • DooD (`/var/run/docker.sock`) | **K8s Security Hardening**: Mounting docker socket in compiler pods exposes host root access. Kaniko builds Docker images securely in user space. |
| **D12** | **Spot Instances for Testing Pools** | • On-demand VMs | **Cost Optimization**: Saves ~70% compute costs. Preempted testing runs return to the Redis Stream queue automatically and retry on fresh nodes. |
