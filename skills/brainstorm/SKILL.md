---
description: Legacy compatibility wrapper for the Design phase (formerly Brainstorm)
---

# Brainstorm — Legacy Compatibility Wrapper for Design

This skill is the legacy compatibility path for what is now called the **Design**
phase. The product surface, agent contracts, and documentation use Design
language; this file exists only so legacy persisted state, session identifiers,
and role lookups that still resolve to the "brainstorm" name continue to load
without migration.

If you reached this skill, treat it as identical in behavior to the Design
skill at [skills/design/SKILL.md](../design/SKILL.md):

- **What you produce**: a design document at the artifact path declared below.
- **How you behave**: follow the Design skill's process and document structure
  exactly. Do not introduce a separate "brainstorm" artifact concept — the
  artifact is a design document and should read like one.
- **Why this file exists**: phase directories on disk are still named
  `brainstorm/`, and a few historical role and session identifiers retain the
  `brainstorm` suffix. The orchestrator routes both names to the same Design
  behavior.

## Output Files

| Artifact | Path | Requirement | Purpose |
|----------|------|-------------|---------|
| `brainstorm markdown artifact` | `{phase_dir}/<newest non-excluded *.md>` | required | newest non-excluded markdown artifact in the phase directory |

## Canonical Guidance

For the canonical process, principles, and design-document structure, see
[skills/design/SKILL.md](../design/SKILL.md). This wrapper exists only to keep
legacy routing working; the behavior it describes is the Design behavior.
