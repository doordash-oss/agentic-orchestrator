# Validation Scoring Model

How the multi-agent plan validation gate evaluates plans, and the reasoning behind the scoring design.

## Design Principle: Separation of Concerns

The scoring architecture separates two responsibilities:

1. **LLM extracts structured data**: Each specialized validator analyzes the plan and produces assessments per criterion (PASS/FAIL with justification).
2. **Deterministic code scores**: A scoring function converts structured assessments into numeric scores, applies weights, and compares against thresholds.

This separation matters because LLMs are good at nuanced evaluation but unreliable at consistent numeric scoring. By having the LLM produce qualitative judgments and deterministic code convert those to numbers, we get the best of both.

## 4 Validators with Weights

| Validator | Weight | Rationale |
|-----------|--------|-----------|
| **Architecture** | 30% | Structural integrity is the hardest property to retrofit. A plan that violates architectural boundaries creates compounding debt. |
| **Security** | 30% | Security flaws are the highest-impact failures. A single auth bypass or injection vulnerability can outweigh any amount of good architecture. |
| **Performance** | 20% | Performance issues are usually fixable post-merge without architectural restructuring. Important but less catastrophic than architecture or security failures. |
| **Testing** | 20% | Test strategy gaps are catchable during implementation. A weak test plan is a risk signal, not a blocking defect if other dimensions are strong. |

Architecture and Security are weighted equally at 30% because both represent hard-to-fix categories: architectural violations require restructuring, security flaws require incident response. Performance and Testing are at 20% because they're more amenable to iterative improvement.

## Scoring: Within vs. Across Validators

### Within a validator: Weakest-link (min)

Each validator evaluates multiple criteria. The validator's score is the **minimum** of its criteria scores, not the average.

**Why min, not average**: A plan that scores 95 on four criteria and 20 on one criterion has a serious gap. Averaging produces 80 — which looks acceptable. The min correctly reports 20 — which forces the gap to be addressed.

Within a single domain (e.g., security), weaknesses are **not** fungible. Excellent input validation does not compensate for missing authentication. The weakest criterion determines the validator's score because a chain is only as strong as its weakest link.

**Example**:
```
Security validator criteria:
  Authentication & Authorization: 90
  Input Validation: 85
  Data Protection: 30  ← hardcoded secrets found
  Rate Limiting: 80
  Security Dependencies: 85

Security score = min(90, 85, 30, 80, 85) = 30
```

### Across validators: Weighted sum

The overall plan score is a **weighted sum** of the 4 validator scores.

**Why weighted sum, not min**: Across *different* domains, trade-offs are acceptable. A plan can be excellent on security (95) and merely adequate on performance (75) and still be a good plan — the domains address different concerns. Taking the min across domains would be too conservative, blocking plans that are genuinely strong overall but have one domain slightly below the others.

**Example**:
```
Architecture: 85 × 0.30 = 25.5
Security:     90 × 0.30 = 27.0
Performance:  75 × 0.20 = 15.0
Testing:      80 × 0.20 = 16.0

Overall = 25.5 + 27.0 + 15.0 + 16.0 = 83.5
```

### Summary

| Aggregation | Method | Reasoning |
|-------------|--------|-----------|
| Within validator | min of criteria | Weaknesses within a domain are not fungible |
| Across validators | weighted sum | Trade-offs between domains are acceptable |

## Threshold

**Starting threshold**: 80/100.

This is a calibration starting point, not a permanent value. The threshold should be adjusted based on shadow mode observation data:

| Score Band | Expected Issue Rate | Action |
|------------|---------------------|--------|
| 75–79 | ~15% of plans have issues caught in implementation | Below threshold — plan revision required |
| 80–84 | ~3% of plans have issues caught in implementation | Acceptable — proceed to implementation |
| 85+ | <1% of plans have issues caught in implementation | High confidence — consider for auto-merge pipeline (future) |

**Calibration process**:
1. Run validators in shadow mode (log scores, don't block)
2. Track which plans required rework during implementation
3. Plot score vs. rework rate
4. Adjust threshold to the score where rework rate drops below acceptable level (target: <5%)

## Hard Vetoes

Hard vetoes override any score. A plan that triggers a hard veto is rejected regardless of how well it scores on other dimensions.

| Veto | Category | Rationale |
|------|----------|-----------|
| SQL injection vector | Security | Exploitable vulnerability, zero tolerance |
| Authentication bypass | Security | Access control is a binary property — it works or it doesn't |
| Hardcoded secrets | Security | Secrets in source control are irrecoverable |
| PII in logs | Security / Compliance | Regulatory violation (GDPR, CCPA) |
| Missing auth on endpoint | Security | Every endpoint must have explicit auth; "add later" is not acceptable |
| Schema migration without rollback | Architecture | Irreversible data changes require rollback planning |

Hard vetoes are implemented as guardian policies with `severity: VETO`. See [guardian-policies.md](guardian-policies.md).

## Current Implementation

> **The current agentic implementation uses `APPROVED` / `CHANGES_REQUESTED` per validator. Numeric scoring is a future enhancement.**

Today, each specialized validator (architecture, security, performance, testing) evaluates the plan and returns a binary verdict via the `## Verdict` section of its `validation-<axis>-feedback.md` handoff file:
- `APPROVED` — the plan passes this validator's criteria
- `CHANGES_REQUESTED` — the plan has issues that must be addressed

The plan validation gate aggregates these verdicts. If any validator requests changes, the plan is revised and re-validated up to `MaxPlanIterations` times.

**The principle — specialized domain review catches issues that generalist review misses — is the value, not the exact scoring numbers.** The numeric model described above is the target architecture for when the system has enough shadow mode data to calibrate thresholds meaningfully.

### Migration Path

1. **Current**: Binary verdicts per validator, any rejection triggers revision
2. **Next**: Validators produce structured criteria assessments (PASS/FAIL per criterion) — already partially in place via the evaluation criteria sections in each validator template
3. **Future**: Deterministic scoring layer converts structured assessments to numeric scores with configurable weights and thresholds
