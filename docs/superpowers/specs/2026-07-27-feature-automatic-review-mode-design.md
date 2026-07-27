# Feature-Level Automatic Review Mode Design

## Goal

Let users control Automatic Review for an individual feature in addition to
the existing workspace-wide setting. A feature can inherit the workspace
setting, explicitly enable Automatic Review, or explicitly disable it. When
Automatic Review is effectively enabled, the feature Overview must make that
state and its source visible.

## User Experience

The feature editor's Behavior tab gains an `Automatic Review` setting with
three values:

- `default`: inherit the current workspace setting;
- `enabled`: enable Automatic Review for new sessions in this feature;
- `disabled`: disable Automatic Review for new sessions in this feature.

The workspace editor remains a two-state `on` / `off` control because it owns
the inherited default rather than an override.

The feature Overview's Info box shows an `Auto mode` row only when Automatic
Review is effectively enabled:

- `On (Feature)` when the feature explicitly enables it;
- `On (Global)` when the feature is set to `default` and the workspace setting
  is enabled.

No row is shown when the effective value is disabled. The existing
`Automatic Review` terminology remains in configuration surfaces; `Auto mode`
is the compact Overview label requested for operational visibility.

## Persisted Model and Resolution

Add a closed feature-level mode type with the values `default`, `enabled`, and
`disabled`. Persist it on `feature.Feature` as
`automatic_review_mode,omitempty`; an absent value normalizes to `default` so
existing feature files retain current workspace-driven behavior.

Resolution is centralized in one feature-domain helper that accepts the
workspace boolean and returns both:

- the effective enabled boolean; and
- the source (`feature` or `global`) used for display and diagnostics.

The truth table is:

| Feature mode | Global off | Global on |
| --- | --- | --- |
| `default` | Off | On (Global) |
| `enabled` | On (Feature) | On (Feature) |
| `disabled` | Off | Off |

Invalid persisted values normalize to `default`. API mutations reject invalid
mode strings instead of silently persisting them.

The reviewer model remains workspace-owned. A feature override changes only
whether Automatic Review is enabled and continues using the workspace's
configured Automatic Review model.

## Session Behavior

Every fresh feature session resolves the feature mode against the current
workspace setting before constructing its permission handler. The resolved
boolean and reviewer identity continue to use the existing
`AutoReviewSnapshot`, preserving the current session boundary:

- feature or workspace edits affect only subsequently created sessions;
- running sessions are unchanged;
- crash-resumed sessions retain their original resolved enabled state and
  reviewer identity.

Feature-aware session launch paths pass the feature mode into the common
session builder explicitly. The session builder does not infer a feature from
working-directory paths or mutable process-global state. Non-feature helper
sessions retain the existing workspace behavior.

## API and Configuration Editing

Extend the feature config read and mutation contracts with
`automatic_review_mode`. The current feature config returns the normalized
mode, while the defaults section continues to expose the workspace enabled
boolean through the runtime defaults contract.

The feature config editor snapshots, edits, diffs, and saves the mode as a
first-class Behavior axis. The workspace editor keeps its existing boolean
state. Config-change audit snapshots include the feature mode so changes are
observable through the existing feature config change hook.

The feature detail read model includes the normalized mode plus effective
enabled state and source, calculated against the current workspace config.
This keeps the Overview accurate after either a feature edit or a workspace
edit without rewriting feature files. TUI adapters pass that resolved display
state into the compact detail renderer used by the Overview.

## Compatibility and Error Handling

- Existing workspace configuration files are unchanged.
- Existing feature files without the new field inherit the workspace setting.
- Feature saves that do not change the mode preserve its current value.
- Invalid API values return a validation error.
- Invalid legacy or hand-edited persisted values behave as `default` and are
  normalized on the next feature config save.
- Reviewer-resolution failures keep the existing behavior: Auto mode remains
  visibly enabled, while session notices explain that no reviewer is
  available and permission requests fall back safely.

## Testing

Implementation follows red-green TDD with focused tests for:

1. mode normalization and the complete resolution truth table;
2. feature store round-tripping and absent-field compatibility;
3. config mutation persistence, audit snapshots, and invalid API input;
4. feature config and detail API read models;
5. feature Behavior editor choices, change detection, and save payloads;
6. Overview visibility for feature-enabled, globally inherited, and disabled
   states;
7. fresh-session resolution and crash-resume snapshot stability.

Before handoff, run:

```bash
make test-fast
go test ./test/e2e/... -count=1 -race
go vet ./...
go build ./...
```

The E2E Go tier is required because the change touches TUI model behavior and
session lifecycle. The isolated integration and full race tiers are not
required because the change does not alter the lifecycle state machine, runs
layout, protocol handling, or concurrency.

## Non-Goals

- Configuring a different reviewer model per feature.
- Changing which Bash commands are eligible or how commands are classified.
- Changing the behavior of an already-running or crash-resumed session.
- Renaming existing workspace configuration fields.
