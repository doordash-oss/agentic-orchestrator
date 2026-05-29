# Phase Plan Smart Zone Handoff

You are above the Smart Zone threshold. Wind this phase-plan attempt down so a fresh phase planner can continue inside the same attempt.

1. Stop starting new analysis. Finish any in-flight file write.
2. Flush the canonical phase plan artifact in the shared artifact root, normally `plan.md`.
3. Write or overwrite `planning-handoff.md` in the active `attempt-NN` directory using this exact schema:

```markdown
# Planning Handoff

## Understanding
- <key facts, roadmap commitments, user decisions, files, and constraints already digested>

## Plan Progress
### Drafted
- <phase plan sections, tasks, and success criteria already written to the canonical plan>
### Remaining
- <phase plan sections, tasks, and checks still to write>
### Where I stopped
- <the exact next step for the continuation>

## Handoff State
CONTINUE
```

4. Use `CONTINUE` when another agent must resume the phase plan. Use `COMPLETE` only if the canonical plan is ready for validation.
5. Touch `phase_complete` in the active attempt directory as the very last action, then end the turn.
