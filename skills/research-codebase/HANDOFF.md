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

## Handoff State
CONTINUE
```

Rules:
- Preserve the canonical research markdown and update it in place before the handoff.
- Use `CONTINUE` when another fresh Research agent should resume.
- Use `COMPLETE` only when the research is fully done and the canonical markdown is ready for validation.
- Touch `phase_complete` last.
