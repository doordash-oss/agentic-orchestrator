# Research Smart Zone Handoff

You are winding down Research because the session crossed the Smart Zone threshold.

Stop starting new research, stop spawning new sub-agents, and flush the canonical research markdown with every finding gathered so far. The next fresh Research iteration will receive that markdown seeded into its own `iteration-NN/` directory and continue from your handoff.

Write the rolling handoff scratch at `research-progress.md` in this iteration directory. Use exactly this structure:

```markdown
# Research Progress

## Completed Findings
- <areas/questions already documented in the canonical research markdown>

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
- Preserve the persistent research markdown and update it in place before the handoff.
- Update the `## Ledger`: flip every question you answered this session to `done`; leave the rest `pending`. Keep `Q-NNN` ids stable — never renumber.
- Use `CONTINUE` when any unit is still `pending`. Use `COMPLETE` only when every unit is `done` (the harness also auto-completes on zero pending). COMPLETE with a pending unit is rejected.
- Do not re-include answered `## Q-NNN` sections; the next iteration receives only the pending ids and a path pointer.
- Touch `phase_complete` last.
