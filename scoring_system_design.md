# BenchGrid Scoring System Design Document
## Multi-Dimensional Performance Evaluation & Verdict Resolution

This document details the engineering decisions, mathematical scoring formulas, and architectural trade-offs that govern how contestant matching engines are benchmarked, graded, and classified within the IICPC-BenchGrid platform.

---

## 1. High-Level Scoring & Evaluation Pipeline

Each contestant submission is compiled and sandboxed before entering the evaluation pipeline. The scoring system takes raw telemetry metrics (correctness, latencies, order counts, and execution rates) and processes them through verdict gates and statistical scoring models to determine a final composite grade.

```
                  ┌────────────────────────────────────────┐
                  │          Submission Sandboxed          │
                  └───────────────────┬────────────────────┘
                                      │
                                      ▼
                  ┌────────────────────────────────────────┐
                  │      Trial Execution Engine (K=3)      │
                  └───────────────────┬────────────────────┘
                                      │
                                      ▼
                  ┌────────────────────────────────────────┐
                  │     Aggregate Trial Performance        │
                  │   (Mean Metrics & Standard Deviation)  │
                  └───────────────────┬────────────────────┘
                                      │
                   ┌──────────────────┴──────────────────┐
                   ▼                                     ▼
        ┌─────────────────────┐               ┌─────────────────────┐
        │  Verdict SLA Gates  │               │   Base Sub-Scores   │
        │  (LV, TLE, TD, AC)  │               │ (Qty, Pri, Val Math)│
        └──────────┬──────────┘               └──────────┬──────────┘
                   │                                     │
                   │                                     ▼
                   │                          ┌─────────────────────┐
                   │                          │ Base Composite Score│
                   │                          └──────────┬──────────┘
                   │                                     │
                   │                                     ▼
                   │                          ┌─────────────────────┐
                   │                          │   Stability Bonus   │
                   │                          │    (StdDev < 2.0)   │
                   │                          └──────────┬──────────┘
                   │                                     │
                   ▼                                     ▼
     ┌─────────────────────────────────────────────────────────┐
     │  Persist Final Score, Warnings, and Engine Archetype   │
     └─────────────────────────────────────────────────────────┘
```

---

## 2. Component Design & Key Engineering Decisions

### Decision 1: Multi-Trial Benchmark Execution ($K=3$)
* **The Problem**: Virtualized host environments running sandboxed matching engines are susceptible to random hardware interrupts, memory context-switching noise, and operating system scheduling pauses. Running a single test run risks penalizing contestants with high scores due to temporary background activity on the benchmarking server.
* **The Solution**: Run $K=3$ consecutive trial executions for every submission. Raw telemetry metrics (correctness, RTT latency percentiles, order rates) are collected per run, and the **arithmetic mean** (average) is calculated to construct the submission's score profile.
* **Result**: High measurement consistency, isolating contestant scores from environment noise.
* **Trade-offs & Alternatives**:
  * *Alternative*: Perform a single trial run. Rejected because random CPU scheduling pauses would arbitrary drop a contestant's score by $20-30\%$, causing unfair leaderboard shifts.
  * *Alternative*: Run $K \ge 5$ trials. Rejected because benchmark runtimes scale linearly. Running $5$ trials increases pretest duration past $25\text{s}$, violating the $<10\text{s}$ developer feedback SLA. $K=3$ provides the optimal balance of statistical noise filtering and fast execution.

---

### Decision 2: Canonical Composite Score Formulation
* **The Problem**: A matching engine cannot be evaluated on speed alone, nor correctness alone. A pure speed metric would reward incorrect or fake matching loops, while a pure correctness metric would fail to reward state-of-the-art latency optimization engineering.
* **The Solution**: Apply a canonical weighted formula combining the three primary sub-scores:
  $$\text{Composite Score} = (S_{\text{correctness}} \times 0.40) + (S_{\text{latency}} \times 0.30) + (S_{\text{throughput}} \times 0.30)$$
  All inputs are bounded to $[0.0, 100.0]$ and the final result is rounded to 2 decimals. Correctness is intentionally weighted highest ($40\%$) to reflect its critical role as the base requirement of a financial exchange.
