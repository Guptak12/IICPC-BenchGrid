# IICPC-BenchGrid — Database Schema & Migrations Design

> Every decision grounded in the actual migration files under `migrations/`.
> Migration runner: **Goose** (`+goose Up` / `+goose Down` directives).

---

## 1. Schema Overview

The database is PostgreSQL 15. The schema was built iteratively through 7 ordered migrations, each solving a specific performance or correctness problem discovered during development.

```
┌──────────────────────────────────────────────────────────────────────────┐
│                        IICPC PostgreSQL Schema                           │
│                                                                          │
│  ┌──────────┐   user_id FK   ┌──────────────────┐                       │
│  │  users   │ ◄──────────────┤   submissions    │                       │
│  └──────────┘                │  (hot table)     │                       │
│                              │                  │                       │
│  ┌──────────┐   arena_id FK  │  id, status,     │   submission_id FK    │
│  │  arenas  │ ◄──────────────┤  verdict, scores │──────────────────────►│  submission  │
│  └────┬─────┘                │  diagnostics JSONB│                      │   _sources   │
│       │                      └──────────────────┘                       │  (TOAST)     │
│       │ arena_id FK                                                      └──────────────┘
│  ┌────▼───────────────┐
│  │ arena_registrations │
│  └─────────────────────┘
└──────────────────────────────────────────────────────────────────────────┘
```

---

## 2. Table Definitions

### 2.1 `submissions` (hot table)

Source: `migrations/00001_submissions.sql`, updated by 00002–00007.

```sql
CREATE TABLE submissions (
    id               VARCHAR(36)   PRIMARY KEY,          -- UUID v4
    contestant_id    VARCHAR(255)  NOT NULL,              -- legacy; superceded by user_id in 00005
    contest_id       VARCHAR(255)  NOT NULL DEFAULT 'default',  -- legacy; superceded by arena_id
    user_id          VARCHAR(36)   REFERENCES users(id),         -- added 00005
    arena_id         VARCHAR(36)   REFERENCES arenas(id),        -- added 00005
    github_url       VARCHAR(2048),                              -- added 00004
    status           VARCHAR(50)   NOT NULL DEFAULT 'queued',
    verdict          VARCHAR(50)   NOT NULL DEFAULT 'Pending',
    diagnostics      JSONB         DEFAULT '{}'::jsonb,
    composite_score  DOUBLE PRECISION DEFAULT 0.0,
    correctness_score DOUBLE PRECISION DEFAULT 0.0,
    p50_us           BIGINT DEFAULT 0,
    p90_us           BIGINT DEFAULT 0,
    p99_us           BIGINT DEFAULT 0,
    actual_tps       DOUBLE PRECISION DEFAULT 0.0,
    s3_path          VARCHAR(512),
    created_at       TIMESTAMPTZ   DEFAULT CURRENT_TIMESTAMP,
    updated_at       TIMESTAMPTZ   DEFAULT CURRENT_TIMESTAMP  -- auto-updated by trigger
);
```

**`status` state machine:**

```
queued → compiling → building → running → completed
                                        ↘ failed
```

**`diagnostics` JSONB structure** (written by `services/testing/runner.go`):
```json
{
  "trial_results": [...],
  "avg_tps": 12430.5,
  "avg_p99_us": 842,
  "avg_correctness": 100.0,
  "orders_sent": 1000000,
  "orders_failed": 0,
  "phantom_fills": 0,
  "priority_violations": 0,
  "engine_archetype": "Latency-Optimized",
  "stability_bonus": 5.0,
  "error": ""
}
```

**Design decision — JSONB for diagnostics**: Telemetry fields are high-cardinality and evolve as the scoring formula changes. Storing them in JSONB rather than typed columns avoids frequent `ALTER TABLE` migrations for every new metric. The tradeoff is that queries on individual fields require `->>'field'` extraction — acceptable because the hot leaderboard query only reads the top-level numeric columns (`composite_score`, `p99_us`, etc.), not the JSONB internals.

---

### 2.2 `submission_sources` (cold TOAST table)

Source: `migrations/00003_separate_source_code.sql`

```sql
CREATE TABLE submission_sources (
    submission_id VARCHAR(36) PRIMARY KEY REFERENCES submissions(id) ON DELETE CASCADE,
    source_code   TEXT NOT NULL,
    created_at    TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP
);
```

**Why a separate table?**

`source_code` is a contestant's full submission ZIP extracted as text — frequently several hundred KB. Storing it inline in `submissions` caused two problems:

