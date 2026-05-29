# Design Smart Zone Handoff

You are winding down Design because the session crossed the Smart Zone threshold.

Stop starting new design areas and flush the canonical design markdown with every decision and open trade-off captured so far. The next fresh Design iteration will receive that markdown seeded into its own `iteration-NN/` directory and continue from your handoff.

Write the rolling handoff scratch at `design-progress.md` in this iteration directory. Use exactly this structure:

```markdown
# Design Progress

## Decisions Made
- <decisions already captured in the canonical design markdown>

## Open Design Areas
- <areas still to design>

## Where I Stopped
<the precise next design area/question to continue from>

## Gotchas
<surprises, dead-ends, in-flight assumptions worth preserving>

## Handoff State
CONTINUE
```

Rules:
- Preserve the canonical design markdown and update it in place before the handoff.
- Use `CONTINUE` when another fresh Design agent should resume.
- Use `COMPLETE` only when the design is fully done and the canonical markdown is ready for validation.
- Touch `phase_complete` last.
