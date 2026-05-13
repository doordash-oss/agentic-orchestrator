# Workflow Examples

Three end-to-end examples showing how the advanced review gate operates at each risk tier. Each example traces the full lifecycle from [task assessment through artifact validation](workflow-details.md), showing how the [validation scoring](validation-scoring.md) and [guardian policies](guardian-policies.md) interact with risk-proportionate autonomy.

---

## Low Risk: Fix Off-by-One Error in Pagination

A user reports that the last page of results is missing one item.

### Task Assessment

| Dimension | Value |
|-----------|-------|
| Risk level | **Low** |
| File scope | Single file (`internal/tui/dashboard.go`) |
| API surface | No API changes |
| Data impact | Read-only display logic |
| Security relevance | None |
| Reversibility | Trivially revertible |

### Research

Skipped. The bug is localized and the fix is obvious from the description. For low-risk single-file changes, research adds cost without value.

### Intent

Minimal — one-sentence objective is sufficient at this risk level:

```
Fix pagination off-by-one: UserList shows count < total instead of count <= total,
dropping the last item when total is an exact multiple of page size.
```

### Plan

Brief, single-phase:

```
Phase 1:
  - Modify pageCount calculation in dashboard.go: change `count < total` to `count <= total`
  - Add test case: total=20, pageSize=10 should yield 2 pages (not 1)
```

### Plan Validation

| Validator | Verdict | Notes |
|-----------|---------|-------|
| Architecture | PASS | Single-file change, no boundary violations |
| Security | PASS | Display logic only |
| Performance | PASS | No hot-path impact |
| Testing | PASS | Test case covers the boundary condition |

### Implementation

2-line code change + 1 new test case. Agent implements directly from the brief plan.

### Decision

Auto-proceed. Low risk + all validators pass = no human gate required beyond the initial intent definition.

### Summary

| Metric | Value |
|--------|-------|
| Human involvement | Intent definition only |
| Validation iterations | 1 (all pass first attempt) |
| Implementation iterations | 1 |
| Approximate wall time | ~10 minutes |
| Key value | Even trivial changes pass through validation, establishing the habit and catching the rare "trivial" change that isn't |

---

## Medium Risk: Add CSV Export to Admin Dashboard

Product requests a "Download CSV" button on the admin dashboard for user activity reports.

### Task Assessment

| Dimension | Value |
|-----------|-------|
| Risk level | **Medium** |
| File scope | 2–5 files (new endpoint, service method, UI component, tests) |
| API surface | New internal API endpoint |
| Data impact | Read-only (exports existing data) |
| Security relevance | Moderate — exports may contain PII |
| Reversibility | Revertible (additive feature) |

### Research

Quick scan of the codebase reveals:
- Existing export patterns: `internal/report/` has a PDF export that uses streaming writes — follow the same pattern
- Data access: Activity data is in the `audit_log` table, accessed via `AuditRepository`
- Auth pattern: Admin endpoints use `RequireRole("admin")` middleware
- Large dataset handling: PDF export streams to avoid memory spikes; CSV should do the same

Research output saved to `{state_dir}/{feature_id}/research/output.txt`.

### Intent

Standard structure — objective, scope, requirements:

```yaml
intent:
  objective:
    what: Add CSV export endpoint for user activity data on admin dashboard
    why: Admin team manually exports data via SQL queries; self-service export saves ~5 hours/week
  scope:
    affected_systems: [admin-api, admin-ui]
    out_of_scope: [scheduled exports, email delivery]
  requirements:
    functional:
      - Download button on activity report page
      - CSV includes: timestamp, user_id, action, details
      - Support date range filter (max 90 days)
      - File size limit: 50MB (truncate with warning)
    non_functional:
      - Streaming response for large datasets (no full materialization in memory)
      - Export must not degrade dashboard query performance
```

### Plan

Detailed, multi-phase:

```
Phase 1: Backend
  - New endpoint: GET /admin/api/v1/activity/export?start=&end=&format=csv
  - Service method: ActivityService.ExportCSV() using streaming writer pattern from report/
  - Query optimization: use read replica for export queries
  - File size check: estimate row count before streaming; return 413 if > 50MB

Phase 2: Frontend
  - Download button component on activity report page
  - Date range picker integration with existing filter state
  - Loading state during export, error handling for 413/500

Phase 3: Tests
  - Unit: CSV formatting, date range validation, size limit enforcement
  - Integration: end-to-end export with test database
  - Performance: verify streaming memory usage stays bounded
```

### Plan Validation — First Attempt

