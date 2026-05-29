# Roadmap Revision Smart Zone Handoff

You are above the Smart Zone threshold. Wind this roadmap revision attempt down so a fresh roadmap reviser can continue inside the same attempt.

1. Stop starting new analysis. Finish any in-flight file write.
2. Flush the canonical roadmap artifact in the shared artifact root, normally `roadmap.md`.
3. Preserve prior approved or frozen sections exactly unless the revision instructions explicitly allow changing them.
4. Write or overwrite `planning-handoff.md` in the active `attempt-NN` directory using this exact schema:

```markdown
# Planning Handoff

## Understanding
- <key facts, validator feedback, sticky approvals, frozen sections, files, and constraints already digested>

## Plan Progress
### Drafted
- <roadmap sections or phases already revised in the canonical roadmap>
### Remaining
- <roadmap sections or validator findings still to address>
### Where I stopped
- <the exact next step for the continuation>

## Handoff State
CONTINUE
```

5. Use `CONTINUE` when another agent must resume the revision. Use `COMPLETE` only if the canonical roadmap is ready for validation.
6. Touch `phase_complete` in the active attempt directory as the very last action, then end the turn.
