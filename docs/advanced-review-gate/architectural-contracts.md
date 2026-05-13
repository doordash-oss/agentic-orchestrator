# Architectural Contracts

Structural invariants that tests alone cannot enforce. Contracts are the rules that [guardian policies](guardian-policies.md) enforce and [validators](agent-configurations.md) check.

## What Architectural Contracts Are

An architectural contract is a named structural property that the codebase must maintain across all changes. Unlike behavioral specifications (which tests verify at call sites), contracts describe *relationships between components* — transaction ownership, API evolution rules, authentication flow requirements, data propagation guarantees.

Contracts exist because certain properties are invisible to tests:

| Property | Why Tests Miss It |
|----------|-------------------|
| Transaction boundaries | Tests mock the database; they don't verify which layer starts transactions |
| Retry policies | Unit tests don't simulate real network partitions or backoff behavior at scale |
| Schema evolution | A migration passes locally but breaks downstream consumers that aren't in the test suite |
| Concurrency invariants | Race conditions are non-deterministic; passing tests prove nothing about thread safety |
| Data propagation timing | Tests verify "data arrives" but not "data arrives within 72 hours across all stores" |
| Precision guarantees | Floating-point tests pass at 6 decimal places but the contract requires 8 |

Contracts fill this gap by declaring the invariant explicitly so that AI agents, reviewers, and automated systems can enforce it during planning and review — not just at runtime.

## Contract Format

```yaml
contract:
  name: "<kebab-case identifier>"
  scope: "<package, service, or cross-cutting>"
  rules:
    - "<rule 1: MUST or MUST NOT statement>"
    - "<rule 2>"
  pacelc_trade_off:
    partition_behavior: "<what happens during network partition>"
    normal_operation: "<latency vs. consistency trade-off during normal operation>"
  violation_escalation: "<what happens when this contract is violated>"
  adr_reference: "<ADR number or 'none'>"
  owner: "<team or role responsible for this contract>"
```