* **Result**: A unified metric that rewards engines for balance across logic, latency, and load handling.
* **Trade-offs & Alternatives**:
  * *Alternative*: Equal weighting ($33.3\%$ each). Rejected because it over-rewards an engine that is fast but occasionally misses matching constraints, raising the score too high for logically flawed code.
  * *Alternative*: Multiplicative scoring (e.g. $\text{Score} = C \times L \times T$). Rejected because it is too volatile; an engine with $99\%$ correctness but a minor latency drop would suffer an excessive score penalty, making minor optimizations hard to evaluate on the leaderboard.

---

### Decision 3: Protocol-Aware Dynamic Latency Targets & Ceilings
* **The Problem**: Evaluating matching engines using static latency thresholds (e.g. target $500\mu\text{s}$, ceiling $5\text{ms}$) is highly unfair across different network transports. A WebSockets/JSON or REST API engine incurs framing and HTTP overhead compared to raw TCP, yet still represents high execution quality. Hardcoded thresholds would reject REST/WebSocket implementations under SLA gates.
* **The Solution**: Define transport-specific latency targets and ceilings, and implement a dynamic calibration query that updates these thresholds based on the average p99 latency of the top 10% of accepted submissions for that protocol:
  * **Default Baselines**:
    * Raw TCP: Target $50\text{ms}$ ($50,000\mu\text{s}$), Ceiling $500\text{ms}$ ($500,000\mu\text{s}$)
    * WebSockets: Target $100\text{ms}$ ($100,000\mu\text{s}$), Ceiling $1\text{s}$ ($1,000,000\mu\text{s}$)
    * REST/HTTP: Target $150\text{ms}$ ($150,000\mu\text{s}$), Ceiling $1.5\text{s}$ ($1,500,000\mu\text{s}$)
  * **Dynamic Calibration**:
    ```sql
    WITH ordered_subs AS (
        SELECT p99_us, PERCENT_RANK() OVER (ORDER BY composite_score DESC) as rank
        FROM submissions
        WHERE status = 'completed' AND verdict = 'Accepted' AND diagnostics->>'protocol' = $1
    )
    SELECT COALESCE(AVG(p99_us), 0.0) FROM ordered_subs WHERE rank <= 0.10;
    ```
    To avoid run-away contractions, targets are clamped to an absolute floor of $100\mu\text{s}$, ceilings to $1,000\mu\text{s}$, and the ceiling is enforced to be at least $10 \times \text{target}$.
* **Result**: Submissions are evaluated fairly relative to other engines using the same protocol stack.
* **Trade-offs & Alternatives**:
  * *Alternative*: Require raw TCP for all submissions. Rejected because supporting JSON WebSockets and REST makes the platform accessible to web and frontend developers, while raw TCP is restricted to systems programmers.

---

### Decision 4: Multi-Percentile Latency Score Weighting (p50: 20%, p90: 30%, p99: 50%)
* **The Problem**: Average (mean) latency is easily skewed by a few extreme outliers and fails to capture the true tail behavior of an exchange engine under load. Conversely, evaluating only the p99 ignores baseline algorithmic throughput efficiency.
* **The Solution**: Compute sub-scores for the p50, p90, and p99 latency percentiles independently using a linear decay function relative to the target and ceiling, and combine them using a weighted sum:
  $$\text{Latency Score} = 0.20 \cdot S_{p50} + 0.30 \cdot S_{p90} + 0.50 \cdot S_{p99}$$
  Where each percentile bucket $s$ is computed as:
  $$s(L) = \begin{cases} 
  100.0 & L \le \text{target} \\
  0.0 & L \ge \text{ceiling} \\
  100.0 \times \left(1.0 - \frac{L - \text{target}}{\text{ceiling} - \text{target}}\right) & \text{otherwise}
  \end{cases}$$
  The 50% weight on p99 is a deliberate choice: a matching engine that is fast on average but has an occasional 50ms GC pause or lock-contention spike is heavily penalized, mirroring real exchange SLAs.
