# § E — Observability Stack: Grafana Admin Dashboard & Contestant Developer Dashboard

> Two separate dashboards serve two distinct audiences at different resolution levels.
> The Grafana dashboard is for **operators** — real-time cluster health.
> The contestant-facing developer dashboard is for **participants** — actionable feedback
> on exactly why their engine scored what it did.

---

## E.1 Grafana Admin Dashboard (`/monitoring/grafana/dashboards/iicpc-overview.json`)

**Access**: `http://localhost:3001` (admin / admin)
**Scrape interval**: 2 seconds (configured in `monitoring/prometheus.yml`)
**Auto-refresh**: Every 2 seconds
**Data source**: Prometheus (`http://prometheus:9090`)

The dashboard is fully **provisioned on container startup** via Grafana's
file-based provisioning mechanism — no manual import required.

```
monitoring/grafana/
├── provisioning/
│   ├── datasources/   →  Prometheus data source auto-wired
│   └── dashboards/    →  file path scanner for /var/lib/grafana/dashboards
└── dashboards/
    └── iicpc-overview.json   →  3-row dashboard, auto-loaded at boot
```

---

### Row 1 — Pipeline Overview (4 stat tiles)

These tiles give the operator an **instant healthcheck** of the evaluation pipeline
without opening any logs.

| Panel | Prometheus Query | What It Tells You |
|---|---|---|
| **Total Submissions** | `sum(iicpc_submissions_total)` | Cumulative submission volume since startup. Monotonically increasing — a flat line means no activity. |
| **Active Jobs (In Flight)** | `sum(iicpc_worker_active_jobs)` | Submissions currently being compiled or evaluated. **Green < 5 / Orange ≥ 5 / Red ≥ 15** — thresholds calibrated to single-node testing-worker capacity. |
| **Compilation Queue Depth** | `max(iicpc_queue_depth{queue="compilation_queue"})` | Number of unprocessed compile jobs. **Green < 50 / Orange ≥ 50 / Red ≥ 200** — a spike here means the compiler worker is behind. |
| **Evaluation Queue Depth** | `max(pretest_queue) + max(systest_queue)` | Combined pretest + systest backlog. A sustained value > 200 indicates the testing worker cannot keep up — trigger to scale horizontally. |

**Operational use**: During a live hackathon, an operator keeps this row visible.
If "Active Jobs" goes to 0 while "Evaluation Queue" is non-zero, it means the
testing-worker pod has crashed. The PEL recovery will trigger within 30 seconds,
but this panel surfaces the incident immediately.

---

### Row 2 — Bot Fleet Telemetry (2 time-series graphs)

These graphs are the primary diagnostic for evaluating engine performance across
a live system test.

#### Panel: Bot Fleet Throughput (TPS)

```
Query: max(iicpc_fleet_tps)
Y-axis: Transactions Per Second
Update cadence: every 200ms (inside RunFleet goroutine → Prometheus gauge → 2s scrape)
```

**What to look for**:
- **Normal profile**: TPS rises from 0 as bots connect, peaks during the MMPP
  Panic burst regime, then falls to 0 as the evaluation finishes.
- **Engine under load**: If TPS plateaus below the Panic-regime target
  (500K aggregate), the engine's TCP accept/dispatch path is the bottleneck.
- **Flat line at 0 for > 30s**: The sandbox container failed to start, or the
  engine crashed before accepting any connections.

#### Panel: Bot Fleet P99 Latency (µs)

```
Query: max(iicpc_fleet_p99_us)
Y-axis: Microseconds
Update cadence: every 200ms (inside RunFleet → HDRHistogram.ValueAtQuantile(99))
```

**What to look for**:
- **Acceptable**: P99 < 5,000µs (5ms) under sustained load
- **Warning**: P99 climbing during Panic regime indicates the engine is not
  draining its TCP receive buffer fast enough
- **Correlation with TPS drop**: If TPS drops while P99 climbs simultaneously,
  the engine has reached its ordering throughput ceiling — it is dropping
  connections, which counts against the correctness score

---

### Row 3 — Database & Resource Pool (2 time-series graphs)

#### Panel: Postgres DB Pool Connections