The `pacelc_trade_off` field encodes the [PACELC](https://en.wikipedia.org/wiki/PACELC_theorem) trade-off: during a **P**artition, choose **A**vailability or **C**onsistency; **E**lse (normal operation), choose **L**atency or **C**onsistency. Making this explicit prevents AI agents from making implicit trade-off decisions that contradict the system's design intent.

## Example Contracts

### 1. Transaction Boundaries

```yaml
contract:
  name: "transaction-ownership"
  scope: "cross-cutting (all services)"
  rules:
    - "Business logic MUST NOT start, commit, or rollback database transactions"
    - "The repository layer owns all transaction management"
    - "Business logic receives a context-bound transaction and operates within it"
    - "Transaction scope MUST NOT span multiple service boundaries"
  pacelc_trade_off:
    partition_behavior: "Favor availability — accept eventual consistency across services; each service commits independently"
    normal_operation: "Favor consistency with bounded latency — single-service transactions are strongly consistent; cross-service coordination uses saga pattern with compensating actions"
  violation_escalation: "VETO — transaction boundary violations cause data integrity issues that are expensive to detect and repair"
  adr_reference: "ADR-012"
  owner: "Platform team"
```

**Why this matters for AI agents**: An LLM generating a new service method will instinctively wrap database calls in `BEGIN`/`COMMIT`. This contract ensures the agent delegates transaction management to the repository layer, preserving the separation that makes the codebase testable and the failure modes predictable.

### 2. API Evolution

```yaml
contract:
  name: "api-version-evolution"
  scope: "all public APIs"
  rules:
    - "Breaking changes MUST be introduced in a new API version (e.g., /v2/)"
    - "The previous version MUST remain functional with a 6-month deprecation notice"
    - "Deprecation notices MUST include sunset date and migration guide"
    - "Additive changes (new fields, new endpoints) do not require a new version"
    - "Field removal, type changes, and semantic changes are breaking"
  pacelc_trade_off:
    partition_behavior: "Not applicable — API versioning is a design-time concern"
    normal_operation: "Favor latency — version routing adds negligible overhead; do not introduce version negotiation that adds round-trips"
  violation_escalation: "VETO — breaking API changes without versioning break downstream consumers silently"
  adr_reference: "ADR-007"
  owner: "API platform team"
```

### 3. Authentication

```yaml
contract:
  name: "authentication-authorization"
  scope: "all API endpoints"
  rules:
    - "Every endpoint MUST use the JWT middleware for authentication"
    - "Authorization MUST be checked via AuthzService — never inline permission logic"
    - "Custom auth implementations are prohibited; extend the existing middleware instead"
    - "Endpoints intentionally public MUST be annotated with @public and a justification"
  pacelc_trade_off:
    partition_behavior: "Favor consistency — deny access if the auth service is unreachable; never fail-open"
    normal_operation: "Favor latency — cache auth decisions with short TTL (< 60s) to avoid per-request auth service calls"
  violation_escalation: "VETO — authentication bypasses are security incidents"
  adr_reference: "ADR-001"
  owner: "Security team"
```

### 4. Data Retention

```yaml
contract:
  name: "pii-deletion-propagation"
  scope: "all services storing PII"
  rules:
    - "PII deletion requests MUST propagate to all downstream data stores within 72 hours"
    - "Each service MUST register its PII-containing stores in the data catalog"
    - "Deletion completion MUST be logged with timestamp and store identifier for audit"
    - "New PII storage locations MUST be added to the propagation pipeline before launch"
  pacelc_trade_off:
    partition_behavior: "Favor consistency — queue deletion requests for retry; never silently drop a deletion"
    normal_operation: "Favor latency — deletions are async and batched; 72-hour SLA allows for efficient bulk processing"
  violation_escalation: "VETO — PII retention violations carry regulatory penalties (GDPR, CCPA)"
  adr_reference: "none (regulatory requirement)"
  owner: "Data governance team"
```

## When to Create a New Contract

Create a new contract when:

| Trigger | Example |
|---------|---------|
| Adding a new service | Define which contracts the service inherits and any new cross-service invariants |
| Changing data ownership | Specify who owns writes, who owns reads, and propagation guarantees |
| Introducing a new integration pattern | Document the expected failure modes, retry policies, and consistency guarantees |
| Post-incident | When an incident reveals an invariant that was assumed but not documented |
| New compliance requirement | When regulatory or security requirements introduce new structural constraints |

Do **not** create contracts for:
- Style preferences (use linters)
- Behavioral specifications (use tests)
- Temporary conventions (use ADR with expiry)

## Relationship to Guardian Policies

Contracts and [guardian policies](guardian-policies.md) are complementary:

| Concern | Contract | Guardian Policy |
|---------|----------|-----------------|
| What it describes | The structural invariant itself | The enforcement rule for the invariant |
| Audience | Engineers, architects, AI agents understanding *why* | Automated systems deciding *pass/fail* |
| Format | Descriptive with PACELC context | Machine-readable with severity and detection method |
| Example | "The repository layer owns all transactions" | `severity: VETO` if business logic contains `tx.Begin()` |

A contract explains the architectural reasoning. A guardian policy encodes the binary check. When a validator in the [plan validation gate](workflow-details.md) flags an issue, it references the contract for context and the policy for the enforcement decision.

## Relationship to Validation Scoring

Contracts inform the [scoring model](validation-scoring.md) but operate on a different axis:

- **Scoring** evaluates *how well* a plan addresses a domain (architecture, security, performance, testing). Scores allow trade-offs between domains.
- **Contracts** define *binary invariants*. A plan either satisfies a contract or it doesn't. Contract violations trigger hard vetoes regardless of the overall score.

A plan scoring 95/100 can still be vetoed by a single contract violation. Both mechanisms are necessary — scoring catches quality issues, contracts catch structural violations.