* **Result**: Balanced scoring that rewards sub-millisecond execution times at the p50 while heavily penalizing engines with poor tail performance at the p99.
* **Trade-offs & Alternatives**:
  * *Alternative*: Use mean latency. Rejected because a single large garbage collection pause would skew the average of thousands of sub-microsecond runs, failing to reflect typical execution speed.
  * *Alternative*: Evaluate only the p99. Rejected because it fails to distinguish between two engines that have similar tail latency profiles but different baseline speeds.

---

### Decision 5: Throughput Score Bifurcation (System Test vs. Pretest)
* **The Problem**: In pretests, the focus is on basic correctness and low-overhead verification, where throughput metrics are calculated locally. In system tests, the platform shifts to a distributed gRPC master/worker model where bots fire at rates up to $500,000\text{ TPS}$. A single throughput calculation formula cannot fit both environments due to varying message volumes.
* **The Solution**: Implement two context-specific throughput formulas:
  * **System Test**: Measures purely the raw order drop rate under heavy network stress:
    $$S_{\text{throughput}} = (1.0 - \text{failRate}) \times 100.0$$
    where $\text{failRate} = \frac{\text{Orders Failed}}{\text{Orders Sent}}$.
  * **Pretest / Pretest Verdict Engine**: Splits the throughput sub-score into two parts ($50\%$ each) to verify stability and peak performance under lighter load:
    $$S_{\text{throughput}} = (0.50 \times S_{\text{stability}}) + (0.50 \times S_{\text{max\_tps}})$$
    * Peak TPS Score: $S_{\text{max\_tps}} = \min\left(100.0, \, \frac{\text{MaxSustainedTPS}}{\text{MaxExpectedTPS}} \times 100.0\right)$, where `MaxSustainedTPS` is the highest 1-second window containing zero failures.
    * Stability Score: Applies logarithmic decay to the order failure rate:
      $$S_{\text{stability}} = \max\left(0.0, \, 100.0 \times \left(1.0 - \ln\left(1.0 + (e - 1.0)\frac{\text{failRate}}{\text{maxFailRate}}\right)\right)\right)$$
      where `maxFailRate` is 0.1% for system tests, or 5 dropped orders for pretests.
* **Result**: Tailored evaluation: pretests reward smooth burst handling, while system tests enforce strict raw drop rate constraints.
* **Trade-offs & Alternatives**:
  * *Alternative*: Use the system test formula for pretests. Rejected because pretests send only $500$ orders; a single connection drop would result in a massive score drop, whereas the logarithmic stability score allows minor network jitter to pass with light penalties.

---

### Decision 6: Shadow Validator Correctness Formula (70% Qty, 20% Pri, 10% Val)
* **The Problem**: A matching engine could report fills with correct prices but out of time priority, or report matches with incorrect counterparties. Measuring correctness as a simple binary (match or mismatch) does not provide granular debugging information.
* **The Solution**: Divide correctness into three primary components, and apply strict point deductions for execution anomalies:
  $$S_{\text{correctness}} = S_{\text{qty}} + S_{\text{priority}} + S_{\text{val}} - P_{\text{phantom}} - P_{\text{protocol}}$$
  * **Quantity Score ($S_{\text{qty}}$ - 70%)**: Fraction of expected fill volume executed at the correct price:
    $$S_{\text{qty}} = \left( \frac{\min(Q_{\text{price\_correct}}, Q_{\text{expected}})}{Q_{\text{expected}}} \right) \times 70.0$$
  * **Priority Score ($S_{\text{priority}}$ - 20%)**: Fraction matched against the correct counterparty in the correct time sequence:
    $$S_{\text{priority}} = \left( \frac{\min(Q_{\text{priority\_correct}}, Q_{\text{expected}})}{Q_{\text{expected}}} \right) \times 20.0$$
  * **Value Score ($S_{\text{val}}$ - 10%)**: Measures price-quantity integrity:
    $$S_{\text{val}} = \max \left(0.0, \, 10.0 \times \left(1.0 - \frac{|V_{\text{expected}} - V_{\text{actual}}|}{V_{\text{expected}}}\right)\right)$$
  * **Phantom Penalty ($P_{\text{phantom}}$)**: Deducts up to 25 points for fills executed on non-existent orders:
    $$P_{\text{phantom}} = \min \left(25.0, \, 25.0 \times \frac{Q_{\text{phantom}}}{Q_{\text{expected}}}\right)$$
  * **Protocol Penalty ($P_{\text{protocol}}$)**: Deducts 2 points per occurrence of duplicated orders, unknown order acknowledgments, or invalid ack type handshakes.
