---
description: Execute a feature-level rebase cycle — rebase conflicting branches onto their bases, resolve conflicts, leave every worktree clean, and emit the standard handoff without pushing
license: Apache-2.0
provenance: agentic-orchestrator-original
---

# Rebase Cycle

The harness has detected one or more conflicting branches across this feature's repos. Your job is to rebase each listed target onto its base, resolve conflicts, leave every worktree clean, and stop without pushing. Agentico executes the plan-declared conflict-marker check after handoff; the orchestrator runs Final Review and applies publish policy after approval. One agent session per iteration; the same handoff contract as the implement skill applies.

## Workspace

- `cwd` is the feature state dir.
- `--add-dir` mounts the feature workspace selected by the orchestrator. The target/conflict subset is listed in the rebase plan; additional mounted repos are validation context and may be edited only when verification proves the cross-repo change is necessary.
- The rebase plan at `## Handoff Contract → Plan path` lists each conflicted repo with its rebase target ref, PR URL, and any in-progress conflict files.
- The generated plan declares the exact conflict-marker check. Agentico executes it after handoff; no language-specific project commands are guessed.

## Per-repo dispatch

Per-repo work is delegated **by prompt**, not by `cwd`. Each Task sub-agent's prompt names exactly one target repo and constrains git ops to that repo's worktree. The main agent keeps cross-repo context and may ask sub-agents to inspect validation-context repos when needed.

- **Stays in main:** plan reading, sequencing across repos, reading the testing contract, coordinating cross-repo validation, and emitting the handoff.
- **Delegated to per-repo Task agents:** the actual `git fetch` / `git rebase` / conflict resolution for each target repo.

## The Process

### Step 1 — Read the plan

The rebase plan has one section per target repo. For each repo, note:
- The rebase target (`origin/<base>` for publishable features, `<base>` for local-only).
- Whether the rebase is **already in progress** (`ConflictFiles` non-empty in the plan section). If so, do NOT run `git rebase --abort` — continue the existing rebase.
- The PR URL (when publishable) — useful context only. Do not push.

### Step 2 — Per repo: rebase

Dispatch one Task agent per behind repo. Each agent:

1. `git fetch origin` (publishable repos only).
2. If the repo's plan section says **rebase already in progress**: skip step 3 (the rebase started in a prior iteration); resume at step 4.
3. `git rebase origin/<target>` (publishable) or `git rebase <target>` (local-only).
4. For each conflicted file: open it, resolve the markers (`<<<<<<<` / `=======` / `>>>>>>>`), `git add <file>`.
5. `git rebase --continue` until the rebase completes.
6. Verify the worktree:
   - `git status` is clean (no `Unmerged paths`, no `interactive rebase in progress`).
   - No conflict markers remain: `! grep -rln "<<<<<<< " <repo-path>` exits 0.

### Step 3 — Do not push

Stop after the rebase is clean and verification is complete. Do not run any push command. The orchestrator runs Final Review and applies publish policy after approval.

### Step 4 — Development checks

Inspect each rebased worktree for a clean status and unresolved conflicts. Agentico runs the plan-declared final command after handoff.

### Step 5 — Iteration handoff

Emit `progress.md` per the standard handoff contract (see [implement skill](../implement/SKILL.md) for the exact schema). Cycle-specific guidance:

- Standard handoff paths:
  - `progress.md`: `{phase_dir}/progress.md`
  - `phase_complete` is harness-owned; never create or edit it.
- Do not place `progress.md` under `{iteration_dir}`; the harness reads the phase-level progress file before routing the next iteration.
- `## Iteration Handoff → Completed this iteration` — one bullet per repo rebased (e.g., `- api: rebased onto origin/main, verified; not pushed`).
- `## Iteration Handoff → Remaining from the plan` — repos NOT yet rebased (empty when every behind repo is done).
- `## Iteration State` — exactly one of:
  - `SUCCESS` — every target repo is rebased, no rebase is in progress, and conflict markers are gone (Agentico re-verifies the plan-declared conflict-marker check after handoff). Do not require or report a push.
  - `RETRY` — partial progress (e.g., one repo rebased cleanly, another hit a non-trivial conflict you want to revisit with fresh context). The next iteration starts from your `progress.md`.
  - If the conflict is fundamentally ambiguous, call the formal AskUserQuestion control. Do not use it as an escape hatch for "the conflict is hard"; reserve it for genuine ambiguity.

## Conflict resolution heuristics

- Read both sides of each conflict in full before resolving. The `<<<<<<<` block is your branch; the `>>>>>>>` block is the upstream commit being rebased onto.
- For `package.json` / `go.sum` / lockfile conflicts: regenerate from scratch (`pnpm install`, `go mod tidy`) rather than line-merging — the auto-generated content is rarely amenable to manual merge.
- For test files where both sides added independent test cases: keep both blocks (drop only the markers).
- For source code where both sides edited the same function: read the underlying intent of each change, write a single resolution that satisfies both, and run the test suite to verify.
- When the upstream commit deleted code your branch modified: keep the deletion, reconcile your modification by porting it to the new structure (or stashing it as a follow-up `Deferral` if the structure no longer supports it).

## Push policy

- Do not push from this cycle. The orchestrator runs Final Review and applies publish policy after approval.
- The agent does NOT need to commit before rebasing; the rebase carries existing commits forward. If you find uncommitted local changes in a worktree, that is a pre-existing dirty state — call the formal AskUserQuestion control rather than guessing whether to stash, commit, or discard.

## What success looks like

- Every target repo's branch sits on top of `<base>` (verified via `git log --oneline | head` showing the upstream commits before your branch's commits).
- `git status` is clean in each rebased worktree (no rebase in progress, no conflict markers).
- The remote PR branches are not pushed by this cycle.
- One standard `progress.md` handoff at `{phase_dir}/progress.md` — no per-repo subdir.