```
Query A: sum by (service) (iicpc_db_pool_active_connections)
Query B: sum by (service) (iicpc_db_pool_idle_connections)
Legend: gateway - Active, compiler - Idle, testing - Active, ...
```

**What it observes**:
- Connection pool saturation per microservice (`gateway`, `compiler`, `testing`)
- Configured limits (from `common.ConfigureDBPool`): max open = 25, max idle = 10,
  max lifetime = 30 min, max idle time = 5 min
- If `active` approaches 25 for any service, new DB operations will queue — causing
  latency spikes in that service's response times

**Operational signal**: During a system test with 500 bots, the testing worker
writes one DB row on completion (`UPDATE submissions SET ...`). If multiple
evaluations overlap, active connections on `testing` will spike to 2–3.
A value near 25 during peak would indicate a connection leak.

#### Panel: Gateway HTTP Traffic (RPS)

```
Query: sum by (method, path, status_code) (rate(iicpc_http_requests_total[$__rate_interval]))
Legend: POST /api/v1/submit (200), GET /api/v1/leaderboard/:id (200), ...
```

**What it observes**:
- Request rate per endpoint and per HTTP status code
- A spike in `POST /api/v1/submit (429)` indicates the per-IP rate limiter is
  activating — a contestant is submitting faster than allowed
- A sustained elevation in `(500)` responses on the leaderboard endpoint would
  indicate a DB connectivity issue
- The ratio of `GET /api/v1/leaderboard/:id/stream` connections shows how many
  SSE clients are actively subscribed

---

## E.2 Contestant-Facing Developer Dashboard (`http://localhost:3002`)

The frontend dashboard is the **primary user-facing observability surface**. It is
built for contestants, not operators — the goal is to give a participant enough
information to understand **exactly** what their engine did wrong and how to improve it.

The dashboard has three functional layers:

```
┌─────────────────────────────────────────────────────────────────────┐
│  Layer 1: Live Leaderboard  (SSE-pushed, no polling)                │
│  Layer 2: Pipeline Monitor  (submission-to-verdict progress bar)    │
│  Layer 3: Diagnostic Drawer (per-submission deep-dive panel)        │
└─────────────────────────────────────────────────────────────────────┘
```

---

### Layer 1 — Live Leaderboard

**Data source**: `GET /api/v1/leaderboard/{arena_id}/stream` (Server-Sent Events)
**Update frequency**: ≤ 5 seconds after any submission completes (pushed by Gateway hub)

| Column | Data Field | Observability Value |
|---|---|---|
| **Rank** | `rank` (PostgreSQL ORDER BY composite_score DESC) | Position relative to all other contestants |
| **Contestant ID** | `contestant_id` (anonymizable with eye-toggle) | Identity, with privacy toggle for live contest display |
| **Verdict** | `verdict` (Accepted / TLE / Logic Violation / Degradation) | Pass/fail classification — colour-coded badge |
| **Composite Score** | `composite_score` + `delta_score` | Overall score with green/red delta since last submission |
| **Correctness** | `correctness_score` (%) | Shadow validator score — % of orders that produced valid fills |
| **P99 Latency** | `p99_us` (µs) + `delta_p99` | P99 round-trip time with latency improvement/regression indicator |
| **TPS** | `actual_tps` | Sustained orders/second achieved during evaluation |
| **Trend** | `score_history` → SVG sparkline | 5-point score history sparkline — green if trending up, grey if flat/down |

**Tick flash animation**: When a new leaderboard push arrives, rows whose
`composite_score` increased flash green; rows that decreased flash red. This uses
the `previousScores` map in `app.js` to diff consecutive SSE events.

**Engine Archetype Tag**: The Gateway classifies each submission into one of four
archetypes based on its score profile:

| Archetype | Classification | Badge Colour |
|---|---|---|
| `Latency-Optimized` | Low P99, moderate TPS | Blue |
| `Accuracy-Optimized` | High correctness, lower TPS | Green |
| `Balanced` | Both above threshold | Neutral |
| `Low-Throughput` | Below TPS floor | Orange/red |

**Keyboard navigation**: `j`/`k` to move between rows, `Enter` to open diagnostic
drawer, `Escape` to close, `/` to focus the search filter — designed for operators
who monitor during the contest without touching the mouse.

---