| Validator | Score | Verdict | Notes |
|-----------|-------|---------|-------|
| Architecture | 4/5 | PASS | Follows existing export patterns |
| Security | 3/5 | **CHANGES_REQUESTED** | No auth check on export endpoint. Activity data contains PII — export must be restricted to admin role and logged for audit. |
| Performance | 4/5 | PASS | Streaming approach is correct |
| Testing | 4/5 | PASS | Coverage is adequate |

### Plan Revision

Agent revises the plan based on security validator feedback:
- Add `RequireRole("admin")` middleware to export endpoint
- Add audit log entry for each export (who, when, date range, row count)
- Redact PII fields configurable via export config (default: mask `user_email`)

### Plan Validation — Second Attempt

| Validator | Score | Verdict | Notes |
|-----------|-------|---------|-------|
| Architecture | 4/5 | PASS | — |
| Security | 5/5 | PASS | Auth, audit logging, and PII handling addressed |
| Performance | 4/5 | PASS | — |
| Testing | 4/5 | PASS | — |

### Implementation

- New controller: `admin/api/activity_export.go`
- New service method: `ActivityService.ExportCSV()`
- UI component: download button with date range
- Tests: unit + integration + streaming memory test
- 3 implementation iterations (first pass missed audit logging, second pass had CSV header formatting bug, third pass clean)

### Decision

Auto-proceed after second validation attempt. Medium risk + all validators pass = no mandatory human gate. The security feedback loop added meaningful protection that the original plan missed.

### Summary

| Metric | Value |
|--------|-------|
| Human involvement | Intent definition, review of security validator feedback |
| Validation iterations | 2 (security flag on first, all pass on second) |
| Implementation iterations | 3 |
| Approximate wall time | ~45 minutes |
| Key value | Security validator caught a missing auth check that would have shipped to production. Specialized review found what generalist review would likely miss. |

---

## High Risk: Migrate Session Storage from Memory to Redis

The in-memory session store doesn't survive process restarts. Sessions must be moved to Redis for persistence and horizontal scaling.

### Task Assessment

| Dimension | Value |
|-----------|-------|
| Risk level | **High** |
| File scope | Cross-cutting — session management touches auth, middleware, config, deployment |
| API surface | Internal behavioral change (session API stays the same, storage backend changes) |
| Data impact | Active user sessions affected — data loss on botched migration |
| Security relevance | High — session tokens, auth state |
| Reversibility | Requires rollback plan (dual-write/read during transition) |

### Research

Deep analysis — the agent explores broadly before proposing anything:

1. **Current session implementation**: `SessionStore` interface in `pkg/session/store.go` with `MemoryStore` implementation. Interface has `Get`, `Set`, `Delete`, `Cleanup` methods. ~15 call sites.
2. **Redis patterns in codebase**: Redis client already in `pkg/cache/redis.go` using `go-redis/v9`. Connection pooling configured via `config.RedisConfig`. Used by rate limiter and feature flags.
3. **Relevant ADRs**: ADR-023 (session management) specifies that session data must be encrypted at rest. ADR-019 (infrastructure changes) requires staged rollout with feature flags.
4. **Existing test patterns**: `MemoryStore` has table-driven tests. Integration tests use `miniredis` for Redis testing.
5. **Failure modes**: If Redis becomes unavailable, all authenticated users lose sessions simultaneously. Need circuit breaker or fallback.

Research output: comprehensive document covering all 5 areas, saved to `{state_dir}/{feature_id}/research/output.txt`.

### Intent

Comprehensive — high-risk changes require business context, metrics, and rollback strategy:

```yaml
intent:
  metadata:
    risk_level: high

  objective:
    what: Migrate session storage from in-memory to Redis for persistence and horizontal scaling
    why: >
      In-memory sessions are lost on deploy (forcing ~2000 users to re-authenticate)
      and prevent horizontal scaling (sessions are pinned to a single instance).

  requirements:
    functional:
      - Sessions survive process restarts
      - Sessions accessible from any instance (horizontal scaling)
      - Session data encrypted at rest per ADR-023
    non_functional:
      - Session read latency < 5ms p99
      - Session write latency < 10ms p99
      - Zero session loss during migration
    constraints:
      - Must use existing Redis infrastructure (no new clusters)
      - SessionStore interface must not change (preserve all 15 call sites)
      - Staged rollout with feature flag

  business_context:
    objective: Eliminate forced re-authentication on deploys
    success_metrics:
      - "Re-auth rate drops from ~2000/deploy to 0"
      - "Session read p99 < 5ms"
    guardrails:
      - "No increase in auth-related 5xx errors during migration"
    rollback_strategy: >
      Feature flag switches reads back to memory store.
      Dual-write ensures memory store stays populated during rollout.
      Rollback decision: if session error rate > 0.5% for 10 minutes.
```

### Plan

