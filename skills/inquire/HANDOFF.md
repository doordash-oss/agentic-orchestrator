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

## Ledger
```yaml
units:
  - id: C-001
    status: done
  - id: C-002
    status: pending
```

## Handoff State
CONTINUE
```

Rules:
- Preserve the persistent inquiry markdown and update it in place before the handoff.
- Update the `## Ledger`: flip every clarification you resolved to `done`; leave the rest `pending`. Keep `C-NNN` ids stable; append newly-discovered clarifications with fresh ids.
- Each ledger unit has only the `id` and `status` keys — add no other fields (no `topic`, `note`, `summary`, etc.).
- Use `CONTINUE` when any unit is still `pending`. Use `COMPLETE` only when every unit is `done` (the harness also auto-completes on zero pending).
- Net progress: pending must strictly decrease — do not add and resolve in equal measure and call it progress.
- Touch `phase_complete` last.
