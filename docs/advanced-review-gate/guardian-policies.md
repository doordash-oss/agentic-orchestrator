# Guardian Policies

Declarative rules that encode architectural invariants the codebase must always satisfy.

## What Guardian Policies Are

A guardian policy is a named, machine-readable rule that expresses an invariant — something that must *always* (or must *never*) be true about the codebase. Policies are not suggestions or best practices; they are hard constraints that act as an architectural backstop.

Policies encode the judgment of senior and principal engineers into a form that AI agents and automated systems can evaluate. This decouples the protective function (enforcing constraints) from the creative function (proposing changes), allowing AI to iterate quickly while the guardrails remain firm.

## Why Both Scoring AND Policies

Validation scoring and guardian policies serve fundamentally different purposes:

| Dimension | Scoring | Policies |
|-----------|---------|----------|
| Nature | Evaluative — how good is this? | Binary — does this violate an invariant? |
| Output | Numeric score (0–100) | PASS or VETO |
| Trade-offs | Allowed between domains | Not allowed — a violation is a violation |
| Flexibility | Threshold is configurable | Rule is absolute (unless exception granted) |
| Example | "Architecture score: 82/100" | "All API endpoints require authentication" |

**Scoring** answers: "Is this plan good enough overall?"
**Policies** answer: "Does this plan break any hard rules?"

A plan can score 95/100 and still be vetoed by a single policy violation. Conversely, a plan at exactly 80/100 (threshold) passes if no policies are violated. Both mechanisms are necessary.

## Policy Categories

### ADR Enforcement

Policies derived from Architecture Decision Records. These ensure that past architectural decisions are respected by AI agents that may not be aware of the historical context.

| Policy | Source | Example |
|--------|--------|---------|
| DB connection pooling | ADR-042 | "All database access must use the connection pool from `pkg/db`. Direct `sql.Open()` calls are prohibited." |
| Event schema registry | ADR-017 | "All async events must be registered in the schema registry before publishing." |
| Error wrapping | ADR-003 | "Errors must be wrapped with context using `fmt.Errorf('...%w', err)`. Bare `return err` is prohibited in non-trivial functions." |

### Security Invariants

Non-negotiable security properties derived from security standards and incident post-mortems.

| Policy | Source | Example |
|--------|--------|---------|
| Endpoint authentication | OWASP A01 | "All API endpoints must have explicit authentication. No endpoint may be added without an auth middleware or explicit `@public` annotation." |
| No hardcoded secrets | OWASP A02 | "Credentials, API keys, and tokens must not appear in source code. Use the secrets manager." |
| Input validation | OWASP A03 | "All user-provided input must be validated before use in queries, commands, or file paths." |
| PII logging | GDPR Art. 5 | "PII fields (email, phone, SSN, payment info) must not appear in log output." |

### Compliance Rules

Policies required by regulatory frameworks or internal compliance mandates.

| Policy | Source | Example |
|--------|--------|---------|
| Audit logging | SOX | "Financial transactions must produce audit log entries with actor, action, timestamp, and affected records." |
| Data retention | GDPR | "User data deletion requests must be propagated to all downstream data stores within 30 days." |
| Access logging | SOC 2 | "Access to customer data must be logged with sufficient detail for audit." |

### Architectural Boundaries

Policies that enforce module boundaries, dependency direction, and separation of concerns.

| Policy | Source | Example |
|--------|--------|---------|
| Service isolation | ADR-001 | "Services must not import packages from other services. Shared code goes in `pkg/`." |
| Dependency direction | Clean Architecture | "Domain packages must not import infrastructure packages. Dependencies point inward." |
| No circular imports | Go convention | "Circular package dependencies are prohibited." |
| Transport/domain separation | ADR-008 | "HTTP handlers must not contain business logic. Business logic must not import `net/http`." |

## Policy Format

```yaml
policy:
  name: "require-endpoint-auth"
  category: security_invariant
  severity: VETO | WARNING | ADVISORY
  pattern: |
    Human-readable description of what the policy checks.
    "All API endpoints must have explicit authentication middleware."
  detection_method: |
    How to check compliance.
    "Verify that every route registration includes an auth middleware
    call, or the endpoint is annotated as @public with justification."
  reference: "OWASP-A01, internal security policy §3.2"
  description: |
    Extended context for why this policy exists.
    "After incident INC-2024-087, we determined that 3 endpoints
    were deployed without auth due to copy-paste from a test handler.
    This policy prevents recurrence."
```

### Severity Levels

| Severity | Effect | Use For |
|----------|--------|---------|
| **VETO** | Blocks the change. No override without exception process. | Security invariants, data integrity rules, compliance requirements |
| **WARNING** | Flags in review. Human must acknowledge. | Architectural preferences, performance concerns, deprecation notices |
| **ADVISORY** | Informational note in review output. | Style preferences, optional improvements, awareness items |

## Exception Process

Policies are absolute by default, but real-world systems occasionally need exceptions. The exception process ensures exceptions are tracked, time-bounded, and visible.

**Requirements for an exception**:

1. **Senior approval**: A senior or principal engineer must approve the exception with written justification.
2. **Expiry**: Every exception has a 90-day expiry. After 90 days, the exception must be re-approved or the policy violation must be resolved.
3. **Tracking ticket**: A ticket must be created to track resolution of the underlying issue that necessitated the exception.
4. **Scope**: The exception applies to a specific change or set of changes, not globally.

```yaml
exception:
  policy: "require-endpoint-auth"
  approved_by: "jane.doe (Principal Engineer)"
  justification: |
    Health check endpoint must be unauthenticated for load balancer probes.
    This is an intentional design decision, not an oversight.
  expiry: "2025-06-15"
  tracking_ticket: "ARCH-1234"
  scope:
    - "api-gateway/routes/health.go"
```

## Rollout Strategy

New policies follow a graduated rollout to avoid blocking legitimate work:

| Phase | Duration | Behavior | Purpose |
|-------|----------|----------|---------|
| **Shadow** | 2–4 weeks | Log violations, don't block | Measure false-positive rate, identify legitimate exceptions |
| **Warning** | 2–4 weeks | Add PR comments, don't block | Surface violations to authors, train teams on new constraints |
| **Enforcing** | Ongoing | VETO violations, block merge | Full enforcement with exception process available |

**Graduation criteria** (shadow → warning):
- False-positive rate < 10%
- All known legitimate exceptions documented

**Graduation criteria** (warning → enforcing):
- False-positive rate < 2%
- Exception process tested and functional
- Team acknowledgment of the new policy

---

## Current Implementation Status

> **Automated policy enforcement is a future feature.** Currently, these principles guide agent behavior and human review.

The specialized validators in the plan validation loop apply these principles during their evaluation:

- **Architecture validator** checks ADR compliance, boundary correctness, and dependency direction
- **Security validator** checks authentication, input validation, data protection, and abuse prevention
- **Testing validator** checks coverage adequacy and regression protection
- **Performance validator** checks resource management and failure modes

These validators *embody* guardian policies as evaluation criteria, but they don't yet execute policies as discrete, machine-readable rules with formal VETO capability.

### Migration Path

1. **Current**: Policies are implicit in validator evaluation criteria and human review practice
2. **Next**: Define policies as structured YAML documents in the repository (e.g., `.agentic/policies/`)
3. **Future**: Policy evaluation engine that runs policies against plans and code changes, producing PASS/VETO verdicts that integrate with the validation gate