### Layer 2 — Pipeline Monitor (Progress Bar)

When a contestant submits, the UI immediately shows a 4-step pipeline timeline:

```
QUEUED ──→ COMPILING ──→ RUNNING ──→ FINISHED
  15%         45%           75%        100%
```

**Implementation**: The monitor polls `GET /api/v1/build/{build_id}` every 1 second
and maps the `status` field from PostgreSQL to a visual progress state. When the
status reaches `completed` or `failed`, polling stops and the diagnostic drawer
opens automatically (800ms delay for animation).

**What it observes in real-time**:
- **QUEUED → COMPILING transition**: Latency here reveals Redis queue depth —
  if compilation starts 30s after submission, the queue is backed up.
- **COMPILING duration**: Kaniko/Docker build time. Displayed implicitly via
  the step timer (contestants learn their Docker build is slow vs. fast).
- **RUNNING duration**: Time the bot fleet is actively stress-testing the engine.
  A very short running phase followed by `failed` indicates the engine crashed
  on startup (TCP liveness probe failed within 10s).

---

### Layer 3 — Diagnostic Drawer (Per-Submission Deep-Dive)

Opened by clicking any leaderboard row, the drawer is the highest-information
surface in the entire system. It renders data from the `diagnostics` JSONB column
in PostgreSQL, populated by the testing worker at evaluation end.

#### Sub-section A: Performance Triptych (3 large KPI tiles)

```
┌─────────────────┬──────────────────┬───────────────────┐
│  Correctness    │  P99 Latency     │  Throughput       │
│  97.3%          │  342 µs          │  12,450 TPS       │
│  (red if <100%) │  (µs, 6 digits)  │  orders/second    │
└─────────────────┴──────────────────┴───────────────────┘
```

- **Correctness** turns **red** if < 100%, **green** if exactly 100%
- This tile is the single most important signal: any correctness < 100%
  means the shadow validator detected fills that violated price-time priority

#### Sub-section B: Radar Chart (3-axis Performance Profile)

```
Axes: Correctness | Latency Score | Throughput Score
Datasets: Submission (emerald fill) vs SLA Target (100/100/100, dashed grey)
```

**What it observes**: The radar chart instantly reveals the *type* of engine:
- A submission with Correctness=100 but Latency=40 and Throughput=70 is a
  **correctness-first engine** — the developer sacrificed speed for accuracy.
- A submission with Latency=95 but Correctness=60 is a **speed-first engine**
  with logic bugs.
- The gap between the submission polygon and the SLA dashed outline shows
  exactly which dimension has the most room for improvement.

#### Sub-section C: Latency Percentile Chart (P50 / P90 / P99)

```
Y-axis: Latency (µs)
Datasets:
  - This Submission (emerald solid line): [P50, P90, P99]
  - Top 10% Avg (grey dashed): [120µs, 240µs, 450µs]   ← reference benchmark
  - SLA Limit (red dashed): [5000µs, 5000µs, 5000µs]
```

**What it observes**:
- The slope from P50→P99 reveals tail latency behaviour. A steep slope (low
  P50, very high P99) indicates non-deterministic spikes — often caused by GC
  pauses, lock contention, or OS scheduling jitter.
- Comparison against the "Top 10% Avg" benchmark shows how far behind the
  submission is from competitive performance.
- Crossing the red SLA Limit line at P99 triggers a `TLE` (Time Limit Exceeded)
  verdict — the chart makes this cause immediately visible.

#### Sub-section D: TPS Trend Chart (Start / Mid / End)

```
Y-axis: Orders/second
Dataset: TPS at [start, mid, end] of evaluation
```

**What it observes**:
- A **declining curve** (high start, low end) indicates **connection pool
  exhaustion** or **memory leak** — the engine starts fast but degrades as
  its accept queue fills up.
- A **flat curve** near 0 means the engine never scaled up — single-threaded
  sequential order processing.
- A **rising curve** (low start, high end) is the rarest and best profile —
  an engine that warms up (JIT compilation, connection warmup) before reaching
  peak throughput.

#### Sub-section E: Anomaly Detector (3 counters with PASS/FAIL badges)

