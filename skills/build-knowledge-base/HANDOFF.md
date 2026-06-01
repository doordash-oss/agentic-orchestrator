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

## Handoff State
CONTINUE
```

Rules:
- Update the persistent KB tree before writing the handoff, including a current top-level `index.md`.
- Use `CONTINUE` when another fresh Knowledge Base agent should resume.
- Use `COMPLETE` only when the KB is fully done and the persistent `index.md` is ready for validation.
- Touch `phase_complete` in the iteration directory last.
