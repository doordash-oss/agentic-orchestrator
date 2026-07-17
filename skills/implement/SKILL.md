---
description: Execute an approved phase plan with TDD, dependency-aware task fan-out, required evidence, and a strict iteration handoff
license: Apache-2.0; Superpowers inspiration acknowledged in ATTRIBUTION.md
provenance: upstream-inspired
---

# Plan Execution

Implement the approved phase plan. You own code, development-time testing, explicitly agent-owned evidence, and the progress handoff. The verification harness owns final contract execution and `verification-report.yaml`.

## Output Files

| Artifact | Path | Requirement | Purpose |
|----------|------|-------------|---------|
| `progress.md` | `{phase_dir}/progress.md` | required | structured progress markdown with iteration handoff, deferrals, and iteration state |
| `need-user-input.yaml` | `{iteration_dir}/need-user-input.yaml` | conditional: required when progress.md reports NEED_USER_INPUT | YAML gate file containing the structured user questions needed before the next iteration |

## Start Here

Never create or edit `verification-report.yaml`. When a testing contract exists, the harness derives it after `phase_complete` from the testing contract, command results, waivers, and agent-owned evidence files.

1. Read the full phase plan.
2. If `{phase_dir}/progress.md` exists, read it and resume from `### Where I stopped`. Reviewer feedback and the current plan override stale handoff prose.
3. Read `testing-contract.yaml` if it exists — at `{phase_dir}/../testing-contract.yaml` for roadmap phases, or `{phase_dir}/testing-contract.yaml` for cycle layouts (rebase/refactor/review-comments, where `{phase_dir}` is the cycle root). Note only items with `owner: agent`; final execution of `owner: harness` items is not your responsibility. If the file does not exist, this feature has no per-iteration machine verification: run every command under the plan's `### Automated Verification` yourself each iteration and record the commands and results in your handoff — SUCCESS asserts they pass.
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

Workers validate the code they change. They do not author final verification results; when a testing contract exists, the harness runs it after handoff. Otherwise, run the plan's Automated Verification before reporting SUCCESS.

## Agent-Owned Evidence

For every testing-contract item with `owner: agent`, write the file named by `expected_evidence.path` under `{iteration_dir}`.

- `manual_observation`: one concise Markdown record covering the contract's consolidated semantic checklist, the observed result, and enough concrete detail for review.
- `visual_artifact`: the actual image at the specified `screenshots/` path.
- `behavioral_artifact`: one actual trace, recording, or interaction log covering the contract's consolidated primary-journey checklist at the specified `behaviors/` path.

Do not write evidence for `owner: harness` items. Do not invent statuses, counts, transcripts, or waiver claims. If required evidence needs unavailable authorization, hardware, a human judgment, or an external environment, emit `NEED_USER_INPUT` and name the missing capability. Never create placeholder evidence.

## Handoff

At iteration end:

1. Write all required agent-owned evidence files.
2. When a testing contract exists, run `"$AGENTICO_BIN" verify-evidence --contract "{testing_contract_path}" --dir "{iteration_dir}"`; fix every reported gap (missing, malformed, mis-sized, or byte-identical duplicate captures) before continuing. This runs the same file-backed checks the post-handoff integrity gate applies — clearing it here avoids losing a whole iteration to a gap you can fix now.
3. Write `{phase_dir}/progress.md`.
4. Write `{iteration_dir}/need-user-input.yaml` only for `NEED_USER_INPUT`.
5. Run `"$AGENTICO_BIN" validate-artifacts --phase implement --role implementer --dir "{iteration_dir}"`; fix reported handoff defects.
6. Write `{iteration_dir}/phase_complete` last.

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

- `SUCCESS`: implementation, acceptance criteria, development tests, due deferrals, and all `owner: agent` evidence are complete. When a testing contract exists, the harness performs final contract verification next; otherwise your reported automated-verification runs are the record.
- `RETRY`: useful implementation progress landed and a concrete in-scope next action is possible in the current environment.
- `NEED_USER_INPUT`: progress requires a human decision, authorization, unavailable external capability, scope/contract change, or resolution of contradictory repository state.

Never emit `RETRY` for a blocker you cannot act on in this environment (missing credentials, absent hardware, a human decision): the harness re-dispatches `RETRY` iterations unchanged, so an externally blocked `RETRY` only burns the iteration budget. Emit `NEED_USER_INPUT` and name the blocker instead.

For `NEED_USER_INPUT`, insert a numbered `## Questions for User` section between `## Deferrals` and `## Iteration State`, then put a concise blocker summary below the `NEED_USER_INPUT` token. Ask only questions whose answers are required to resume safely.
