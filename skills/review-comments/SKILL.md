---
description: Execute a feature-level review-comments cycle — aggregate unaddressed PR comments across every Feature.Repos PR, address or dismiss each one, and emit the standard handoff
license: Apache-2.0
provenance: agentic-orchestrator-original
---

# Review-Comments Cycle

The harness has detected unaddressed PR review comments on one or more of this feature's PRs. Your job is to address (or, with reasoning, dismiss) every aggregated comment, validate your edits with focused development tests, and emit one combined `review-resolutions.json`. One agent session per iteration; the same handoff contract as the implement skill applies.

## Workspace

- `cwd` is the feature state dir.
- `--add-dir` mounts **every** `Feature.Repos` worktree — not just the repos with comments. Review threads frequently reference cross-repo behavior, and you may need to read a sibling repo's source to judge a comment correctly even when no edit lands there.
- The aggregated plan at `## Handoff Contract → Plan path` lists each repo with unaddressed comments, its PR URL, and the comments themselves. Every comment carries a `**Repo:** <name>` tag and `repo: <name>` annotation so you can route the fix.
- Agentico does not guess language-specific project commands for this plan-less cycle. Use focused development tests appropriate to the edits.

## Per-repo dispatch

Per-repo edits are delegated **by prompt**, not by `cwd`. Each Task sub-agent's prompt names exactly one repo and constrains file edits to that repo's worktree. Today's main-vs-sub split is preserved:

- **Stays in main:** plan reading, deciding address/dismiss for each comment, sequencing across repos, reading the testing contract, emitting the handoff, writing the combined `review-resolutions.json`.
- **Delegated to per-repo Task agents:** the actual code edits, focused development tests, commit + push.

## The Process

### Step 1 — Read the aggregated plan

The plan has one section per repo with unaddressed comments. For each comment, decide:

- **Addressed** — the feedback warrants a code change. Make the change in the named repo.
- **Dismissed** — the comment is already handled, not applicable, or the current approach is better. Document the reasoning in the resolution entry.

### Step 2 — Per repo: address the addressable comments

Dispatch one Task agent per repo with addressable comments. Each agent:

1. Make the targeted edit(s) in that repo's worktree. Do not refactor neighboring code; do not touch any repo not listed in the plan.
2. Run focused development tests relevant to the edit.

Cross-repo edits are allowed when a comment in repo A genuinely requires a change in repo B (e.g. a shared API contract). When that happens, document the cross-repo reasoning in the resolution entry for the original comment and verify the build in **both** repos.

### Step 3 — Write the combined resolutions JSON

After every comment has a decision, write **one** combined JSON file at the path named in the plan's `## Resolution Tracking` section. Format:

```json
[
  {"comment_id": 123, "disposition": "addressed", "description": "Fixed error handling"},
  {"comment_id": 456, "disposition": "dismissed", "description": "Already handled by existing validation"}
]
```

Every aggregated comment must appear exactly once. The orchestrator reads this file post-cycle to dispatch per-PR replies (via the GitHub API) and mark addressed comment IDs in the per-repo `addressed-ids.json` ledger.

### Step 4 — Per repo: commit + push

Each Task agent, once its edits are validated:

- `git add -A` and `git commit -m "Address review comments"` on that repo's branch.
- `git push origin <branch>` (no force needed; we're appending commits, not rewriting history).

The orchestrator's post-cycle finalisation also runs a commit-all + pull-rebase + push pass for safety; per-Task pushes are belt-and-suspenders so each branch's PR shows progress immediately when verification lands.

### Step 5 — Iteration handoff

Emit `progress.md` per the standard handoff contract (see [implement skill](../implement/SKILL.md) for the exact schema). Cycle-specific guidance:

- Standard handoff paths:
  - `progress.md`: `{phase_dir}/progress.md`
  - `phase_complete` is harness-owned; never create or edit it.
- Do not place `progress.md` under `{iteration_dir}`; the harness reads the phase-level progress file before routing the next iteration.
- `## Iteration Handoff → Completed this iteration` — one bullet per repo touched (e.g., `- api: addressed 3 comments, dismissed 1, force-pushed`).
- `## Iteration Handoff → Remaining from the plan` — comments not yet decided (empty when every aggregated comment has a resolution entry).
- `## Iteration State` — exactly one of:
  - `SUCCESS` — every aggregated comment has a resolution entry, your focused development tests on the touched repos pass, and the combined `review-resolutions.json` is written.
  - `RETRY` — partial progress (e.g., one comment fix broke a test; revisit on the next iteration with fresh context). The next iteration starts from your `progress.md`.
  - If a comment is fundamentally ambiguous, call the formal AskUserQuestion control and continue after the answer.

## Comment resolution heuristics

- Read the diff hunk (when present) before deciding. The reviewer's comment is anchored to specific code; without that anchor, dismissals frequently miss the actual concern.
- For comments asking "why this approach?": if the answer is in the plan or roadmap, dismiss with a pointer to the source. If the answer is genuinely "the alternative is better", address it.
- For comments requesting cosmetic changes (naming, style): address them unless they conflict with project guidelines. Don't argue style.
- For comments requesting behavior changes: read the affected tests carefully. A behavior change without a test update is a regression risk.
- For comments asking about cross-repo behavior: read the sibling repo's source before deciding. The full workspace is mounted for exactly this case.

## What success looks like

- Every aggregated comment has exactly one entry in `review-resolutions.json`.
- Addressed comments have real code changes in the named repo.
- Each touched repo's branch is committed and pushed (the orchestrator's finalisation runs a safety push too).
- One standard `progress.md` handoff at `{phase_dir}/progress.md` — no per-repo subdir.
