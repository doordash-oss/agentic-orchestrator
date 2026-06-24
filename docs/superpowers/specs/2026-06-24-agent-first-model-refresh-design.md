# Agent-First Model Refresh Design

## Context

OpenCode now exposes models such as `portkey/@fireworks/accounts/fireworks/models/glm-5p2`, but Agentico can miss them for two reasons:

- Startup model catalogs are cached by provider CLI version. If OpenCode's reachable catalog changes without the `opencode` CLI version changing, Agentico loads the old cache and never reaches live discovery.
- OpenCode model discovery and launch validation currently reject `@` in backend IDs, so a refreshed catalog would still drop GLM 5.2 and an explicit `opencode:portkey/@fireworks/.../glm-5p2` selection would fail before launch.

At the same time, OpenCode can expose many long backend IDs. The current TUI model picker treats models as strings and cycles values in row order, which is too opaque for large per-agent catalogs.

## Goals

- Add a user-controlled way to refresh model catalogs at startup.
- Preserve startup resilience when refresh fails.
- Make OpenCode backend IDs with `@` discoverable and launchable without weakening command safety.
- Upgrade the wizard and Config panel model UX to choose coding agent first, then a model available through that agent.
- Keep existing persisted config format stable; this is not a config migration.

## Non-Goals

- No dedicated full-screen model-management area in this iteration.
- No background or in-TUI refresh command after startup.
- No changes to provider authentication flows.
- No automatic deletion of old cache files beyond overwriting the active provider/version cache on successful refresh.

## CLI And Cache Behavior

Add `agentico --refresh-models`.

Normal startup keeps the current behavior:

1. Determine each ready provider's cache key from its parsed CLI version.
2. Load the provider catalog cache when present and valid.
3. Run live discovery only on cache miss, corrupt cache, empty cache, or uncacheable version.
4. Save a discovered catalog back to the version-keyed cache.

Refresh startup changes only the cache-read decision:

1. Determine each ready provider's cache key as today.
2. Skip the initial cache read.
3. Run live discovery for every ready catalog provider.
4. Save a fresh cache on success.
5. If live discovery fails, try to load the previous cache for the same provider/version.
6. If stale cache load succeeds, install it and warn that stale cache is being used.
7. If no stale cache is available, keep the current built-in fallback behavior and warning.

The flag is provider-generic. It applies to Claude, Codex, OpenCode, and future catalog providers that use the shared discovery/cache path.

## OpenCode ID Safety

OpenCode discovery and launch validation should allow `@` inside slash-form backend IDs, including:

`portkey/@fireworks/accounts/fireworks/models/glm-5p2`

The validation rules remain fail-closed:

- Reject empty model IDs.
- Reject IDs that start with `-`.
- Reject whitespace.
- Reject shell/interpolation metacharacters.
- Reject credential-shaped strings using the existing token-prefix checks.
- Require slash-form provider/model shape.

The accepted model should work in all three paths:

- Discovered from `opencode models --verbose --refresh`.
- Selected through the TUI model picker.
- Passed explicitly as `opencode:portkey/@fireworks/accounts/fireworks/models/glm-5p2`.

## Catalog Metadata

The TUI should stop treating phase model choices as raw strings. Keep persisted config strings unchanged, but build a richer display catalog for model-selection surfaces.

Each display entry should include:

- `Agent`: Agentico provider name, such as `claude`, `codex`, or `opencode`.
- `ModelID`: the model value used for selection within that agent's catalog.
- `DisplayName`: friendly model name when known.
- `FullID`: full backend or provider-qualified ID for details and matching.
- `ContextWindow`: token window when known.
- `Category`: role category used for phase eligibility.
- `Recommended`: whether this entry is the recommended model for the current phase.
- `Aliases`: alternate strings that should match the same entry.

This structure lives in the TUI/catalog layer, not in the on-disk config. Saving a choice still writes the existing model string format expected by the registry and provider routing. When multiple providers are active, OpenCode choices may need the `opencode:` prefix to avoid ambiguity; when OpenCode is the only ready provider, the bare backend ID remains acceptable.

## Agent-First Picker UX

Use the existing shared `ConfigEditorModel` seam so the wizard review step and Config panel stay consistent.

The Models pane becomes a five-row table:

```text
Phase           Agent       Model
Research        Claude      Sonnet 4.5
Planning        OpenCode    GLM 5.2
Implementation  OpenCode    Gemma 4 31B Dense
Review          Codex       GPT-5.4
KB Build        Claude      Haiku 4.5
```

The focused row expands below the table. It shows:

- Agent choices for the focused phase.
- Model choices filtered to the selected agent and eligible for that phase.
- Optional model filter text for large lists.
- Friendly display name first.
- Full backend ID and metadata in a detail/help line for the highlighted model.

Long backend IDs should never dominate the table. They appear in muted detail text and are truncated only visually, never in the stored selection.

Keyboard behavior:

- `up` / `down` or `k` / `j`: move between phase rows.
- `tab` / `shift+tab`: switch active cell between Agent and Model.
- `left` / `right` or `h` / `l`: cycle the active cell.
- `/`: start filtering models for the focused phase and selected agent.
- text input while filtering: update the filtered model list.
- `enter`: accept the highlighted filtered model, or finish editing when not filtering.
- `esc`: exit filtering first; existing close/cancel behavior remains outside filtering.

When the Agent cell changes, the Model cell immediately changes to that agent's recommended eligible model for the same phase. If there is no recommendation for that agent/phase, choose the first eligible model for that agent/phase. If no eligible model exists, keep the previous value marked unavailable and render a clear empty-state line.

## Rendering Rules

The table should favor scanability:

- Phase and Agent columns stay stable width.
- Model column uses display names, not full backend IDs.
- The active row has an obvious focus marker and selected cell indication.
- Recommendation is shown with text or a stable marker, not color alone.
- Filtering shows a no-results state without clearing the current saved selection.
- Unavailable saved selections remain visible with an `(unavailable)` marker until the user changes them.

## Error And Warning UX

Startup refresh warnings should be specific:

- `Warning: could not refresh opencode model catalog; using stale cache from <timestamp>: <error>`
- If stale cache also fails: keep the current built-in fallback warning and mention refresh failure.

Cache warnings must not echo raw provider output that could contain credentials. They should continue using sanitized provider diagnostics.

## Test Plan

Add tests before production code.

- CLI parser accepts `--refresh-models` and passes the flag to startup.
- Normal startup still loads cache before discovery.
- Refresh startup skips cache read and runs discovery.
- Refresh failure with prior cache installs stale cache and warns.
- Refresh failure without prior cache uses built-in fallback behavior.
- OpenCode parser accepts `portkey/@fireworks/accounts/fireworks/models/glm-5p2`.
- OpenCode parser still rejects malformed IDs, whitespace, flags, shell metacharacters, and credential-shaped IDs.
- OpenCode command validation accepts explicit `opencode:portkey/@fireworks/accounts/fireworks/models/glm-5p2`.
- Catalog display entries preserve config string round-trip behavior.
- Agent-first model editor changes model recommendation when agent changes.
- Agent-first model editor filters models only within the selected agent.
- Rendered model table keeps long IDs out of the main row and displays full ID in detail text.

## Verification

Targeted verification:

```bash
go test ./cmd/agentico ./internal/llm/opencode ./internal/tui -count=1
```

Required handoff verification:

```bash
make test-fast
```

Relevant PR verification tiers:

- Fast suite
- TUI package tests

Skip extended E2E and integration tiers unless implementation touches session lifecycle, launch command construction outside provider validation, or broader TUI runtime behavior.
