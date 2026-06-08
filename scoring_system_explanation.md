# IICPC-BenchGrid Scoring System Architecture

This document provides a comprehensive explanation of how contestant matching engines are evaluated, aggregated, scored, and classified within the IICPC-BenchGrid platform.

---

## 1. High-Level Scoring & Evaluation Pipeline

Each submission undergoes a multi-run trial execution to ensure consistent benchmark readings under load, followed by metric aggregation, verdict gating, scoring calculations, stability adjustments, and classification.

```mermaid
graph TD
    Sub[Submission Upload] --> Comp[Compilation Stage]
    Comp --> RunT[Trial Runs: 3 Trials]
    
    subgraph Execution Loop (3 Trials)
        RunT --> T1[Trial 1 Run]
        RunT --> T2[Trial 2 Run]
        RunT --> T3[Trial 3 Run]
    end

    T1 & T2 & T3 --> Agg[Average Metrics & Run Variance Calculation]
    
    subgraph Scoring & Verdict Resolution
        Agg --> Gate1{Correctness < 100%?}
        Gate1 -- Yes --> LV[Logic Violation - LV]
        Gate1 -- No --> Gate2{P99 Latency > 5000µs?}
        Gate2 -- Yes --> TLE[Tail Latency Exceeded - TLE]
        Gate2 -- No --> Gate3{Fail Rate > 10% OR TPS Degradation > 30%?}
        Gate3 -- Yes --> TD[Throughput Degradation]
        Gate3 -- No --> AC[Accepted]
        
        Agg --> BaseScore[Compute Base Composite Score]
        BaseScore --> StdDev{Trial StdDev < 2.0%?}
        StdDev -- Yes --> Bonus[Apply +5.0 Stability Bonus]
        StdDev -- No --> NoBonus[No Stability Bonus]
    end

    LV & TLE & TD & AC & Bonus & NoBonus --> Finalize[Persist Score & Classify Engine Archetype]
    Finalize --> UI[Update Leaderboard UI]
```

---

## 2. Dynamic Trial Execution

To filter out temporary background noise or CPU spikes on the benchmarking host, each submission is tested across **$k = 3$ trials**.
* The benchmark suite executes $3$ independent trials.
* Raw values for Correctness, P99 Latency, Orders Sent, Orders Failed, Actual TPS, and Phantom/Priority anomalies are collected per trial.
* For scoring and final evaluation, the platform uses the **arithmetic mean** (average) of these metrics across all $3$ trials.

---

## 3. Metric Score Math

The platform calculates three primary sub-scores (each bounded between `0.0` and `100.0`):

### A. Correctness Score ($C$)
* **Source**: The percentage of successfully matched order book math operations, order fill attributions, and price-time priority conformance rules.
* **Range**: $0.0$ to $100.0$ percent.

### B. Latency Score ($L$)
Evaluates the tail latency performance based on the average P99 latency ($P99$ in microseconds):
* **Target ($500\,\mu\text{s}$)**: Any engine with $P99 \le 500\,\mu\text{s}$ receives a perfect latency sub-score ($100.0$).
* **Ceiling ($5000\,\mu\text{s}$)**: Any engine with $P99 \ge 5000\,\mu\text{s}$ receives a zero latency sub-score ($0.0$).
* **Linear Decay**: For latencies between the target and ceiling, the score decays linearly:
  $$L = 100.0 \times \left(1.0 - \frac{P99 - 500.0}{5000.0 - 500.0}\right)$$

### C. Throughput Score ($T$)
Measures stability and message preservation under stress, based on the Order Failure Rate:
* **Failure Rate ($\text{failRate}$)**: The ratio of dropped or unacknowledged orders to the total orders dispatched.
  $$\text{failRate} = \frac{\text{Orders Failed}}{\text{Orders Sent}}$$
* **Formula**:
  $$T = (1.0 - \text{failRate}) \times 100.0$$

---

## 4. Composite Score & Stability Bonus

The final score combines the three sub-scores using a weighted average and applies a bonus based on trial consistency:

### A. Base Composite Score
Calculated using a 40-30-30 weighted index:
$$\text{Base Score} = (T \times 0.3) + (L \times 0.3) + (C \times 0.4)$$

### B. Stability Bonus
To encourage deterministic engines that avoid volatile garbage collection runs or lock contention spikes:
1. The standard deviation ($\sigma$) of the composite scores across the 3 independent runs is computed:
   $$\sigma = \sqrt{\frac{1}{k} \sum_{i=1}^{k} (S_i - \mu)^2}$$
   *(where $S_i$ is the composite score of run $i$, and $\mu$ is the mean run score)*
2. If $\sigma < 2.0\%$, the submission is awarded a **$+5.0$ Stability Bonus**.
3. The final composite score is capped at a maximum of $100.0$:
   $$\text{Final Score} = \min(100.0, \, \text{Base Score} + \text{Stability Bonus})$$

---

## 5. Strict Verdict Gates (SLAs)

Before a submission is marked `Accepted`, it must pass three SLA gates evaluated in order of strict priority:

| Check Order | Gate | Rule | Resulting Verdict | Actionable Reason |
| :---: | :--- | :--- | :--- | :--- |
| **1** | **Correctness Check** | Correctness $< 100.0\%$ | **Logic Violation (LV)** | `Correctness < 100% (Order Book Math Mismatch)` |
| **2** | **Latency Check** | $P99$ Latency $> 5000\,\mu\text{s}$ | **Tail Latency Exceeded (TLE)** | `P99 > 5000µs (Worst-case Tail Spikes)` |
| **3** | **Throughput Check** | Failure Rate $> 10\%$ OR TPS Degradation $> 30\%$ | **Throughput Degradation** | `Failure Rate > 10% (Dropped Orders)` OR `TPS Degradation > 30% (Severe Contention)` |
| **4** | **Gold Standard** | Passes all SLA gates | **Accepted** | `Optimal Execution (Passes all SLAs)` |

---

## 6. Engine Archetype Classification

Matching engines are classified into behavioral profiles based on their multi-dimensional metric signature:

* **Latency-Optimized**: Great latency ($L \ge 70.0$) but sacrifices correctness ($C < 85.0$).
* **Accuracy-Optimized**: Flawless correctness ($C \ge 95.0$) but slow ($L < 30.0$).
* **Low-Throughput**: High failure rate under stress ($T < 70.0$).
* **Balanced**: Good across all categories ($C \ge 80.0 \land L \ge 30.0 \land T \ge 80.0$).
* **Unclassified**: Fallback classification for other metric distributions.