1. **Index bloat**: PostgreSQL btree indexes store full row widths for INCLUDE columns. A 500KB `TEXT` column in a covering index pushes index rows past the 2704-byte hard limit.
2. **Sequential scan amplification**: `EXPLAIN ANALYZE` on the leaderboard query showed that PostgreSQL was fetching 512KB+ rows from the heap just to read 6 numeric columns. Separating `source_code` reduces the `submissions` heap tuple width from ~520KB to ~800 bytes, making buffer-pool cache hits ~650× more likely.

`submission_sources` uses PostgreSQL **TOAST** (The Oversized-Attribute Storage Technique) automatically: `TEXT` columns > 2KB are compressed and stored out-of-line. Reads of `source_code` only hit disk when the dashboard explicitly fetches the source viewer — the leaderboard query never touches this table.

---

### 2.3 `users`

Source: `migrations/00005_users_and_arenas.sql`

```sql
CREATE TABLE users (
    id            VARCHAR(36)  PRIMARY KEY,
    handle        VARCHAR(64)  UNIQUE NOT NULL,
    email         VARCHAR(255) UNIQUE,
    password_hash VARCHAR(255),            -- NULL for GitHub OAuth users
    github_id     VARCHAR(64)  UNIQUE,     -- GitHub OAuth ID
    role          VARCHAR(20)  DEFAULT 'contestant',  -- contestant | admin
    created_at    TIMESTAMPTZ  DEFAULT NOW()
);
```

**Auth model**: `password_hash` is `NULL` for GitHub OAuth users; `github_id` is `NULL` for password users. The `UNIQUE` constraint on both allows either path without a separate auth provider table.

---

### 2.4 `arenas`

Source: `migrations/00005_users_and_arenas.sql`

```sql
CREATE TABLE arenas (
    id          VARCHAR(36)  PRIMARY KEY,
    title       VARCHAR(255) NOT NULL,
    description TEXT,
    status      VARCHAR(20)  DEFAULT 'upcoming',  -- upcoming | active | system_test | ended
    start_time  TIMESTAMPTZ NOT NULL,
    end_time    TIMESTAMPTZ NOT NULL,
    created_at  TIMESTAMPTZ DEFAULT NOW()
);
```

**`status` state machine**:
```
upcoming → active → system_test → ended
```
System test is a separate phase — during `system_test` the platform auto-triggers rejudging all submissions with the full 500-bot × 2000-order load. Gateway rejects new submissions while `status = system_test`.

---

### 2.5 `arena_registrations`

```sql
CREATE TABLE arena_registrations (
    arena_id     VARCHAR(36) REFERENCES arenas(id) ON DELETE CASCADE,
    user_id      VARCHAR(36) REFERENCES users(id)  ON DELETE CASCADE,
    registered_at TIMESTAMPTZ DEFAULT NOW(),
    PRIMARY KEY (arena_id, user_id)
);
```

Composite primary key prevents duplicate registrations without a separate unique index. `ON DELETE CASCADE` ensures referential integrity when arenas or users are removed.

---

## 3. Index Strategy

### 3.1 Leaderboard Index Evolution

The leaderboard index went through **4 iterations** as query patterns and data volumes changed:

| Migration | Index | Problem it solved |
|---|---|---|
| 00001 | `(contest_id, contestant_id, composite_score DESC)` | Initial multi-contest leaderboard |
| 00002 | `(status, contestant_id, composite_score DESC, updated_at ASC)` | Filter to `completed` submissions before ranking |
| 00003 | `... INCLUDE (id, verdict, correctness_score, p50_us, p90_us, p99_us, actual_tps, diagnostics)` | Covering index — leaderboard query hits index only, no heap fetch |
| 00006 | `(arena_id, user_id, composite_score DESC)` CONCURRENTLY | Partition by arena; `ROW_NUMBER() OVER (PARTITION BY user_id)` for best-of-N |
| **00007** | `... INCLUDE (id, verdict, correctness_score, p50_us, p90_us, p99_us, actual_tps)` (no `diagnostics`) | **Bug fix**: `diagnostics` JSONB pushed btree row to 3752 bytes > 2704-byte limit → index creation failed |

**Migration 00007 root cause**: PostgreSQL btree indexes have a hard max index row size of 2704 bytes (¼ of the default 8KB page size). The `INCLUDE`d `diagnostics` column, which holds the full telemetry JSON (~1–3KB), pushed the index tuple over this limit. The fix was to remove `diagnostics` from `INCLUDE` — the leaderboard query does not need it; the dashboard drawer does a separate `SELECT diagnostics FROM submissions WHERE id = $1` heap fetch.

