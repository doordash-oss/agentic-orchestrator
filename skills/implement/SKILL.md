---
description: Execute an approved phase plan with TDD, dependency-aware task fan-out, the plan's verification and evidence, and a strict iteration handoff
license: Apache-2.0; Superpowers inspiration acknowledged in ATTRIBUTION.md
provenance: upstream-inspired
---

# Plan Execution

Implement the approved phase plan. You own code, development-time testing, the plan's verification and the evidence it asks for, and the progress handoff.

## Output Files

| Artifact | Path | Requirement | Purpose |
|----------|------|-------------|---------|
| `progress.md` | `{phase_dir}/progress.md` | required | structured progress markdown with iteration handoff, deferrals, and iteration state |

## Start Here

Never create or edit `phase_complete`.

1. Read the full phase plan.
2. If `{phase_dir}/progress.md` exists, read it and resume from `### Where I stopped`. Reviewer feedback and the current plan override stale handoff prose.
3. Confirm every Task `**Repo:** <name>` is mounted. If the plan or repo scope is contradictory, call the formal AskUserQuestion control and continue after the answer.
4. Build a dependency graph from `Blocked by`, shared files, repo tags, and acceptance criteria.

## Implementation Rules

- The approved Tasks, acceptance criteria, and Success Criteria define scope.
- For behavior changes, use a red-green-refactor loop: add a focused failing test, verify the expected failure, implement the smallest fix, run the focused test and relevant surrounding suite, then refactor while green.
- Pin discovered bugs with regression tests before fixing them when practical.
- Manual evidence does not replace an automated test for behavior that can reasonably be automated.
- Use stubs only when the plan explicitly requests them. Mark intentional stubs with `// STUB(Phase N): <purpose>`.
- If the plan or scope must change, call the formal AskUserQuestion control.
- Add comments only for intent, rationale, invariants, or non-obvious tradeoffs.

## Disputing a Review Finding

Later approved decisions outrank the plan's acceptance criteria: the roadmap's out-of-scope list, the user's recorded answers, and the phase's review decisions file. A reviewer finding that one of them supersedes, or that is wrong on the facts, is not fixed. Follow a decision the decisions file already records; for any other disputed blocking finding, call the formal AskUserQuestion control once per finding. Quote the finding, name its axis, and say why you dispute it, citing the decision when there is one. Offer exactly these options:

- "Fix it": you fix the finding as the reviewer asked.
- "Accept as is": the finding stands accepted and is not raised again.
- "Change the scope": the user describes the scope change in the answer, and you apply it.

Act on the answer. The harness records every question and answer in the decisions file that later reviewers and implementers read. When you worked on review findings, end your final response with a short note for the next reviewers: what you fixed, what you disputed, and why.

## Task Fan-Out

Parallelize independent non-trivial Tasks. Give each worker the exact Task and acceptance criteria, its repo/file boundary, dependencies, and the focused development test it should use. Sequentialize shared migrations, API decisions, and overlapping edits. Review returned diffs and integrate them before unlocking dependents.

Workers validate the code they change. Before reporting SUCCESS, run every command under the plan's `### Automated Verification` yourself, from the root of the repository its `[repo: <name>]` tag names, and record the commands and results in your handoff — SUCCESS asserts they pass.

## Plan Evidence

When the plan's Success Criteria ask for evidence, capture it under `{iteration_dir}`:

- Manual Verification: one concise Markdown record under `observations/` covering the consolidated checklist, the observed result, and enough concrete detail for review.
- Visual Evidence: the actual image for each item under `screenshots/`, at its declared size.
- Behavioral Evidence: one actual trace, recording, or interaction log per item under `behaviors/`, produced by the item's packaged command when it has one.

Never create placeholder evidence or report a result you did not observe. If evidence needs authorization, hardware, or an environment this session lacks, call the formal AskUserQuestion control, or record exactly what you could not capture and why under `### Gotchas / blockers / in-flight decisions`.

## Handoff

At iteration end:

1. Write the plan's evidence files.
2. Write `{phase_dir}/progress.md`.
3. Run `"$AGENTICO_BIN" validate-artifacts --phase implement --role implementer --dir "{iteration_dir}"` and fix every reported handoff defect.
4. Finish the root response with exactly one structured outcome tag from the system prompt. The harness writes `phase_complete` after validation.

Use this `progress.md` shape:

````markdown
# Iteration Progress

## Iteration Handoff

### Completed this iteration
- <concrete files, functions, or change units landed>

### Remaining from the plan
- <unfinished Tasks or Success Criteria; empty when complete>

### Where I stopped
<next concrete action, or "Complete">

### Gotchas / blockers / in-flight decisions
- <anything the next iteration cannot infer from the plan and diff>

## Deferrals

```yaml
deferrals: []
closed_deferrals: []
```

## Iteration State

SUCCESS
````

The Deferrals block must include both keys. Deferrals are work due in a numbered future roadmap phase; permissions, external systems, and human follow-ups are blockers, not deferrals.

Choose exactly one state:

- `SUCCESS`: implementation, acceptance criteria, development tests, the plan's automated verification, due deferrals, and the plan's evidence are complete.
- `RETRY`: useful implementation progress landed and a concrete in-scope next action is possible in the current environment.
Never emit `RETRY` for a blocker you cannot act on in this environment (missing credentials, absent hardware): the harness re-dispatches `RETRY` iterations unchanged. Call the formal AskUserQuestion control for a human decision and resume the same iteration after the answer.
