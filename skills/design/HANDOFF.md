# Design Smart Zone Handoff

You are winding down Design because the session crossed the Smart Zone threshold.

Stop starting new design areas and flush the canonical design markdown with every decision and open trade-off captured so far. The next fresh Design iteration will receive that markdown seeded into its own `iteration-NN/` directory and continue from your handoff.

Write the rolling handoff scratch at `design-progress.md` in this iteration directory. Use exactly this structure:

```markdown
# Design Progress

## Decisions Made
- <decisions already captured in the canonical design markdown>

## Open Design Areas
- <areas still to design>

## Where I Stopped
<the precise next design area/question to continue from>

## Gotchas
<surprises, dead-ends, in-flight assumptions worth preserving>

## Ledger
```yaml
units:
  - id: data-model
    status: done
    decision: "chosen option + the load-bearing interface/contract/section (<=2 sentences)"
  - id: retry-policy
    status: pending
```

## Handoff State
CONTINUE
```

Rules:
- Preserve the persistent design markdown and update it in place before the handoff.
- Update the `## Ledger`: flip every decision you settled to `done` and fill its `decision` field (chosen option + load-bearing interface/contract/section, ≤2 sentences). Keep slug ids stable.
- Each ledger unit has only the `id`, `status`, and `decision` keys — add no other fields (no `topic`, `note`, `summary`, etc.).
- Use `CONTINUE` when any unit is still `pending`. Use `COMPLETE` only when every unit is `done` (the harness also auto-completes on zero pending).
- The next iteration receives your decision summaries, not the full design prose — keep them precise. Do not re-include resolved design areas verbatim.
- Touch `phase_complete` last.