* **Result**: Granular correctness evaluation that detects subtle timing and counterparty priority errors.
* **Trade-offs & Alternatives**:
  * *Alternative*: Binary validation (100% or 0%). Rejected because a single priority failure under load would result in a score of 0, giving developers no feedback on whether their engine processed $99\%$ of the book correctly.

---

### Decision 7: Strict Sequential SLA Verdict Gates
* **The Problem**: A contestant could submit an extremely fast matching engine that immediately acknowledges every order as filled without actually maintaining an order book. A simple composite score would allow this incorrect engine to rank highly on the leaderboard due to its $100.0$ latency and $100.0$ throughput scores.
* **The Solution**: Enforce strict sequential verdict gates before scoring a submission:
  1. **Correctness Check**: If $\text{Correctness} < 100\% \implies$ **Logic Violation (LV)**.
  2. **Throughput Check**: If $\text{OrdersFailed} > 5$ (Pretest) or $\text{Failure Rate} > 0.1\%$ (Systest) $\implies$ **Correctness Error**. If $\text{TPS Degradation} > 30\% \implies$ **Throughput Degradation**.
  3. **Latency Check**: If $P99 > \text{Ceiling} \implies$ **Tail Latency Exceeded (TLE)**.
  4. Only when all gates are passed can a submission be marked as **Accepted** and receive its full composite score.
* **Result**: Absolute verification that only $100\%$ correct and stable engines are allowed to enter the leaderboard ranking.
* **Trade-offs & Alternatives**:
  * *Alternative*: Soft penalties where correctness is just a weighted parameter (e.g. $40\%$) of the composite score without gates. Rejected because it would allow incorrect engines to score $60/100$, ranking above correct but slower engines ($50/100$), which defeats the primary objective of a matching engine.

---

### Decision 8: Run Variance Stability Bonus (+5.0 Points)
* **The Problem**: Matching engines with inconsistent execution profiles (e.g., erratic GC collections, locks, or unsafe memory page faults) can occasionally get lucky and score well in one trial, but perform poorly in others.
* **The Solution**: Compute the standard deviation ($\sigma$) of the composite scores across the 3 independent runs:
  $$\sigma = \sqrt{\frac{1}{k} \sum_{i=1}^{k} (S_i - \mu)^2}$$
  If $\sigma < 2.0\%$, the submission is awarded a $+5.0$ **Stability Bonus** (capped at $100.0$).
* **Result**: Strongly incentivizes contestants to write highly predictable, zero-allocation, lock-free C++ code with zero latency fluctuation.
* **Trade-offs & Alternatives**:
  * *Alternative*: Taking the best single run out of the 3. Rejected because it encourages contestants to submit volatile code and rely on "lucky" low-jitter scheduling slots.

---

