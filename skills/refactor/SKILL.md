---
description: Execute a feature-level refactor cycle — author a refactor plan with per-Task **Repo:** tags, then implement the plan across every Feature.Repos worktree, supporting cross-repo edits in a single iteration
license: Apache-2.0
provenance: agentic-orchestrator-original
---

# Refactor Cycle

The harness has dispatched a refactor cycle on a published feature. Your job is to author a refactor plan that scopes the work via per-Task `**Repo:** <name>` tags, then implement the plan across every staged repo. Cross-repo Tasks (one Task touching multiple repos) are first-class; the same Claude session iterates over every repo via Task sub-agents.

## Output Files

| Artifact | Path | Requirement | Purpose |
|----------|------|-------------|---------|
| `refactor-plan.md` | `{phase_dir}/refactor-plan.md` | required | refactor plan markdown with phase-plan-style tasks and per-repo tags |

## Workspace

- `cwd` is the feature state dir.
- `--add-dir` mounts **every** `Feature.Repos` worktree, regardless of which repos the plan stages. Refactors are cross-repo by design — the agent needs read access to all repos to judge a refactor's impact, even when the edits land in a subset.
- The refactor-plan markdown lives at `<artifactDir>/refactor-plan.md`.
- The testing contract is **planned**: per-repo baseline rows + plan-source rows extracted from the plan's per-Task Automated Verification blocks, every item tagged with `repo:`.

## Per-repo dispatch

Per-repo work is delegated **by prompt**, not by `cwd`. Each Task sub-agent's prompt names exactly one repo and constrains file edits to that repo's worktree. Cross-repo Tasks list every involved repo on its own `**Repo:**` line; the main agent dispatches one sub-agent per repo to execute the cross-repo coordination.

- **Stays in main:** plan reading, deciding which repos to stage, sequencing cross-repo edits, reading the testing contract, emitting the handoff.
- **Delegated to per-repo Task agents:** the actual file edits, build/test/lint runs, and (when publishable) `git commit` + `git push --force-with-lease`.

## The Process

### Step 1 — Author the refactor plan (refactor-plan step)

This step runs once per cycle. Read the user's refactor request, the feature description, and the workspace's per-repo paths. Author `refactor-plan.md` following the phase-plan format:

- A `## Tasks` section with one `### Task N: <heading>` per discrete change.
- Every Task in a multi-repo refactor MUST carry a `**Repo:** <name>` line whose value matches one of the repos in `Feature.Repos`.
- Cross-repo Tasks list each involved repo on its own `**Repo:**` line in the Task body. PhaseScope deduplicates and sorts the resulting repo set; the harness uses that set as the staged subset for AtomicPhaseStamp.
- Each Task may include a `#### Automated Verification:` block with checklist items naming bash commands. The harness compiles those into the per-repo testing contract.
- An optional top-level `#### Cross-Repo Verification:` block carries verification commands that exercise more than one repo at once (these become `repo: cross-repo` testing-contract rows).

Do NOT make code edits in this step — that work happens in the iterations that follow. Emit `phase_complete` once `refactor-plan.md` is written.

### Step 2 — Iterate on the plan

Each iteration runs one Claude session that:

1. Reads `refactor-plan.md` and the testing contract at `<artifactDir>/testing-contract.yaml`.
2. Dispatches per-repo Task sub-agents for the file edits. Each sub-agent's prompt names exactly one repo; the main agent owns the cross-repo coordination (e.g., land repo-a's API change before repo-b's import update).
3. Per touched repo, runs the build/test/lint commands from the testing contract's per-repo baseline rows + that repo's plan-source rows. Records each result in `verification-report.yaml`.
4. (When publishable) commits the per-repo edits and `git push --force-with-lease origin <branch>` for every touched repo. Use `--force-with-lease`, never plain `--force` — the lease guards against clobbering a collaborator's push.
5. Emits the standard handoff (`progress.md` + `verification-report.yaml`) at `<artifactDir>/iteration-NN/`.

### Step 3 — Iteration handoff

Cycle-specific guidance for `progress.md`:

- `## Iteration Handoff → Completed this iteration` — one bullet per Task addressed (e.g., `- Task 1 (repoA): added shared-config.go, build passes`).
- `## Iteration Handoff → Remaining from the plan` — Tasks NOT yet addressed (empty when every plan-staged Task is done).
- `## Verification Report → Summary` — must agree with the YAML tally, including the `cross-repo` rows when present.
- `## Iteration State` — exactly one of:
  - `SUCCESS` — every plan-staged Task is addressed, the per-repo baseline + plan-source rows passed, any `cross-repo` verification rows passed, and (when publishable) every touched repo's branch is force-pushed.
  - `RETRY` — partial progress (e.g., one repo's edits landed cleanly; another needs reviewer feedback before continuing). The next iteration starts from your `progress.md`.
  - `NEED_USER_INPUT` — the refactor's design is fundamentally ambiguous (e.g., the user's prompt admits two architectures with no objective tiebreaker). Surface a `## Questions for User` section. Do NOT use this as an escape hatch for "the refactor is hard"; reserve for genuine ambiguity.

## Cross-repo refactor heuristics

- When a Task targets multiple repos, sequence the edits so consumers see a consistent state at every commit boundary. Prefer landing the upstream repo's change first (e.g., introduce the new shared package before importers reference it).
- When the same logical change has different shapes per repo (e.g., a renamed field with one repo using `snake_case` and another `camelCase`), express the per-repo deltas inside the same Task with `**Repo:**` listed twice in the Task body — one Task, two repo lines.
- Cross-repo verification commands (top-level `#### Cross-Repo Verification:`) typically run a build/test step in repo-a and a different one in repo-b that depends on repo-a's output (e.g., a contract test). Tag these `repo: cross-repo`; the verification report cross-checks coverage across repos.
- Out-of-plan touches (file edits in repos NOT named by any Task tag) are tracked under `additional_checks:` in the verification report. The reviewer enforces policy: an out-of-plan touch is a High finding by default, downgradable only if it is a trivial mechanical follow-on (e.g., updating an import path that the in-plan edit broke).

## What success looks like

- Every plan-staged Task is addressed (file edits committed) or explicitly dismissed in `progress.md` with a reasoning note.
- Per-repo baseline + plan-source rows pass on every touched repo, attested in `verification-report.yaml`.
- Any `cross-repo` verification rows pass.
- When publishable: every touched repo's branch is force-pushed to its remote PR branch.
- One handoff (`progress.md` + `verification-report.yaml`) at the cycle's iteration artifact dir — no per-repo subdir.
- The cycle artifact layout is flat: `runs/run-N/refactor-N/iteration-NN/` for each iteration, with `refactor-plan.md` and `testing-contract.yaml` at the cycle root.
