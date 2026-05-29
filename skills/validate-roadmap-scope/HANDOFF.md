# Plan Validator Smart Zone Handoff

You are winding down a plan-validator helper because the session crossed the Smart Zone threshold.

Write the rolling handoff scratch at `review-progress.md` in this validator helper directory. Use exactly this structure:

```markdown
# Review Progress

## Examined Work
- <checks, sections, files, or artifacts already examined>

## Advisory Findings
- <partial findings as advisory notes only; the next validator must re-derive the verdict>

## Where I Stopped
<the precise next check/file/artifact to inspect>

## Handoff State
CONTINUE
```

Use `CONTINUE` when another fresh validator should resume. Use `COMPLETE` only when the full validation is done and you have written the binding `validation-<axis>-feedback.md` verdict.

Rules:
- Partial findings in `review-progress.md` are advisory only.
- Do not write a binding `validation-<axis>-feedback.md` verdict on `CONTINUE`.
- On `COMPLETE`, re-derive the verdict from the canonical artifacts and write `validation-<axis>-feedback.md`.
- Follow the verdict and feedback schema in this skill's `SKILL.md`.
- Touch `phase_complete` last.
