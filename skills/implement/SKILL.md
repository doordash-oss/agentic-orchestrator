---
description: Execute an approved phase plan with TDD, dependency-aware task fan-out, structured verification, and a strict iteration handoff
---

# Plan Execution

You have an approved phase plan. Execute its Tasks, satisfy its acceptance criteria and Success Criteria, run the testing contract, and emit the handoff artifacts the harness parses.

You are the coordinator for the whole phase. Keep main context focused on reading the plan, scheduling work, resolving cross-task conflicts, integrating worker results, and writing the final handoff. Delegate independent implementation, analysis, debugging, and verification to Task agents.

## Output Files

| Artifact | Path | Requirement | Purpose |
|----------|------|-------------|---------|
| `progress.md` | `{phase_dir}/progress.md` | required | structured progress markdown with iteration handoff, deferrals, verification summary, and iteration state |
| `verification-report.yaml` | `{iteration_dir}/verification-report.yaml` | required | verification report YAML recording required testing-contract results with evidence |
| `need-user-input.yaml` | `{iteration_dir}/need-user-input.yaml` | conditional: required when progress.md reports NEED_USER_INPUT | YAML gate file containing the structured user questions needed before the next iteration |

## Operating Rules

- The phase plan is the source of truth: `## Tasks`, task acceptance criteria, `## Success Criteria`, `### Automated Verification`, and `### Manual Verification`.
- For behavior changes, use TDD: write the failing automated test first, verify it fails for the expected reason, write the smallest production change that makes it pass, then refactor with tests green.
- Manual checks are not a substitute for automated tests when the behavior can reasonably be automated.
- If a bug appears while implementing, pin it with a failing test before fixing it.
- If a test is hard to write, treat that as design feedback: simplify the interface, introduce dependency injection, or narrow the behavior under test.
- Use stubs only when the approved plan explicitly calls for them. Mark intentional stubs with `// STUB(Phase N): <what the real implementation will do>`.
- Do not invent extra scope. New useful checks go in `verification-report.yaml` under `additional_checks:`, not in the contract `results:`.
- Add comments only when they explain intent, rationale, invariants, or non-obvious tradeoffs. Keep required API/doc comments. Do not add comments that merely restate self-explanatory code.

## Workspace

Your session runs with `cwd` at the feature state directory and `--add-dir` mounted on every repo in `Feature.Repos` plus the state dir.

For multi-repo plans, each Task has a `**Repo:** <name>` tag. That tag is the edit boundary for the worker handling the Task. Cross-repo verification items have `repo: cross-repo` in the testing contract and are run from the main session.

Before editing:

1. Read the full phase plan.
2. On resumed iterations (`iteration > 1`) or whenever `{phase_dir}/progress.md` exists, read `{phase_dir}/progress.md` before editing. For `RETRY`, resume from `### Where I stopped`. For review rejection, prioritize reviewer feedback and use progress as context. The prior handoff is discovered from `{phase_dir}/progress.md`; it is not re-injected into the user prompt.
3. Confirm every `**Repo:** <name>` tag maps to a mounted repo. If not, emit `NEED_USER_INPUT`.
4. Build a task dependency graph from `#### Blocked by`, shared files, repo tags, and acceptance criteria.

## Parallel Task Fan-Out

Default to parallel workers for independent Tasks. Sequentialize only when a Task depends on another Task's code, touches the same fragile files, or shares a migration/API contract that must be decided first.

Use this scheduling loop:

1. Identify ready Tasks: no unmet `Blocked by` dependencies and no write conflict with active workers.
2. Spawn one worker per ready non-trivial Task. Give each worker:
   - The exact Task text and acceptance criteria.
   - The repo and file/module ownership boundary.
   - The TDD requirement: RED, verify RED, GREEN, verify GREEN, REFACTOR.
   - The verification command or contract item relevant to that Task.
   - A reminder that other workers may be editing other Tasks and it must not revert their work.
3. Keep small changes in main only when that is genuinely faster than coordination.
4. When workers return, review their diffs, run or queue verification, then unlock dependent Tasks.
5. After implementation lands, run independent verification commands in parallel workers. If a check fails, spawn a focused debugging worker with the error output and expected behavior.

## TDD Loop

For each behavior-bearing Task:

1. **RED:** write one minimal automated test for one required behavior. Use a clear behavior name. Prefer testing real code; mock only when the dependency is external, slow, nondeterministic, or otherwise impractical.
2. **Verify RED:** run the narrow test and confirm it fails because the behavior is missing. If it passes immediately, the test is wrong or the behavior already exists; fix the test or adjust scope before writing production code. If it errors from setup or typos, fix the test until it fails for the expected reason.
3. **GREEN:** write the smallest production change that passes the test. Do not add unrelated behavior while green is still unproven.
4. **Verify GREEN:** run the narrow test, then the relevant surrounding suite. Fix production code when the test fails; do not weaken the test to make it pass.
5. **REFACTOR:** clean names, duplication, and structure only after tests are green. Re-run the relevant suite after refactoring.
6. Repeat with the next behavior or edge case until the Task acceptance criteria are met.

Before marking a Task complete, confirm:

