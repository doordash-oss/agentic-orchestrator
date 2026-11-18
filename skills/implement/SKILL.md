---
description: Execute an approved phase plan with TDD, dependency-aware task fan-out, the plan's verification, and short handoff notes
license: Apache-2.0; Superpowers inspiration acknowledged in ATTRIBUTION.md
provenance: upstream-inspired
---

# Plan Execution

Implement the approved phase plan. You own code, development-time testing, the plan's verification, and the handoff notes.

## Output Files

| Artifact | Path | Requirement | Purpose |
|----------|------|-------------|---------|
| `progress.md` | `{phase_dir}/progress.md` | expected | short handoff notes for the next session |

## Start Here

Never create or edit `phase_complete`.

1. Read the full phase plan.
2. If `{phase_dir}/progress.md` exists, read it and resume from where it left off. Reviewer feedback and the current plan override stale notes.
3. Confirm every Task `**Repo:** <name>` is mounted. If the plan or repo scope is contradictory, call the formal AskUserQuestion control and continue after the answer.
4. Build a dependency graph from `Blocked by`, shared files, repo tags, and acceptance criteria.

## Implementation Rules

- The approved Tasks, acceptance criteria, and Verification section define scope.
- For behavior changes, use a red-green-refactor loop: add a focused failing test, verify the expected failure, implement the smallest fix, run the focused test and relevant surrounding suite, then refactor while green.
- Pin discovered bugs with regression tests before fixing them when practical.
- Manual evidence does not replace an automated test for behavior that can reasonably be automated.
- Use stubs only when the plan explicitly requests them. Mark intentional stubs with `// STUB(Phase N): <purpose>`.
- If the plan or scope must change, call the formal AskUserQuestion control.
- Add comments only for intent, rationale, invariants, or non-obvious tradeoffs.

## Disputing a Review Finding

Later approved decisions outrank the plan's acceptance criteria: the roadmap's out-of-scope list, the user's recorded answers, and the phase's review decisions file. A reviewer finding that one of them supersedes, or that is wrong on the facts, is not fixed. Follow a decision the decisions file already records; for any other disputed blocking finding, call the formal AskUserQuestion control once per finding. Start the question with "Dispute:", quote the finding, name its axis, and say why you dispute it, citing the decision when there is one. Offer exactly these options:

- "Fix it": you fix the finding as the reviewer asked.
- "Accept as is": the finding stands accepted and is not raised again.
- "Change the scope": the user describes the scope change in the answer, and you apply it.

Act on the answer. The harness records dispute and handoff answers in the decisions file that later reviewers and implementers read; other questions are not decisions. When you worked on review findings, end your final response with a short note for the next reviewers: what you fixed, what you disputed, and why.

When your prompt has a Handoff to the User section, the phase review stopped converging. Ask that section's question first, verbatim and as a single choice, ask no dispute questions this iteration, and act on the answer as the section says.

## Task Fan-Out

Parallelize independent non-trivial Tasks. Give each worker the exact Task and acceptance criteria, its repo/file boundary, dependencies, and the focused development test it should use. Sequentialize shared migrations, API decisions, and overlapping edits. Review returned diffs and integrate them before unlocking dependents.

Workers validate the code they change. Before reporting success, follow the plan's `## Verification` section yourself: run its commands from the root of the repository each belongs to, and look at what it asks you to look at. Success asserts that the verification passes. An older plan without `## Verification` keeps its checks under `## Success Criteria`; treat that section as the verification. Save any screenshots, logs, or recordings you produce under `{iteration_dir}`.

Never report a result you did not observe. If verification needs authorization, hardware, or an environment this session lacks, call the formal AskUserQuestion control, or say in your notes exactly what you could not check and why.

## Handoff

At iteration end, write `{phase_dir}/progress.md`: short notes a fresh session can pick up from. Say what was done, what is left, and anything surprising, including the verification you ran and its results. There is no template; replace stale notes rather than appending to them.

Then finish the root response with exactly one structured outcome from the system prompt. The harness writes `phase_complete`.

- `success`: implementation, acceptance criteria, development tests, and the plan's verification are complete.
- `retry`: useful implementation progress landed and a concrete in-scope next action is possible in the current environment.

Never ask for `retry` because of a blocker you cannot act on in this environment (missing credentials, absent hardware): the harness re-dispatches retry iterations unchanged. Call the formal AskUserQuestion control for a human decision and resume the same iteration after the answer.
