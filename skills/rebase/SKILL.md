---
description: Execute a feature-level rebase cycle — rebase every behind branch in Feature.Repos onto its base, resolve conflicts, force-push, and emit the standard handoff
---

# Rebase Cycle

The harness has detected one or more behind branches across this feature's repos. Your job is to rebase each behind branch onto its base, resolve any conflicts, run the project verification commands, and force-push (when publishable). One Claude session per iteration; the same handoff contract as the implement skill applies.

## Workspace

- `cwd` is the feature state dir.
- `--add-dir` mounts every repo in the **behind subset** (only the repos with branches behind their base — repos already up-to-date are not mounted).
- The rebase plan at `## Handoff Contract → Plan path` lists each behind repo with its rebase target ref, PR URL, and any in-progress conflict files.
- The testing contract is **plan-less**: per-repo baseline rows (build/test/lint) only, no plan-source rows.

## Per-repo dispatch

Per-repo work is delegated **by prompt**, not by `cwd`. Each Task sub-agent's prompt names exactly one repo and constrains git ops to that repo's worktree. Today's main-vs-sub split is preserved:

- **Stays in main:** plan reading, sequencing across repos, reading the testing contract, emitting the handoff.
- **Delegated to per-repo Task agents:** the actual `git fetch` / `git rebase` / conflict resolution / `git push --force-with-lease` for each repo.

## The Process

### Step 1 — Read the plan

The rebase plan has one section per behind repo. For each repo, note:
- The rebase target (`origin/<base>` for publishable features, `<base>` for local-only).
- Whether the rebase is **already in progress** (`ConflictFiles` non-empty in the plan section). If so, do NOT run `git rebase --abort` — continue the existing rebase.
- The PR URL (when publishable) — referenced in commit messages and force-push status reporting only.

### Step 2 — Per repo: rebase

Dispatch one Task agent per behind repo. Each agent:

1. `git fetch origin` (publishable repos only).
2. If the repo's plan section says **rebase already in progress**: skip step 3 (the rebase started in a prior iteration); resume at step 4.
3. `git rebase origin/<target>` (publishable) or `git rebase <target>` (local-only).
4. For each conflicted file: open it, resolve the markers (`<<<<<<<` / `=======` / `>>>>>>>`), `git add <file>`.
5. `git rebase --continue` until the rebase completes.
6. Verify the worktree:
   - `git status` is clean (no `Unmerged paths`, no `interactive rebase in progress`).
   - No conflict markers remain: `grep -rn "<<<<<<< " <repo-path> | head -5` returns nothing.

### Step 3 — Per repo: force-push (publishable only)

Each repo's Task agent, after a clean rebase:

- `git push --force-with-lease origin <branch>` — `--force-with-lease` is mandatory; never use `--force` (it can clobber concurrent collaborator pushes).

Skip this step when the feature is not publishable (no remote).

### Step 4 — Verify

Run the testing contract's per-repo baseline rows. Each repo's contract items are tagged `repo: <name>`; dispatch one Task agent per repo to run that repo's `build`/`test`/`lint` commands and record results in `verification-report.yaml`.

The plan-less contract has no `cross-repo` items in the rebase cycle — verification is per-repo. Conflicts surfaced during rebase that span repos (e.g. a shared dependency version bump) get attested under `additional_checks:`.

### Step 5 — Iteration handoff

Emit `progress.md` and `verification-report.yaml` per the standard handoff contract (see [implement skill](../implement/SKILL.md) for the exact schema). Cycle-specific guidance:

- `## Iteration Handoff → Completed this iteration` — one bullet per repo rebased (e.g., `- api: rebased onto origin/main, force-pushed`).
- `## Iteration Handoff → Remaining from the plan` — repos NOT yet rebased (empty when every behind repo is done).
- `## Verification Report → Summary` — must agree with the YAML tally.
- `## Iteration State` — exactly one of:
  - `SUCCESS` — every behind repo is rebased, force-pushed (when publishable), and the per-repo baseline checks passed.
  - `RETRY` — partial progress (e.g., one repo rebased cleanly, another hit a non-trivial conflict you want to revisit with fresh context). The next iteration starts from your `progress.md`.
  - `NEED_USER_INPUT` — the conflict is fundamentally ambiguous (e.g., the same line was independently rewritten by two PRs and either resolution is plausible). Surface a `## Questions for User` section. Do NOT use this as an escape hatch for "the conflict is hard"; reserve for genuine ambiguity.

## Conflict resolution heuristics

- Read both sides of each conflict in full before resolving. The `<<<<<<<` block is your branch; the `>>>>>>>` block is the upstream commit being rebased onto.
- For `package.json` / `go.sum` / lockfile conflicts: regenerate from scratch (`pnpm install`, `go mod tidy`) rather than line-merging — the auto-generated content is rarely amenable to manual merge.
- For test files where both sides added independent test cases: keep both blocks (drop only the markers).
- For source code where both sides edited the same function: read the underlying intent of each change, write a single resolution that satisfies both, and run the test suite to verify.
- When the upstream commit deleted code your branch modified: keep the deletion, reconcile your modification by porting it to the new structure (or stashing it as a follow-up `Deferral` if the structure no longer supports it).

## Force-push safety

- Always `--force-with-lease`, never plain `--force`. Lease-based force-push aborts when the remote ref has advanced since your last fetch — that is the failsafe against clobbering a collaborator's push.
- The agent does NOT need to commit before rebasing; the rebase carries existing commits forward. If you find uncommitted local changes in a worktree, that is a pre-existing dirty state — emit `NEED_USER_INPUT` rather than guessing whether to stash, commit, or discard.

## What success looks like

- Every behind repo's branch sits on top of `<base>` (verified via `git log --oneline | head` showing the upstream commits before your branch's commits).
- `git status` is clean in each rebased worktree (no rebase in progress, no conflict markers).
- The remote PR branches reflect the rebased history (publishable repos only).
- The testing contract's per-repo baseline rows passed in `verification-report.yaml`.
- One handoff (`progress.md` + `verification-report.yaml`) at the cycle's iteration artifact dir — no per-repo subdir.