### Decision 9: Post-Contest Reduced-Score Policy
* **The Problem**: Pretests run lighter loads than system tests. A contestant could submit an engine optimized to pass pretests at $100$ points, but which degrades, leaks memory, or fails correctness constraints under full post-contest system tests. Simply replacing the score on the leaderboard could cause a contestant's rank to drop suddenly, confusing them.
* **The Solution**: Enforce a strict "system-test override" rule. If a post-contest system-test run results in a lower score than the pretest run for the same submission, the system-test score takes precedence and overrides the active leaderboard value. The contestant's submission history and previous pretest metadata are preserved in the DB, but their rank is adjusted down.
* **Result**: Leaderboard integrity is protected against scaled performance regressions while maintaining complete submission transparency.
* **Trade-offs & Alternatives**:
  * *Alternative*: Disqualify the submission entirely on system-test regression. Rejected because it is too harsh; an engine that functions correctly at $100\text{ TPS}$ but degrades slightly at $500,000\text{ TPS}$ is still a valid submission and should be scored accordingly.

---

### Decision 10: Multi-Dimensional Engine Archetype Classification
* **The Problem**: Displaying a single score on the leaderboard doesn't capture the engineering philosophy of the submission (e.g., C++ template metaprogramming vs simple lock-free queues).
* **The Solution**: Classify submissions into archetypes based on their sub-score balance:
  * **Latency-Optimized**: $S_{\text{latency}} \ge 70.0 \land S_{\text{correctness}} < 85.0$ (Sacrifices correctness constraints for raw speed).
  * **Accuracy-Optimized**: $S_{\text{correctness}} \ge 95.0 \land S_{\text{latency}} < 30.0$ (Flawless priority logic but slow).
  * **Low-Throughput**: $S_{\text{throughput}} < 70.0$ (Severe failures under pressure).
  * **Balanced**: $S_{\text{correctness}} \ge 80.0 \land S_{\text{latency}} \ge 30.0 \land S_{\text{throughput}} \ge 80.0$.
  * **Unclassified**: Fallback state.
* **Result**: Enriches the developer dashboard, giving contestants clear feedback on their design trade-offs.
* **Trade-offs & Alternatives**:
  * *Alternative*: Manual classification by review. Rejected because it does not scale to thousands of submissions. Algorithmic classification provides instant, automated design diagnostics.

---

## 3. Reference Math Formulation Summary

### Latency Decay
$$S_{\text{bucket}}(L) = \max \left( 0.0, \, \min \left( 100.0, \, 100.0 \times \left( 1.0 - \frac{L - L_{\text{target}}}{L_{\text{ceiling}} - L_{\text{target}}} \right) \right) \right)$$

### Latency Weighting
$$S_{\text{latency}} = (S_{p50} \times 0.20) + (S_{p90} \times 0.30) + (S_{p99} \times 0.50)$$

### Composite Score
$$\text{Base Composite} = (S_{\text{correctness}} \times 0.40) + (S_{\text{latency}} \times 0.30) + (S_{\text{throughput}} \times 0.30)$$

### Final Score with Stability Adjustment
$$\text{Final Score} = \min \left( 100.0, \, \text{Base Composite} + \begin{cases} 5.0 & \sigma_{\text{composite}} < 2.0 \\ 0.0 & \text{otherwise} \end{cases} \right)$$

---

## 4. Implementation References

The scoring code is located in the following files:
* **Verdict Evaluation**: [verdict.go](file:///Users/destructor/Desktop/Kush/Projects/IICPC-BenchGrid/services/testing/verdict.go#L39) - Contains verdict rules, SLA gates, dynamic target lookup, and logarithmic stability scores.
* **Scoring Metrics**: [scoring.go](file:///Users/destructor/Desktop/Kush/Projects/IICPC-BenchGrid/pkg/scoring/scoring.go) - Contains composite, dynamic latency, and throughput score functions.
* **Trial Aggregator**: [main.go](file:///Users/destructor/Desktop/Kush/Projects/IICPC-BenchGrid/services/testing/main.go#L380-L425) - Controls execution of the 3 trial loops, averages results, computes standard deviations, and applies the stability bonus.
* **Correctness Validator**: [validator.go](file:///Users/destructor/Desktop/Kush/Projects/IICPC-BenchGrid/bot-fleet/shadow/validator.go#L501) - Computes correctness percentages and checks priority/value mismatch data.
