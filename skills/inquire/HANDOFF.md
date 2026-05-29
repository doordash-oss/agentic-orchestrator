# Inquire Smart Zone Handoff

You are winding down Inquire because the session crossed the Smart Zone threshold.

Stop asking new questions and flush the canonical inquiry markdown with every requirement question gathered so far. The next fresh Inquire iteration will receive that markdown seeded into its own `iteration-NN/` directory and continue from your handoff.

Write the rolling handoff scratch at `inquire-progress.md` in this iteration directory. Use exactly this structure:

```markdown
# Inquire Progress

## Clarified Requirements
- <requirements already captured in the canonical inquiry markdown>

## Open Questions
- <requirements still unclear or still needing question generation>

## Where I Stopped
<the precise next requirement/question to continue from>

## Gotchas
<surprises, dead-ends, in-flight assumptions worth preserving>

## Handoff State
CONTINUE
```

Rules:
- Preserve the canonical inquiry markdown and update it in place before the handoff.
- Use `CONTINUE` when another fresh Inquire agent should resume.
- Use `COMPLETE` only when the inquiry is fully done and the canonical markdown is ready for validation.
- Touch `phase_complete` last.
