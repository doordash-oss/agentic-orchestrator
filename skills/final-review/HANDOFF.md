# Final Review Smart Zone Handoff

You are winding down a final-review helper because the session crossed the Smart Zone threshold.

Before writing the handoff, flush `verification-report.yaml` in place with durable evidence for checks already run or attested. The next reviewer should rely on that report for completed evidence rows and run only the remaining checks, while still re-deriving the final verdict from a clean read.

Write the rolling handoff scratch at `review-progress.md` in this iteration directory. Use exactly this structure:

```markdown
# Review Progress

## Examined Work
- <checks, sections, files, or artifacts already examined>

## Advisory Findings
- <partial findings as advisory notes only; the next reviewer must re-derive the verdict>

## Where I Stopped
<the precise next check/file/artifact to inspect>

## Handoff State
CONTINUE
```

Use `CONTINUE` when another fresh reviewer should resume. Use `COMPLETE` only when the full review is done and you have written the binding `review-feedback.md` verdict.

Rules:
- Partial findings in `review-progress.md` are advisory only.
- Do not write a binding `review-feedback.md` verdict on `CONTINUE`.
- Keep accumulated `verification-report.yaml` evidence in place.
- On `COMPLETE`, re-derive the verdict and write `review-feedback.md`.
- Touch `phase_complete` last.