- The new behavior has an automated test that failed first.
- The failure reason was expected.
- New tests are behavior-focused and not just mock assertions.
- Required error paths and regression cases are covered.
- The Task's verification passes cleanly.

## Verification Report

The system prompt names:

- `phase_dir`
- `iteration_dir`

Read `{iteration_dir}/verification-report.yaml` before final verification. When its `contract_path` field is non-empty, read that testing contract before running final verification. Each contract item has a stable `id`, a command or manual check, expected evidence, and policy. The pre-seeded `{iteration_dir}/verification-report.yaml` already has one `results:` row per item. When `contract_path` is empty or no testing contract exists, use the required verification items supplied in the prompt or plan.

For each contract row:

- Run the command or perform the manual check.
- Fill `status` with `passed`, `failed`, `blocked`, `not_run`, `waived`, or `pending_human`.
- Add non-empty evidence describing what happened.
- Do not rename item IDs or add rows under `results:`.
- Use `blocked` only for genuine environment blocks, with `blocked_reason`.
- Use `pending_human` only for manual items that require a named downstream owner or environment outside this session.

For visual and behavioral artifact rows, evidence is file-backed:

- Put visual artifacts under `screenshots/` relative to `{iteration_dir}`.
- Put behavioral artifacts under `behaviors/` relative to `{iteration_dir}`.
- For `passed`, `failed`, and `pending_human` visual or behavioral rows, write real files and set `evidence.primary` to the required artifact path. Use `evidence.attachments[]` for optional supporting files. Paths must be relative and stay under the matching directory.
- When a behavioral requirement asks for command output or a command transcript, preserve one transcript block per named command with the command line, exit code, and actual combined stdout/stderr. A hand-written outcome summary is not command evidence. On retries, carry the complete transcript forward and append or replace individual command blocks; never collapse it into summaries.
- `blocked`, `not_run`, and `waived` visual or behavioral rows do not require artifact files. A genuine `blocked` row still needs `blocked_reason`; do not use `blocked` to hide missing artifacts.
- Command and manual rows may keep using `evidence.summary` and `evidence.exit_code`; artifact rows may combine file fields with summary or exit code when useful.

Compact examples:

```yaml
- item_id: visual_abc123
  mode: visual
  status: passed
  evidence:
    summary: dashboard after import
    primary: screenshots/dashboard-import.png
    attachments:
      - screenshots/dashboard-empty-state.png

- item_id: behavioral_def456
  mode: behavioral
  status: failed
  evidence:
    summary: create flow still returns validation error
    exit_code: 1
    primary: behaviors/create-flow.log
```

## Handoff

At iteration end, write artifacts in this exact order:

1. `{iteration_dir}/verification-report.yaml`.
2. `{phase_dir}/progress.md`.
3. Run `"$AGENTICO_BIN" validate-artifacts --phase implement --role implementer --dir "{iteration_dir}"`; fix any reported issue and rerun before continuing.
4. `{iteration_dir}/need-user-input.yaml` only when `progress.md` reports `NEED_USER_INPUT`.
5. `{iteration_dir}/phase_complete`.

`progress.md` must use this schema:

````markdown
# Iteration Progress

## Iteration Handoff

### Completed this iteration
- <concrete files / functions / change units landed; one bullet per unit>

### Remaining from the plan
- <unfinished Tasks or Success Criteria; empty when complete>

### Where I stopped
<next task/file/check for a future iteration, or "Complete">

### Gotchas / blockers / in-flight decisions
- <anything not obvious from the plan, diff, or verification report>

## Deferrals

```yaml
deferrals: []
closed_deferrals: []
```

## Verification Report

- **Path**: <absolute path to {iteration_dir}/verification-report.yaml>
- **Summary**: <N passed, M failed, K blocked, L not_run>
- **Notes**: <optional one-line summary; omit the bullet when empty>

## Iteration State

SUCCESS
````

The `## Deferrals` fenced YAML block must always include both keys. Deferrals are only Agentic-owned work due in a numbered future roadmap phase (`description`, integer `due_by_phase`, `reason`, optional `id`/`repo_scope`); manual or external follow-ups are not deferrals. If the prompt lists deferrals due this phase, either close each completed ID under `closed_deferrals:` or re-defer by citing its existing `id:` with a new `due_by_phase` and reason.

`## Iteration State` must be exactly one of:

- `SUCCESS`: every Task and Success Criteria item is complete, required checks pass, and due deferrals are reconciled.
- `RETRY`: real progress landed, work remains, and `### Where I stopped` names a concrete next action that the next iteration can perform with the current scope and environment. Do not use `RETRY` when implementation is complete and only an external blocker, plan/contract change, scope expansion, waiver, permission, or human decision remains.
- `NEED_USER_INPUT`: the plan is wrong, repo state contradicts it, or a human decision is required.

For `NEED_USER_INPUT`, insert this section between `## Verification Report` and `## Iteration State`:

```markdown
## Questions for User

1. <specific decision needed>
2. <specific decision needed>
```

After `NEED_USER_INPUT`, include a short summary under the same `## Iteration State` section explaining why work cannot continue safely.