| Counter | Prometheus/DB Field | What it Detects |
|---|---|---|
| **Priority Violations** | `diag.priority_violations` | Orders matched out of price-time priority — e.g. a lower-priced buy filled before a higher-priced one. Caught by the shadow validator. |
| **Phantom Fills** | `diag.phantom_fills` | Fill notifications sent for orders that were never accepted — the engine invented a fill that never happened. |
| **Trade Discrepancies** | `diag.orders_failed` | Orders sent by the bot fleet that received no response (connection dropped, engine crashed, or protocol error). |

Each counter shows either a green `PASS` badge (count = 0) or a red `FAIL` badge.
**Any non-zero value is an automatic correctness deduction** in the scoring formula.

#### Sub-section F: Strategy Breakdown Table

```
Strategy       | Orders Sent | Orders Failed | Avg Latency
─────────────────────────────────────────────────────────
Market Maker   | 166,667     | 0             | 287 µs
Momentum       | 166,667     | 12            | 1,247 µs  ← red
Noise Trader   | 166,666     | 0             | 312 µs
```

**What it observes**: The three MMPP bot strategies stress different engine paths:
- **Market Maker** bots send alternating BID/ASK limit orders — tests the engine's
  matching path for two-sided quotes.
- **Momentum Trader** bots mix market orders and cancels — tests cancel processing
  and immediate fill paths. High failure rate here usually means the engine has a
  bug in its cancel handler.
- **Noise Trader** bots use Zipf-distributed quantities and random side — tests
  the engine under unpredictable order sizes. High latency for noise traders
  indicates the engine's order book is not O(log N) for large quantity orders.

A contestant can immediately see which bot strategy caused the most failures,
narrowing down exactly which code path to fix.

#### Sub-section G: Stability Score

```
Std Dev across K runs: 1.23    →  Stability Bonus: +5.0 pts
```

When `K_RUNS > 1`, the testing worker runs the same evaluation K times with
different seeds and reports the standard deviation of scores across runs.
- **StdDev < 2.0** → `+5` bonus points (deterministic, repeatable engine)
- **StdDev ≥ 2.0** → no bonus (engine has non-deterministic behaviour,
  e.g. race conditions or random timeouts)

#### Sub-section H: Sandbox Log Console

```
[18:42:01.341] [OK] Priority checks: 100% passed
[18:42:01.341] [OK] CPU limits: enforced
[18:42:01.341] [OK] Network isolation: active
[18:42:01.341] [OK] Output matches expected criteria
```

On failure, the console shows the exact error:
```
[18:42:01.341] [FATAL] contestant TCP server failed to listen on port 8000 within 10 seconds
```

This is the last line of debugging information a contestant sees. It bridges
the gap between a failed verdict and the actual root cause — without exposing
internal infrastructure details.

---

## E.3 Observability Coverage Summary

| Metric / Event | Admin Grafana | Contestant Dashboard |
|---|---|---|
| Submission pipeline status | ✓ Active Jobs gauge | ✓ Pipeline Monitor progress bar |
| Compilation queue depth | ✓ Stat tile (colour-coded) | — |
| Evaluation queue depth | ✓ Stat tile (colour-coded) | — |
| Fleet TPS (live, per evaluation) | ✓ Time-series graph (2s) | ✓ TPS trend chart (start/mid/end) |
| Fleet P99 latency (live) | ✓ Time-series graph (2s) | ✓ Latency chart (P50/P90/P99) |
| Correctness score | — | ✓ KPI tile + radar chart |
| Phantom fills / priority violations | — | ✓ Anomaly badges + counts |
| Strategy-level failure breakdown | — | ✓ Per-strategy table |
| Stability across K runs | — | ✓ StdDev + bonus |
| DB connection pool health | ✓ Time-series per service | — |
| Gateway HTTP error rate | ✓ RPS by status code | — |
| Leaderboard rank change | — | ✓ Tick flash + score delta |
| Sandbox crash / error logs | — | ✓ Log console |
| Engine architecture classification | — | ✓ Archetype tag |
| Score history trend | — | ✓ SVG sparkline |

The two dashboards are intentionally **non-overlapping**. The Grafana dashboard
gives operators the cluster-level view they need to ensure the platform is healthy.
The contestant dashboard gives participants the submission-level view they need
to iterate on their engine. Neither audience has access to the other's view.
