# Final Fix Smart Zone Handoff

You are winding down a final-fix helper because the session crossed the Smart Zone threshold.

Before writing the handoff, flush `verification-report.yaml` in place with the current fix evidence. Worktree edits and the report persist across continuations.

Write the rolling handoff scratch at `producer-progress.md` in this iteration directory. Use exactly this structure:

```markdown
# Producer Progress

## Completed Fix Work
- <fixes already completed>

## Remaining Fix Work
- <fixes, tests, or report updates still remaining>

## Where I Stopped
<the precise next file/check/action to continue>

## Handoff State
CONTINUE
```

Use `CONTINUE` when another fresh final-fix agent should resume. Use `COMPLETE` only when the fix is done and `verification-report.yaml` reflects the completed verification state.

Rules:
- Preserve existing worktree edits.
- Preserve and update `verification-report.yaml` in place.
- On `COMPLETE`, leave the canonical fix output ready for validation.
- Touch `phase_complete` last.