### 3.2 Full Index Inventory

| Index | Columns | Type | Purpose |
|---|---|---|---|
| `idx_submissions_leaderboard` (00006) | `(arena_id, user_id, composite_score DESC)` | btree CONCURRENTLY | Leaderboard ranking by arena |
| `idx_submissions_leaderboard_v4` (00007) | `(status, contestant_id, composite_score DESC, updated_at ASC) INCLUDE (id, verdict, ...)` | Covering btree | Fast leaderboard read without heap fetch |
| `idx_submissions_diagnostics_gin` (00003) | `diagnostics jsonb_path_ops` | GIN | Dashboard JSONB path queries |
| `idx_submissions_active` (00003) | `(created_at DESC) WHERE status IN ('queued','compiling','running')` | Partial btree | Active submission count for dashboard stat cards |
| `idx_submissions_status` (00006) | `(status) WHERE status IN ('queued','building','running')` | Partial btree CONCURRENTLY | Live job counter |
| `idx_submissions_arena_status` (00005) | `(arena_id, status)` | btree | Gateway: count pending submissions per arena |
| `idx_submissions_user_arena` (00005) | `(user_id, arena_id)` | btree | Contestant submission history per arena |

**`CREATE INDEX CONCURRENTLY`** is used in 00006 for production safety: standard `CREATE INDEX` takes an `AccessExclusiveLock`, blocking all reads and writes. `CONCURRENTLY` takes only a `ShareUpdateExclusiveLock`, allowing reads/writes to continue — critical during a live contest.

**Why `NO TRANSACTION` in 00006**: Goose wraps each migration in a transaction by default. `CREATE INDEX CONCURRENTLY` cannot run inside a transaction block (it requires multiple transaction passes to build without locking). The `-- +goose NO TRANSACTION` directive disables the wrapper.

---

## 4. Migration Execution

### Runner: Goose

All migrations use [Goose](https://pressly.github.io/goose/) format. The runner is a one-shot Kubernetes Job (`k8s/init-db.yaml`) that runs on every deploy before any service starts.

```
k8s/init-db.yaml  →  Dockerfile.init-db  →  goose postgres $DSN up
```

The Job mounts `ConfigMap`-sourced SQL files and runs `goose` sequentially. `backoffLimit: 5` allows retries if the database is not yet reachable.

### Running migrations manually

```bash
# Local (standalone Go mode)
goose -dir migrations postgres "postgres://iicpc:iicpc_secret@localhost:5432/iicpc_db" up

# Local (Kind cluster)
kubectl run pg-client --image=postgres:15-alpine --restart=Never --rm -i \
  -- psql "postgres://iicpc:iicpc_secret@postgres:5432/iicpc_db" \
  -f /migrations/00007_fix_leaderboard_index.sql

# EKS (production)
kubectl run pg-client --image=postgres:15-alpine --restart=Never --rm -i \
  --env="PGPASSWORD=iicpc_secret_production" \
  -- psql "postgres://iicpc@<RDS_ENDPOINT>:5432/iicpc_db" -c "\dt"
```

### Rolling back a migration

```bash
goose -dir migrations postgres "$DSN" down       # roll back the last migration
goose -dir migrations postgres "$DSN" status     # show applied/pending migrations
```

---

## 5. Key Engineering Decisions Summary

| Decision | Alternative considered | Reason chosen |
|---|---|---|
| JSONB for `diagnostics` | 15+ typed columns for every metric | Schema flexibility — scoring formula evolves; avoids repeated `ALTER TABLE` |
| Separate `submission_sources` table | `TEXT` column in `submissions` | Keeps hot table tuples ~800B; prevents TOAST-induced heap bloat on leaderboard scans |
| `INCLUDE`-only numeric columns in covering index | Include all columns including JSONB | JSONB blows past 2704-byte btree row limit (00007 bug fix) |
| `CREATE INDEX CONCURRENTLY` in 00006 | Standard `CREATE INDEX` | Avoids `AccessExclusiveLock` during live contest |
| Goose migration runner | Flyway, Liquibase, raw psql | Goose is Go-native, supports `StatementBegin`/`StatementEnd` for multi-statement blocks, and integrates cleanly into the Kubernetes Job init container |
| `updated_at` trigger | Application-layer timestamp | Trigger is unconditional — no bug can leave `updated_at` stale regardless of which service writes the row |
