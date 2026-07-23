---
description: Execute a feature-level refactor cycle — author a refactor plan with per-Task **Repo:** tags, then implement the plan across every Feature.Repos worktree, supporting cross-repo edits in a single iteration
license: Apache-2.0
provenance: agentic-orchestrator-original
---

# Refactor Cycle

The harness has dispatched a refactor cycle on a published feature. Your job is to author a refactor plan that scopes the work via per-Task `**Repo:** <name>` tags, then implement the plan across every staged repo. The same agent session coordinates related per-repo Tasks across the workspace.

## Output Files

| Artifact | Path | Requirement | Purpose |
|----------|------|-------------|---------|
| `refactor-plan.md` | `{phase_dir}/refactor-plan.md` | required | refactor plan markdown with phase-plan-style tasks and per-repo tags |

## Workspace

- `cwd` is the feature state dir.
- `--add-dir` mounts **every** `Feature.Repos` worktree, regardless of which repos the plan stages. Refactors are cross-repo by design — the agent needs read access to all repos to judge a refactor's impact, even when the edits land in a subset.
- The refactor-plan markdown lives at `<artifactDir>/refactor-plan.md`.
- The testing contract contains only plan-declared commands and semantic evidence requirements. Agentico runs harness-owned commands after handoff.

## Per-repo dispatch

Per-repo work is delegated **by prompt**, not by `cwd`. Each Task and Task sub-agent names exactly one repo and constrains file edits to that repo's worktree. Split a logical cross-repo change into related per-repo Tasks and sequence them through `#### Blocked by`; the main agent owns their coordination.

- **Stays in main:** plan reading, deciding which repos to stage, sequencing cross-repo edits, and emitting the handoff.
- **Delegated to per-repo Task agents:** the actual file edits, focused development tests, and (when publishable) `git commit` + `git push --force-with-lease`.

## The Process

### Step 1 — Author the refactor plan (refactor-plan step)

This step runs once per cycle. Read the user's refactor request, the feature description, and the workspace's per-repo paths. Author `refactor-plan.md` following the phase-plan format:

- A `## Tasks` section with one `### Task N: <heading>` per discrete change.
- Every Task in a multi-repo refactor MUST carry a `**Repo:** <name>` line whose value matches one of the repos in `Feature.Repos`.
- Each Task may include a `#### Automated Verification:` block with checklist items naming bash commands. The harness compiles those into the per-repo testing contract.
- A top-level `### Automated Verification` section may also be used. In a multi-repo refactor, every top-level command begins with `[repo: <name>]`. Commands run from that repository root; never add `cd <repo>` or use a Cross-Repo Verification section.

Do NOT make code edits in this step — that work happens in the iterations that follow. Emit `phase_complete` once `refactor-plan.md` is written.

### Step 2 — Iterate on the plan

Each iteration runs one agent session that:

1. Reads `refactor-plan.md` and the testing contract at `<artifactDir>/testing-contract.yaml`.
2. Dispatches per-repo Task sub-agents for the file edits. Each sub-agent's prompt names exactly one repo; the main agent owns the cross-repo coordination (e.g., land repo-a's API change before repo-b's import update).
3. Runs focused development tests relevant to each edit; Agentico owns final contract execution.
4. (When publishable) commits the per-repo edits and `git push --force-with-lease origin <branch>` for every touched repo. Use `--force-with-lease`, never plain `--force` — the lease guards against clobbering a collaborator's push.
5. Emits the standard `progress.md` handoff.

### Step 3 — Iteration handoff

Cycle-specific guidance for `progress.md`:

- `## Iteration Handoff → Completed this iteration` — one bullet per Task addressed (e.g., `- Task 1 (repoA): added shared-config.go, build passes`).
- `## Iteration Handoff → Remaining from the plan` — Tasks NOT yet addressed (empty when every plan-staged Task is done).
- `## Iteration State` — exactly one of:
  - `SUCCESS` — every plan-staged Task is addressed, development tests and agent-owned evidence are complete, and (when publishable) every touched repo's branch is force-pushed. Agentico runs final commands next.
  - `RETRY` — partial progress (e.g., one repo's edits landed cleanly; another needs reviewer feedback before continuing). The next iteration starts from your `progress.md`.
  - `NEED_USER_INPUT` — the refactor's design is fundamentally ambiguous (e.g., the user's prompt admits two architectures with no objective tiebreaker). Surface a `## Questions for User` section. Do NOT use this as an escape hatch for "the refactor is hard"; reserve for genuine ambiguity.

## Cross-repo refactor heuristics

- For a logical change spanning repositories, create one Task per repo and sequence them so consumers see a consistent state at every commit boundary. Prefer landing the upstream repo's change first (e.g., introduce the new shared package before importers reference it).
- When the same logical change has different shapes per repo (e.g., a renamed field with one repo using `snake_case` and another `camelCase`), describe each delta in that repo's Task.
- Declare the final command for each repository in that Task's Automated Verification block, or use explicitly scoped top-level commands. The harness records every result separately and the reviewer judges the combined outcome.

## What success looks like

- Every plan-staged Task is addressed (file edits committed) or explicitly dismissed in `progress.md` with a reasoning note.
- Focused development tests cover the changed behavior; Agentico runs the declared final commands.
- When publishable: every touched repo's branch is force-pushed to its remote PR branch.
- One `progress.md` handoff at the cycle root — no per-repo subdir.
- The cycle artifact layout is flat: `runs/run-N/refactor-N/iteration-NN/` for each iteration, with `refactor-plan.md` and `testing-contract.yaml` at the cycle root.
