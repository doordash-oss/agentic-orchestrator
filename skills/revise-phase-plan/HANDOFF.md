# Phase Plan Revision Smart Zone Handoff

You are above the Smart Zone threshold. Wind this phase-plan revision attempt down so a fresh phase-plan reviser can continue inside the same attempt.

1. Stop starting new analysis. Finish any in-flight file write.
2. Flush the canonical phase plan artifact in the shared artifact root, normally `plan.md`.
3. Preserve prior approved or frozen sections exactly unless the revision instructions explicitly allow changing them.
4. Write or overwrite `planning-handoff.md` in the active `attempt-NN` directory using this exact schema:

```markdown
# Planning Handoff

## Understanding
- <key facts, validator feedback, sticky approvals, frozen sections, files, and constraints already digested>

## Plan Progress
### Drafted
- <phase plan sections, tasks, and success criteria already revised in the canonical plan>
### Remaining
- <phase plan sections, tasks, validator findings, and checks still to address>
### Where I stopped
- <the exact next step for the continuation>

## Ledger
```yaml
units:
  - id: <stable-slug>      # one plan section / decision per unit; never renumber
    status: done           # or "pending"
    decision: "chosen approach + load-bearing section/contract, <=2 sentences (required when done)"
  - id: <another-unit>
    status: pending
# Each unit has only the id, status, and decision keys — add no other fields.
```

## Handoff State
CONTINUE
```

5. Use `CONTINUE` when another agent must resume the revision. Use `COMPLETE` only if the canonical plan is ready for validation.
6. Touch `phase_complete` in the active attempt directory as the very last action, then end the turn.
