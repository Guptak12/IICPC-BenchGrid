# BenchGrid Component 4: Bot Fleet & Shadow Validator Design Document
## Deterministic Trade Simulation & Match Correctness Auditing

This document details the engineering architecture, mathematical models, and algorithmic designs for **Component 4: Bot Fleet & Shadow Validator** of the BenchGrid platform. This component generates high-concurrency deterministic order flows to stress-test contestant matching engines and validates execution reports against a strict price-time priority shadow oracle.

---

## 1. Architectural Overview & Simulation Mechanics

The load generation and validation pipeline operates entirely **in-process** within the Testing Service runner to minimize network latency jitter and eliminate process-spawning overhead.

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                           Testing Service Runner                            │
│                                                                             │
│  ┌───────────────────────┐                    ┌─────────────────────────┐   │
│  │   Bot Fleet Manager   │                    │  Shadow Book Validator  │   │
│  │                       │                    │                         │   │
│  │  ┌─────────────────┐  │                    │  ┌───────────────────┐  │   │
│  │  │ Bot GoRoutine 1 │  │                    │  │ Symbol Shard: BTC │  │   │
│  │  ├─────────────────┤  │                    │  │ (Red-Black Trees) │  │   │
│  │  │ Bot GoRoutine 2 │  │   Proto/JSON/FIX   │  └───────────────────┘  │   │
│  │  ├─────────────────┤  ├───────────────────>│           ▲             │   │
│  │  │       ...       │  │                    │           │             │   │
│  │  └─────────────────┘  │                    │           │ Audits      │   │
│  └──────────┬────────────┘                    └───────────┼─────────────┘   │
│             │                                             │                 │
│      Orders │ (Raw Socket/TCP)                            │ Exec Reports    │
│             ▼                                             │                 │
│  ┌────────────────────────────────────────────────────────┴─────────────┐   │
│  │                    Contestant Matching Engine                        │   │
│  │                         (Sandbox Pod)                                │   │
│  └──────────────────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────────────────┘
```

### Decision 1: In-Process Client Simulation
* **The Problem**: Orchestrating independent out-of-process containerized client containers for simulation introduces substantial startup latency, scheduling overhead, and network context-switching jitter, corrupting high-precision telemetry metrics.
* **The Solution**: Spawn the virtual bot fleet as lightweight Go routines running concurrently in-process within the Testing Service runner. They communicate directly using zero-allocation protobuf/JSON serialization pools (`sync.Pool`) and record direct round-trip metrics using [hdrhistogram-go](https://github.com/HdrHistogram/hdrhistogram-go).
* **Result**: Drastically minimized measurement jitter, near-zero startup overhead, and immediate telemetry aggregation.
* **Trade-offs & Alternatives**:
  * *Alternative*: Run clients as separate Docker containers inside the sandbox network. Rejected because the overhead of starting $N$ Docker containers (where $N \ge 50$) causes massive CPU spikes on the host worker and degrades test speed.

### Decision 2: Air-Gapped Network Isolation
* **The Problem**: A contestant container could attempt to connect to external servers to download malicious tools, exfiltrate private telemetry data, or launch network attacks (DDoS) from our servers.
* **The Solution**: Force contestant containers to join a dedicated, isolated network bridge (`sandbox-net`) that lacks a default routing gateway and has DNS disabled.
* **Result**: The container is completely air-gapped, unable to communicate with anything outside its dynamic, mapped TCP port.
* **Trade-offs & Alternatives**:
  * *Alternative*: Restricting network access using standard firewall (`iptables`) rules at the host level. Rejected because firewall configurations are difficult to maintain in dynamically scaled container environments (like Kubernetes) and are prone to administrative misconfigurations.

### Decision 3: Preventing Port Conflicts via Dynamic Host Port Mapping (HostPort: "0")
* **The Problem**: Contestant matching engines must bind to port `8000` (per the protocol spec). If two worker threads try to run different submissions concurrently and bind to port `8000` on the host, a port conflict error occurs, preventing parallel evaluations.
* **The Solution**: Map container port `8000` to a dynamic host port on loopback IP `127.0.0.1` by setting the target host port to `"0"`:
  ```go
  PortBindings: network.PortMap{
      "8000/tcp": []network.PortBinding{
          {
              HostIP:   "127.0.0.1",
              HostPort: "0", // Let Docker allocate an available port dynamically
          },
      },
  }
  ```
  The pretest worker queries the allocated port via `ContainerInspect` once the container starts:
  ```go
  hostPort := info.Container.NetworkSettings.Ports["8000/tcp"][0].HostPort
  endpoint := fmt.Sprintf("127.0.0.1:%s", hostPort)
  ```
* **Result**: Zero port collision events, enabling unlimited local execution concurrency for parallelizing $K=3$ sandbox iterations.
* **Trade-offs & Alternatives**:
  * *Alternative*: Statically mapping port blocks to workers (e.g. worker 1 gets 8001, worker 2 gets 8002). Rejected because it restricts worker scaling and is prone to configuration mismatch bugs. Dynamic mapping enables infinite local concurrency.

### Decision 4: Exponential Backoff Liveness Probing
* **The Problem**: Matching engines (especially compiled Go or Rust engines) can take a few seconds to start up, bind, and listen on Port 8000. Launching the bot fleet stress test immediately results in a TCP connection socket crash. Also, docker-exposed endpoints might immediately accept TCP connections (via dynamic loopback proxies) even before the matching engine inside the container starts listening, resulting in false-positive connection handshakes.
* **The Solution**: Implement a liveness check loop using exponential backoff to probe the port, incorporating a false-positive detection read deadline:
  ```go
  backoff := 50 * time.Millisecond
  for start := time.Now(); time.Since(start) < 10*time.Second; {
      probeConn, probeErr = net.DialTimeout("tcp", endpoint, 500*time.Millisecond)
      if probeErr == nil {
          // Set short read deadline to detect false-positive proxy connections
          probeConn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
          oneByte := make([]byte, 1)
          _, readErr := probeConn.Read(oneByte)
          if readErr != nil {
              if netErr, ok := readErr.(net.Error); ok && netErr.Timeout() {
                  // Timeout is expected -> engine is listening but waiting for input
                  probeConn.Close()
                  probeErr = nil
                  break
              }
              probeErr = readErr
          } else {
              probeConn.Close()
              probeErr = nil
              break
          }
          probeConn.Close()
      }
      time.Sleep(backoff)
      backoff *= 2
      if backoff > 1*time.Second {
          backoff = 1 * time.Second
      }
  }
  ```
  If the container does not accept TCP connections within 10 seconds, the run is terminated as a Runtime Error and debug logs are saved.
* **Result**: Stable simulation start sequences with high noise immunity.
* **Trade-offs & Alternatives**:
  * *Alternative*: Hardcoded thread sleep (e.g. `time.Sleep(3 * time.Second)`) before launching the simulation. Rejected because it wastes bench time for fast-starting C++ engines and fails for slower-starting runtimes (Go/Java).

### Protocol Support
Bots negotiate connections using standard adapters (`ProtocolAdapter` interface in [services/testing/runner.go](file:///Users/destructor/Desktop/Kush/Projects/IICPC-BenchGrid/services/testing/runner.go#L811)):
* **TCP / Protobuf**: Native high-speed binary protocol defined in `pkg/protocol`.
* **WebSockets / JSON**: Simulates retail web client environments.
* **REST / JSON**: Simulates REST HTTP polling engines.
* **FIX / Tag-Value**: Simulates institutional financial flows.


## 2. Deterministic Trading Strategies

To evaluate a matching engine's robustness under realistic market conditions, the Bot Fleet simulates various participant behaviors. All randomness is deterministic, driven by a base evaluation seed.

### RNG Seed & Bot Separation
To prevent correlation between bot decisions and ensure total coverage of the search space, each bot's PRNG is isolated:
* **Momentum strategy separation**: $\text{seed}_{\text{bot}} = \text{base\_seed} + (\text{BotID} \times 7919)$
* **Noise strategy separation**: $\text{seed}_{\text{bot}} = \text{base\_seed} + (\text{BotID} \times 104729)$
* Primes $7919$ and $104729$ guarantee maximum distance between initial states of the standard linear congruential generators (LCGs) or Mersenne Twister PRNGs.

### Behavior Profiles
1. **Market Maker (`MARKET_MAKER`)**:
   * *Purpose*: Maintains liquidity on both sides of the book.
   * *Mechanism*: Fires paired limit orders (`BUY` and `SELL`) around the mid-price.
   * *Pricing*: 
     $$P_{\text{bid}} = P_{\text{mid}} - \frac{\text{Spread}}{2} - \delta_1$$
     $$P_{\text{ask}} = P_{\text{mid}} + \frac{\text{Spread}}{2} + \delta_2$$
     where $\delta_i \sim \text{Uniform}(0, \frac{\text{Spread}}{10})$.
   * *Rate Limiting*: Utilizes a token bucket limiter with a burst size of 2 to allow simultaneous entry of the bid and ask quotes.

2. **Momentum Trader (`MOMENTUM_TRADER`)**:
   * *Purpose*: Simulates trend followers chasing volume.
   * *Mechanism*: Submits aggressive market orders or aggressive limit orders overlapping the spread to trigger instant match execution cascades.
   * *Canceling*: Every 5th order triggers a `CANCEL` for an outstanding resting limit order.
   * *Refill Throttling*: Bursts up to 16 tokens. Between bursts, it sleeps for a duration computed dynamically to refill the token bucket:
     $$T_{\text{sleep}} = \frac{\text{BurstSize}}{\text{RatePerSec}} + \text{Jitter}$$

3. **Noise Trader (`NOISE_TRADER`)**:
   * *Purpose*: Simulates uncoordinated retail order flow.
   * *Mechanism*: Generates random order actions (60% Limit, 25% Market, 15% Cancel) across a wide price boundary.
   * *Quantity Distribution*: Evaluated via a Zipfian distribution to model a power-law scale in order sizes (a few large orders, many small orders):
     $$u \sim \mathcal{U}(0.01, 1.0)$$
     $$Q_{\text{zipf}} = \min \left(10000, \left\lfloor \frac{200}{u} \right\rfloor \right)$$

### Poisson Process Scheduling & Volatility Regimes
The Bot Fleet simulates time-varying event arrivals using a **Markov-Modulated Poisson Process (MMPP)**. 
* **State Transition**: The scheduler alternates between three market volatility regimes: `CalmState`, `ElevatedState`, and `PanicState`.
* **Regime Transitions**: Transitions are governed by a discrete Markov chain evaluated at randomized intervals (100–300ms).
* **Interval Generation**: Within each state, the inter-arrival time $t_{\text{sleep}}$ until the next order is generated using inverse transform sampling:
  $$t_{\text{sleep}} = -\frac{\ln(1 - u)}{\lambda}$$
  where $u \sim \mathcal{U}(0, 1)$ and $\lambda$ represents the target throughput rate configured for the current volatility regime.

| Volatility Regime | Throughput Parameter ($\lambda$ per bot) |
| :--- | :--- |
| **Warm-up / Initial** | $\frac{10000}{N_{\text{bots}}}$ |
| **CalmState** | $\frac{1000}{N_{\text{bots}}}$ |
| **ElevatedState** | $\frac{20000}{N_{\text{bots}}}$ |
| **PanicState** | $\frac{500000}{N_{\text{bots}}}$ |

---

## 3. Shadow Order Book Oracle & Validation Logic

The validator [validator.go](file:///Users/destructor/Desktop/Kush/Projects/IICPC-BenchGrid/bot-fleet/shadow/validator.go) builds an in-memory replica of the order book. By processing the deterministic order inputs, it acts as an oracle of expected executions. It then verifies if the actual execution reports returned by the contestant matching engine match the oracle.

### Sharded Concurrency Architecture
To prevent lock contention when simulating thousands of concurrent bot orders, the `Validator` struct routes validation streams to symbol-specific `SymbolShard` instances:
```go
type Validator struct {
    mu           sync.RWMutex
    shards       map[string]*SymbolShard
    orderToShard map[int64]*SymbolShard
}
```
Each shard maintains its own mutex boundaries, allowing parallel matching calculations across independent trading pairs.

### Order Book Storage Strategy
Inside each symbol shard, resting limit orders are tracked using Red-Black trees to facilitate $O(\log N)$ insertions, deletions, and lookup operations.
* **Bids**: Red-Black tree sorted in **descending** order (highest bid price first).
* **Asks**: Red-Black tree sorted in **ascending** order (lowest ask price first).
* **FIFO Price-Time Priority**: Each price level contains a doubly-linked list (`container/list`) of active orders. Incoming orders match against the head of the list.

### Match Auditing Algorithm
Whenever a limit or market order is accepted, the shadow validator performs the matching logic:
1. **Self-Crossing (Wash Trade) Protection**:
   * Order IDs embed the Bot ID in the upper 32 bits: $\text{BotID} = \text{OrderID} \gg 32$.
   * A trade is a wash trade if:
     $$\text{isSelfCross}(ID_{\text{incoming}}, ID_{\text{resting}}) \iff (ID_{\text{incoming}} \gg 32) == (ID_{\text{resting}} \gg 32)$$
   * Wash trades bypass matching to prevent bots from filling their own orders.
2. **Execution Record Matching**:
   * The expected fills generated by the matching simulation are recorded in `expectedFills[OrderID]`.
   * Real fills from the contestant engine are routed to `actualFills[OrderID]`.
   * Consecutive partial fills at the same price and counterparty are merged (`foldPartials`) before validation.
   * If expected and actual executions align, the order resources are released from memory.

---

## 4. Scoring Formulation & Math

The validator computes a correctness score based on three components and applies penalty deductions for protocol violations.

### Scoring Equation
$$\text{Correctness Score} = S_{\text{qty}} + S_{\text{priority}} + S_{\text{val}} - P_{\text{phantom}} - P_{\text{protocol}}$$

#### 1. Quantity Match Score ($S_{\text{qty}}$ - Max 70 Points)
Measures if the engine filled the correct total volume:
$$S_{\text{qty}} = \left( \frac{\min(Q_{\text{price\_correct}}, Q_{\text{expected}})}{Q_{\text{expected}}} \right) \times 70$$

#### 2. Price-Time Priority Score ($S_{\text{priority}}$ - Max 20 Points)
Audits the exact match ordering, verifying that the engine filled resting limit orders in FIFO priority:
$$S_{\text{priority}} = \left( \frac{\min(Q_{\text{priority\_correct}}, Q_{\text{expected}})}{Q_{\text{expected}}} \right) \times 20$$
* A fill is priority-correct if the price, volume, and matched counterparty match the expected sequence.

#### 3. Match Value Score ($S_{\text{val}}$ - Max 10 Points)
Checks price accuracy to ensure trades were executed at valid execution prices:
$$S_{\text{val}} = \max\left(0, 10.0 \times \left(1.0 - \frac{|V_{\text{expected}} - V_{\text{actual}}|}{V_{\text{expected}}}\right)\right)$$
where $V = \sum P_{\text{fill}} \times Q_{\text{fill}}$.

#### 4. Penalty Deductions
* **Phantom Fills ($P_{\text{phantom}}$)**: Occur when an engine reports fills for non-existent orders, or fills quantities exceeding the requested size.
  $$P_{\text{phantom}} = \min \left(25.0, 25.0 \times \frac{Q_{\text{phantom}}}{Q_{\text{expected}}}\right)$$
* **Protocol Violations ($P_{\text{protocol}}$)**: Subtracts 2 points per occurrence of:
  * **Duplicate Orders**: Re-using an active Order ID.
  * **Unknown Acks**: Sending execution report updates for non-existent orders.
  * **Ack Violations**: Incorrectly acknowledging order types (e.g. failing to reject invalid parameters).

---

## 5. Comparison: Pretests vs. System Tests

The Bot Fleet adapts its parameters depending on the evaluation pipeline. Pretests provide quick validation, while System Tests subject the engine to maximum stress.

| Parameter | Pretest | System Test |
| :--- | :--- | :--- |
| **Target Execution Timeout** | $< 5\text{s}$ | $120\text{s}$ |
| **Number of Bots ($N$)** | $5$ (Default) | $50 - 200$ (Scaled) |
| **Orders per Bot** | $100$ | $1,000 - 2,000$ |
| **Total Order Volume** | $500$ | $100,000 - 400,000$ |
| **Throughput Scheduler** | Uniform rate limit throttling | Volatility-driven MMPP Burst scheduler |
| **Peak Load Rate** | $100\text{ TPS}$ | Up to $500,000\text{ TPS}$ (Panic state) |
| **Verification Objective** | Code logic, Protocol compliance | Concurrency, Memory leaks, Thread safety |

---

## 6. Implementation References

The code powering these components is located in the following files:
* **Shadow Order Book & Validation**: [validator.go](file:///Users/destructor/Desktop/Kush/Projects/IICPC-BenchGrid/bot-fleet/shadow/validator.go) - Tracks expected fills, maintains bids/asks Red-Black trees, and calculates correctness percentages.
* **Deterministic Strategy Generation**: [strategy.go](file:///Users/destructor/Desktop/Kush/Projects/IICPC-BenchGrid/bot-fleet/strategy.go) - Configures Market Maker, Momentum, and Noise limiters and sleep intervals.
* **Bot Specifications & Data Schemas**: [bot-fleet/bot.go](file:///Users/destructor/Desktop/Kush/Projects/IICPC-BenchGrid/bot-fleet/bot.go) - Implements Order ID bit-packing, Zipf distribution, and Ring Buffer trackers.
* **In-Process Testing Coordinator**: [services/testing/runner.go](file:///Users/destructor/Desktop/Kush/Projects/IICPC-BenchGrid/services/testing/runner.go) - Manages TCP liveness checks, instantiates MMPP schedulers, and feeds data to the Validator.
* **Out-of-Process Distributed Driver**: [bot-fleet/runner.go](file:///Users/destructor/Desktop/Kush/Projects/IICPC-BenchGrid/bot-fleet/runner.go) - TCP driver used when orchestrating bot fleet execution from external worker nodes.
