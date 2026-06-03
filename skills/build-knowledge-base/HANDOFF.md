# Knowledge Base Smart Zone Handoff

You are winding down a per-repo Knowledge Base build because the session crossed the Smart Zone threshold.

Stop starting new categories, leaves, and sub-agents. Flush the persistent KB tree with every document completed so far, and ensure the top-level `index.md` reflects the current category map. The next fresh Knowledge Base iteration will continue the same repo's KB in place from this handoff; it must not restart completed categories and must not re-run the full-vs-incremental decision.

Write the rolling handoff scratch at `kb-progress.md` in this iteration directory. Use exactly this structure:

```markdown
# Knowledge Base Progress

## Completed Categories
- <category>: <leaf files written / "index.md only"> - already in the persistent KB tree

## Remaining Categories
- <category>: <leaves still to write>

## Where I Stopped
<the precise next category / leaf / area to continue from>

## Gotchas
<surprises, dead-ends, in-flight findings worth preserving>

## Ledger
```yaml
units:
  - id: architecture
    status: done
  - id: conventions
    status: pending
  - id: api-surface
    status: pending
  - id: dependencies
    status: pending
  - id: verification
    status: pending
```

## Handoff State
CONTINUE
```

Rules:
- Update the persistent KB tree before writing the handoff, including a current top-level `index.md`.
- Maintain the `## Ledger` over the five fixed category ids (`architecture`, `conventions`, `api-surface`, `dependencies`, `verification`): mark each finished category `done`, leave the rest `pending`. Do not add or rename units.
- Each ledger unit has only the `id` and `status` keys — add no other fields (no `topic`, `note`, `summary`, etc.).
- Use `CONTINUE` when any category is still `pending`. Use `COMPLETE` only when all five are `done` (the harness also auto-completes on zero pending).
- Touch `phase_complete` in the iteration directory last.
