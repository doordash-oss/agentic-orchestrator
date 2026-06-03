# Roadmap Smart Zone Handoff

You are above the Smart Zone threshold. Wind this roadmap planning attempt down so a fresh roadmap creator can continue inside the same attempt.

1. Stop starting new analysis. Finish any in-flight file write.
2. Flush the canonical roadmap artifact in the shared artifact root, normally `roadmap.md`.
3. Write or overwrite `planning-handoff.md` in the active `attempt-NN` directory using this exact schema:

```markdown
# Planning Handoff

## Understanding
- <key facts, decisions, inputs, files, and constraints already digested>

## Plan Progress
### Drafted
- <roadmap sections or phases already written to the canonical roadmap>
### Remaining
- <roadmap sections or phases still to write>
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

4. Use `CONTINUE` when another agent must resume the roadmap. Use `COMPLETE` only if the canonical roadmap is ready for validation.
5. Touch `phase_complete` in the active attempt directory as the very last action, then end the turn.
