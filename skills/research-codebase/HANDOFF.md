# Research Smart Zone Handoff

You are winding down Research because the session crossed the Smart Zone threshold.

This is a DRAIN-AND-BANK wind-down, NOT an abort. The threshold is a soft nudge delivered as a user message; nothing force-terminates your turn — YOU decide when to end it. Banking the findings already in flight is the priority. Do the following in order:

1. STOP spawning NEW sub-agents and stop starting new research areas.
2. WAIT for any sub-agents you already launched to return — do NOT close, cancel, or abandon them. Their in-flight findings are the progress this iteration exists to bank. It is FINE to briefly exceed the Smart Zone threshold to let them finish.
3. INCORPORATE those returned findings: write each answered question's `questions/Q-NNN.md`, add/refresh its entry in the `research.md` index, and flip those `Q-NNN` units to `done` in the `## Ledger`.
4. ONLY THEN write `research-progress.md` and touch `phase_complete` (last).

An iteration that closes its sub-agents and banks ZERO findings is a FAILURE that stalls the loop — it trips the "no progress for N continuations" safety rail. Never do this. The next fresh Research iteration will receive the updated markdown seeded into its own `iteration-NN/` directory and continue from your handoff.

Write the rolling handoff scratch at `research-progress.md` in this iteration directory. Use exactly this structure:

```markdown
# Research Progress

## Completed Findings
- <questions already written to questions/Q-NNN.md and linked from research.md>

## Remaining Areas
- <areas/questions still to research>

## Where I Stopped
<the precise next area/question/file to continue from>

## Gotchas
<surprises, dead-ends, in-flight hypotheses worth preserving>

## Ledger
```yaml
units:
  - id: Q-001
    status: done
  - id: Q-002
    status: pending
```

## Handoff State
CONTINUE
```

Rules:
- Preserve the persistent question files and the `research.md` index; write/refresh them in place before the handoff.
- Update the `## Ledger`: flip every question you answered this session to `done`; leave the rest `pending`. Keep `Q-NNN` ids stable — never renumber.
- Each ledger unit has only the `id` and `status` keys — add no other fields (no `topic`, `note`, `summary`, etc.).
- Use `CONTINUE` when any unit is still `pending`. Use `COMPLETE` only when every unit is `done` (the harness also auto-completes on zero pending). COMPLETE with a pending unit is rejected.
- Do not re-include answered question content; each lives in its own `questions/Q-NNN.md` linked from the index. The next iteration receives only the pending ids and a path pointer.
- Touch `phase_complete` last.