Detailed, 4-phase with explicit failure modes:

```
Phase 1: RedisStore implementation
  - Implement RedisStore satisfying SessionStore interface
  - Encryption at rest using AES-256-GCM (key from secrets manager)
  - Connection pooling: reuse existing RedisConfig
  - Serialization: JSON with gzip for sessions > 1KB
  - Tests: unit (encrypt/decrypt, serialization) + integration (miniredis)

Phase 2: Dual-write migration layer
  - DualStore wrapper: writes to both MemoryStore and RedisStore
  - Reads: configurable primary (memory or redis) via feature flag
  - Consistency check: log discrepancies between stores during dual-write
  - Feature flag: session_store_backend = "memory" | "redis" | "dual"
  - Circuit breaker: if Redis fails 5 consecutive ops, fall back to memory-only

Phase 3: Staged rollout
  - Deploy with dual-write, memory-primary (flag: "dual")
  - Validate: monitor discrepancy rate, Redis latency, error rate
  - Switch to redis-primary (flag: "redis")
  - Validate: monitor session read latency, auth error rate
  - Remove dual-write after 1 week of stable redis-primary

Phase 4: Cleanup
  - Remove MemoryStore and DualStore
  - Remove feature flag
  - Update ADR-023 with Redis implementation details
```

### Plan Validation

| Validator | Score | Verdict | Notes |
|-----------|-------|---------|-------|
| Architecture | 5/5 | PASS | Clean interface implementation, respects existing patterns |
| Security | 4/5 | PASS | Encryption at rest addressed. Note: ensure session tokens are not logged during dual-write discrepancy logging. |
| Performance | 3/5 | **CHANGES_REQUESTED** | Connection pool sizing not specified. With ~2000 concurrent sessions and 4 instances, need explicit pool size analysis. Also: what's the Redis memory budget? |
| Testing | 5/5 | PASS | Comprehensive strategy including integration and staged rollout validation |

### Senior Review

Required regardless of validation score — all high-risk changes require senior human review.

Senior engineer reviews the plan and requests one change:
> "Add Redis connection pool config to Phase 1. Pool size = max_concurrent_sessions / instance_count * 1.5 = 750. Also add a Redis memory budget: 2000 sessions × ~2KB avg = ~4MB — well within current Redis capacity. Document this in the plan."

### Plan Revision and Re-Validation

Plan revised with pool sizing analysis. Re-validated:

| Validator | Score | Verdict |
|-----------|-------|---------|
| Architecture | 5/5 | PASS |
| Security | 5/5 | PASS |
| Performance | 5/5 | PASS |
| Testing | 5/5 | PASS |

### Implementation

- `RedisStore` implementation with encryption
- `DualStore` migration wrapper with circuit breaker
- Feature flag integration
- Full test suite (unit, integration with miniredis, circuit breaker tests)
- Config changes for connection pool sizing
- 5 implementation iterations (encryption edge case, circuit breaker timing, discrepancy logging format, test flakiness, final cleanup)

### Decision

Human approval required. High-risk changes never auto-proceed, regardless of validation scores. Senior reviews the PR, verifies the rollout plan, and approves.

### Summary

| Metric | Value |
|--------|-------|
| Human involvement | Intent definition, senior plan review, senior PR approval |
| Validation iterations | 2 (performance flag on first, all pass on second) |
| Implementation iterations | 5 |
| Approximate wall time | ~4 hours |
| Key value | Performance validator caught missing pool sizing analysis. Senior review added concrete capacity numbers. The staged rollout plan (dual-write → redis-primary → cleanup) would not emerge from a generalist "implement Redis sessions" prompt. |

---

## Pattern Summary

| Dimension | Low Risk | Medium Risk | High Risk |
|-----------|----------|-------------|-----------|
| Research depth | Skip or minimal | Quick scan | Deep analysis |
| Intent detail | One sentence | Structured (objective, scope, requirements) | Comprehensive (+ business context, metrics, rollback) |
| Plan detail | Single phase, brief | Multi-phase, detailed | Multi-phase with failure modes and rollout strategy |
| Validation rigor | All validators run, single pass expected | All validators run, 1–2 iterations typical | All validators run + mandatory senior review |
| Human gates | Intent only | Intent + review of validator feedback | Intent + senior plan review + senior PR approval |
| Auto-proceed eligible | Yes (if validators pass) | Yes (if validators pass) | Never — always requires human approval |
| Typical wall time | ~10 minutes | ~45 minutes | ~4 hours |

The risk classification determines how much governance overhead the system applies. Low-risk changes move fast with light validation. High-risk changes front-load the cost of understanding and review — because the cost of getting them wrong is orders of magnitude higher.
