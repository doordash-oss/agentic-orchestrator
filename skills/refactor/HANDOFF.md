# Refactor Plan Smart Zone Handoff

You are above the Smart Zone threshold. Wind this refactor-plan step down so a fresh refactor planner can continue inside the same step.

1. Stop starting new analysis. Finish any in-flight file write.
2. Flush the canonical refactor plan artifact in the refactor cycle directory, normally `refactor-plan.md`.
3. Write or overwrite `planning-handoff.md` in the refactor cycle directory using this exact schema:

```markdown
# Planning Handoff

## Understanding
- <key facts, repo boundaries, requested refactor goals, files, and constraints already digested>

## Plan Progress
### Drafted
- <refactor plan sections, tasks, and verification already written to the canonical plan>
### Remaining
- <refactor plan sections, tasks, and checks still to write>
### Where I stopped
- <the exact next step for the continuation>

## Handoff State
CONTINUE
```

4. Use `CONTINUE` when another agent must resume the refactor plan. Use `COMPLETE` only if the canonical refactor plan is ready for validation.
5. Touch `phase_complete` in the refactor cycle directory as the very last action, then end the turn.
