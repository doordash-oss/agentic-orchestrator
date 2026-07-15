---
description: Execute an approved phase plan with TDD, dependency-aware task fan-out, semantic evidence, and a strict iteration handoff
---

# Plan Execution

Implement the approved phase plan. You own code, development-time testing, any explicitly agent-owned semantic evidence, and the progress handoff. Agentico owns final contract execution and `verification-report.yaml` after you signal completion.

## Output Files

| Artifact | Path | Requirement | Purpose |
|----------|------|-------------|---------|
| `progress.md` | `{phase_dir}/progress.md` | required | structured progress markdown with iteration handoff, deferrals, and iteration state |
| `need-user-input.yaml` | `{iteration_dir}/need-user-input.yaml` | conditional: required when progress.md reports NEED_USER_INPUT | YAML gate file containing the structured user questions needed before the next iteration |

## Start Here

Never create or edit `verification-report.yaml`. The harness derives it after `phase_complete` from the testing contract, command results, waivers, and agent-owned evidence files.

1. Read the full phase plan.
2. If `{phase_dir}/progress.md` exists, read it and resume from `### Where I stopped`. Reviewer feedback and the current plan override stale handoff prose.
3. Read `{phase_dir}/../testing-contract.yaml`. Note only items with `owner: agent`; final execution of `owner: harness` items is not your responsibility.
4. Confirm every Task `**Repo:** <name>` is mounted. If the plan or repo scope is contradictory, emit `NEED_USER_INPUT`.
5. Build a dependency graph from `Blocked by`, shared files, repo tags, and acceptance criteria.

## Implementation Rules

- The approved Tasks, acceptance criteria, and Success Criteria define scope.
- For behavior changes, use a red-green-refactor loop: add a focused failing test, verify the expected failure, implement the smallest fix, run the focused test and relevant surrounding suite, then refactor while green.
- Pin discovered bugs with regression tests before fixing them when practical.
- Manual evidence does not replace an automated test for behavior that can reasonably be automated.
- Use stubs only when the plan explicitly requests them. Mark intentional stubs with `// STUB(Phase N): <purpose>`.
- Do not invent bookkeeping rows or edit the testing contract. If the contract or scope must change, request user input.
- Add comments only for intent, rationale, invariants, or non-obvious tradeoffs.

## Task Fan-Out

Parallelize independent non-trivial Tasks. Give each worker the exact Task and acceptance criteria, its repo/file boundary, dependencies, and the focused development test it should use. Sequentialize shared migrations, API decisions, and overlapping edits. Review returned diffs and integrate them before unlocking dependents.

Workers validate the code they change. They do not author final verification results; Agentico runs the contract after handoff.

## Agent-Owned Evidence

For every testing-contract item with `owner: agent`, write exactly the relative file named by `expected_evidence.path` under `{iteration_dir}`:

- `manual_observation`: a concise Markdown record of what was inspected, the observed result, and enough concrete detail for review.
- `visual_artifact`: the actual image at the specified `screenshots/` path.
- `behavioral_artifact`: the actual trace, log, or other artifact at the specified `behaviors/` path.

Do not write evidence for `owner: harness` items. Do not invent statuses, counts, transcripts, or waiver claims. If required semantic evidence needs unavailable authorization, hardware, a human judgment, or an external environment, emit `NEED_USER_INPUT` and name the missing capability. Never create placeholder evidence.

## Handoff

At iteration end:

1. Write all required agent-owned evidence files.
2. Write `{phase_dir}/progress.md`.
3. Write `{iteration_dir}/need-user-input.yaml` only for `NEED_USER_INPUT`.
4. Run `"$AGENTICO_BIN" validate-artifacts --phase implement --role implementer --dir "{iteration_dir}"`; fix reported handoff defects.
5. Write `{iteration_dir}/phase_complete` last.

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

The Deferrals block must include both keys. Deferrals are Agentico-owned work due in a numbered future roadmap phase; permissions, external systems, and human follow-ups are blockers, not deferrals.

Choose exactly one state:

- `SUCCESS`: implementation, acceptance criteria, development tests, due deferrals, and all `owner: agent` evidence are complete. Agentico performs final contract verification next.
- `RETRY`: useful implementation progress landed and a concrete in-scope next action is possible in the current environment.
- `NEED_USER_INPUT`: progress requires a human decision, authorization, unavailable external capability, scope/contract change, or resolution of contradictory repository state.

For `NEED_USER_INPUT`, insert a numbered `## Questions for User` section between `## Deferrals` and `## Iteration State`, then put a concise blocker summary below the `NEED_USER_INPUT` token. Ask only questions whose answers are required to resume safely.
