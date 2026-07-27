# Feature-Level Automatic Review Mode Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an inheritable feature-level Automatic Review mode and display the effective enabled state in the feature Overview.

**Architecture:** Persist a closed `default|enabled|disabled` mode on each feature and centralize resolution against the workspace boolean in the feature domain. Carry the mode through feature config APIs and audit snapshots, resolve it at fresh session creation while preserving the existing crash-resume snapshot, and expose a server-computed effective state to the Overview renderer.

**Tech Stack:** Go 1.24, YAML feature persistence, OpenAPI/oapi-codegen REST contracts, Bubble Tea v2, Lip Gloss v2, Go `testing`.

## Global Constraints

- Existing feature files with no mode must continue inheriting the workspace setting.
- Invalid persisted values normalize to `default`; invalid API mutations are rejected.
- The reviewer model remains workspace-owned.
- Running and crash-resumed sessions retain their snapshotted Automatic Review policy.
- Every new `*.go`, `*.sh`, or `*.py` file must carry the Apache 2.0 header from `AGENTS.md`; this plan creates no new source files.
- Use red-green TDD for every behavior change.
- Run the Fast suite, E2E Go tier, `go vet ./...`, and `go build ./...` before handoff.

---

## File Map

- `internal/feature/feature.go`: mode/source types, normalization, resolution, persisted feature field, display-only effective fields, config snapshots.
- `internal/feature/feature_test.go`, `internal/feature/store_test.go`: truth table and compatibility persistence coverage.
- `internal/orchestrator/lifecycle_delegates.go`, `internal/orchestrator/update_feature_config_test.go`: atomically persist and audit the feature mode.
- `internal/server/mutation.go`, `cmd/agentico/main.go`: mutation validation and orchestration transport.
- `api/openapi.yaml`, `internal/server/serverapi.gen.go`: public config and feature-detail contracts.
- `internal/server/read_model.go`, `internal/server/read_model_test.go`: normalized config DTOs and effective detail state.
- `internal/tui/editconfig.go`, `internal/tui/api_app.go`: feature Behavior control, save payload, and DTO adaptation.
- `internal/tui/autoreview_editconfig_test.go`, `internal/tui/api_app_test.go`: editor and API adapter behavior.
- `internal/agent/phase.go`, `internal/agent/autoreview_internal_test.go`: resolve the current feature mode for fresh sessions and preserve resume snapshots.
- `internal/tui/detail.go`, `internal/tui/detail_test.go`: Overview Info row.

---

### Task 1: Feature-Domain Mode and Resolution

**Files:**
- Modify: `internal/feature/feature.go`
- Test: `internal/feature/feature_test.go`
- Test: `internal/feature/store_test.go`

**Interfaces:**
- Produces: `type AutomaticReviewMode string`
- Produces: `NormalizeAutomaticReviewMode(AutomaticReviewMode) AutomaticReviewMode`
- Produces: `ParseAutomaticReviewMode(string) (AutomaticReviewMode, error)`
- Produces: `ResolveAutomaticReview(AutomaticReviewMode, bool) (enabled bool, source AutomaticReviewSource)`
- Produces: `PersistAutomaticReviewMode(AutomaticReviewMode) AutomaticReviewMode`

- [ ] **Step 1: Write failing resolution and parse tests**

Add table tests with literal expectations:

```go
func TestResolveAutomaticReview(t *testing.T) {
	tests := []struct {
		mode    feature.AutomaticReviewMode
		global  bool
		enabled bool
		source  feature.AutomaticReviewSource
	}{
		{"", false, false, feature.AutomaticReviewSourceGlobal},
		{"default", true, true, feature.AutomaticReviewSourceGlobal},
		{"enabled", false, true, feature.AutomaticReviewSourceFeature},
		{"enabled", true, true, feature.AutomaticReviewSourceFeature},
		{"disabled", true, false, feature.AutomaticReviewSourceFeature},
		{"bogus", true, true, feature.AutomaticReviewSourceGlobal},
	}
	// Call ResolveAutomaticReview and compare both outputs.
}
```

Add parse cases proving empty input normalizes to `default`,
`default|enabled|disabled` are accepted, and every other value is rejected.
Add a store round-trip proving an absent YAML field loads as default while an
explicit enabled value survives save/load.

- [ ] **Step 2: Run focused tests and verify RED**

Run:

```bash
go test ./internal/feature -run 'Test(Resolve|Parse|Store.*AutomaticReview)' -count=1
```

Expected: compile failure because the mode types/functions and feature field do not exist.

- [ ] **Step 3: Implement the minimal domain model**

Add:

```go
type AutomaticReviewMode string

const (
	AutomaticReviewDefault  AutomaticReviewMode = "default"
	AutomaticReviewEnabled  AutomaticReviewMode = "enabled"
	AutomaticReviewDisabled AutomaticReviewMode = "disabled"
)

type AutomaticReviewSource string

const (
	AutomaticReviewSourceGlobal  AutomaticReviewSource = "global"
	AutomaticReviewSourceFeature AutomaticReviewSource = "feature"
)
```

Normalize empty and unknown values to `default`; parse must reject values
outside the closed set. Persist `default` as the empty value so
`yaml:",omitempty"` preserves backward-compatible feature files. Resolve
explicit modes before consulting the workspace boolean.

Add these fields to `Feature`:

```go
AutomaticReviewMode    AutomaticReviewMode   `yaml:"automatic_review_mode,omitempty"`
AutomaticReviewEnabled bool                  `yaml:"-"`
AutomaticReviewSource  AutomaticReviewSource `yaml:"-"`
```

Add `AutomaticReviewMode` to `ConfigSnapshot`.

- [ ] **Step 4: Run focused tests and verify GREEN**

Run:

```bash
go test ./internal/feature -run 'Test(Resolve|Parse|Store.*AutomaticReview)' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit the domain boundary**

```bash
git add internal/feature/feature.go internal/feature/feature_test.go internal/feature/store_test.go
git commit -m "Let features express automatic review intent"
```

---

### Task 2: Persist and Transport Feature Configuration

**Files:**
- Modify: `internal/orchestrator/lifecycle_delegates.go`
- Modify: `internal/orchestrator/update_feature_config_test.go`
- Modify: `internal/server/mutation.go`
- Modify: `cmd/agentico/main.go`
- Modify: `api/openapi.yaml`
- Regenerate: `internal/server/serverapi.gen.go`
- Test: `internal/server/read_model_test.go`
- Test: `internal/server/client_test.go`

**Interfaces:**
- Consumes: `feature.AutomaticReviewMode`, `feature.ParseAutomaticReviewMode`
- Produces: `UpdateFeatureConfigInput.AutomaticReviewMode`
- Produces: JSON field `automatic_review_mode`
- Produces: `AutomaticReviewState{Mode, Enabled, Source}` in feature detail

- [ ] **Step 1: Write failing orchestration and API tests**

Extend `TestUpdateFeatureConfig_QuiescentWritesAllThreeAxes` (renaming it to
describe all editable axes) with an initial disabled mode, an enabled input,
and assertions on the feature plus before/after audit snapshots.

Add server tests asserting:

```go
if got := featureConfigDTO(f).AutomaticReviewMode; got != "enabled" {
	t.Fatalf("AutomaticReviewMode = %q, want enabled", got)
}
```

Add HTTP mutation coverage proving `"automatic_review_mode":"enabled"` reaches
the mutation target and `"automatic_review_mode":"bogus"` returns HTTP 400.
Add detail coverage for the three effective states from the design truth table.

- [ ] **Step 2: Run focused tests and verify RED**

Run:

```bash
go test ./internal/orchestrator ./internal/server ./cmd/agentico -run 'AutomaticReview|UpdateFeatureConfig' -count=1
```

Expected: compile failures for missing input/DTO fields.

- [ ] **Step 3: Extend persistence and validate mutations**

Add `AutomaticReviewMode feature.AutomaticReviewMode` to
`UpdateFeatureConfigInput`. In `UpdateFeatureConfig`, include the normalized
mode in `before`/`after`, and persist:

```go
f.AutomaticReviewMode = feature.PersistAutomaticReviewMode(input.AutomaticReviewMode)
```

Add `AutomaticReviewMode *string` to `FeatureConfigMutationRequest`. Validate
a non-nil value in the HTTP handler with
`feature.ParseAutomaticReviewMode`; reject invalid input with the same
bad-request helper used by other mutation validation. A nil field means
"preserve the current mode" for backward-compatible clients. In
`serverMutationTarget.UpdateFeatureConfig`, load the current feature mode when
the pointer is nil; otherwise parse the pointed-to string and forward the
result.

- [ ] **Step 4: Extend and regenerate the OpenAPI contract**

Add `automatic_review_mode` to `FeatureConfig`. Add:

```yaml
AutomaticReviewState:
  type: object
  required: [mode, enabled, source]
  properties:
    mode:
      type: string
      enum: [default, enabled, disabled]
    enabled:
      type: boolean
    source:
      type: string
      enum: [global, feature]
```

Add `automatic_review` referencing that schema to `FeatureDetail`, then run:

```bash
make generate-openapi
```

Populate `featureConfigDTO` with the normalized mode. In
`featureDetailDTO`, resolve the feature mode against
`h.configOrDefault().Defaults.AutomaticReviewEnabled` and populate all three
state fields with literal strings from the domain types.

- [ ] **Step 5: Run focused tests and verify GREEN**

Run:

```bash
go test ./internal/orchestrator ./internal/server ./cmd/agentico -run 'AutomaticReview|UpdateFeatureConfig' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit the persisted API contract**

```bash
git add internal/orchestrator/lifecycle_delegates.go internal/orchestrator/update_feature_config_test.go internal/server/mutation.go internal/server/read_model.go internal/server/read_model_test.go internal/server/client_test.go cmd/agentico/main.go api/openapi.yaml internal/server/serverapi.gen.go
git commit -m "Keep feature automatic review settings durable and observable"
```

---

### Task 3: Feature Behavior Editor

**Files:**
- Modify: `internal/tui/editconfig.go`
- Modify: `internal/tui/api_app.go`
- Test: `internal/tui/autoreview_editconfig_test.go`
- Test: `internal/tui/api_app_test.go`

**Interfaces:**
- Consumes: normalized `feature.AutomaticReviewMode`
- Produces: feature config mutation payload field `AutomaticReviewMode`

- [ ] **Step 1: Replace the feature-hidden test with failing tri-state tests**

Replace `TestFeatureEditorDoesNotShowAutomaticReview` with tests proving:

- both workspace and feature editors show `Automatic Review`;
- workspace choices remain `on` / `off`;
- feature choices render `default`, `enabled`, and `disabled`;
- cycling from default reaches enabled and disabled;
- mode changes participate in `HasChanges`, `behaviorChanged`, and
  `diffSummary`;
- the save command sends `AutomaticReviewMode: "enabled"`.

Use the real editor model and captured API request rather than asserting on a
mock UI component.

- [ ] **Step 2: Run TUI editor tests and verify RED**

Run:

```bash
go test ./internal/tui -run 'AutomaticReview|FeatureConfig' -count=1
```

Expected: failures because feature editors still hide the setting and save
payloads omit it.

- [ ] **Step 3: Implement the feature tri-state editor**

Add mode/original-mode fields to `EditConfigModel` and initialize them from
`feature.Feature`. Make `behaviorSettings` always include Automatic Review.
Branch the Automatic Review values/detail rendering:

- workspace: existing boolean `on` / `off`;
- feature: `default` / `enabled` / `disabled`, with details explaining the
  effective scope.

Branch cycling/toggling and change detection the same way. Preserve the
workspace Models-tab reviewer selector as workspace-only.

Update `apiFeatureFromConfig` to hydrate the mode, and
`saveFeatureConfigCmd` to send:

```go
AutomaticReviewMode: string(feature.NormalizeAutomaticReviewMode(editor.automaticReviewMode)),
```

Assign that string to a local and send its address because the mutation field
is presence-sensitive.

- [ ] **Step 4: Run TUI editor tests and verify GREEN**

Run:

```bash
go test ./internal/tui -run 'AutomaticReview|FeatureConfig' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit the configuration UI**

```bash
git add internal/tui/editconfig.go internal/tui/api_app.go internal/tui/autoreview_editconfig_test.go internal/tui/api_app_test.go
git commit -m "Give each feature control over automatic review"
```

---

### Task 4: Fresh-Session Resolution and Resume Stability

**Files:**
- Modify: `internal/agent/phase.go`
- Test: `internal/agent/autoreview_internal_test.go`

**Interfaces:**
- Consumes: `FeatureStore.Load(featureID)` and `ResolveAutomaticReview`
- Produces: `BuildSessionOpts.FeatureID string`
- Preserves: `ports.AutoReviewSnapshot` precedence on crash resume

- [ ] **Step 1: Write failing fresh-session policy tests**

Add table tests that save a feature with each mode, set workspace on/off, call
`BuildSession` with `FeatureID`, and assert the returned
`SessionOpts.AutoReview.Enabled` truth table. Add a case changing a persisted
feature from default to enabled between two fresh builds and assert the second
build sees the update. Extend the existing crash-resume test so a saved feature
mode change does not override a non-nil snapshot.

- [ ] **Step 2: Run agent tests and verify RED**

Run:

```bash
go test ./internal/agent -run 'AutomaticReview|AutoReview' -count=1
```

Expected: failures because `BuildSessionOpts` has no feature identity and the
session builder reads only workspace defaults.

- [ ] **Step 3: Resolve feature policy at the session boundary**

Add `FeatureID string` to `BuildSessionOpts`. When
`opts.AutoReview.Enabled != nil`, retain the current snapshot-first path
without loading feature state. Otherwise:

1. read the workspace boolean;
2. when `FeatureID` is non-empty, load the current feature from
   `pr.FeatureStore`;
3. return a build error if the feature cannot be loaded;
4. resolve its mode against the workspace boolean;
5. populate the existing `AutoReviewSnapshot`.

Add a `buildSessionForFeature(f *feature.Feature) BuildSessionFunc` wrapper
that injects `f.ID`. Use it for every loop config created by `PhaseRunner`, and
set `FeatureID: f.ID` on direct `BuildSessionOpts` construction in interactive
and knowledge-base launches. Non-feature helper sessions leave `FeatureID`
empty and retain workspace behavior.

- [ ] **Step 4: Run agent tests and verify GREEN**

Run:

```bash
go test ./internal/agent -run 'AutomaticReview|AutoReview' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit session policy resolution**

```bash
git add internal/agent/phase.go internal/agent/autoreview_internal_test.go
git commit -m "Honor feature automatic review policy in new sessions"
```

---

### Task 5: Overview Visibility

**Files:**
- Modify: `internal/tui/api_app.go`
- Modify: `internal/tui/detail.go`
- Test: `internal/tui/api_app_test.go`
- Test: `internal/tui/detail_test.go`

**Interfaces:**
- Consumes: server `FeatureDetail.AutomaticReview`
- Consumes: transient `feature.Feature.AutomaticReviewEnabled` and
  `AutomaticReviewSource`
- Produces: Overview Info row `Auto mode  On (Feature|Global)`

- [ ] **Step 1: Write failing adapter and rendering tests**

Add adapter tests that apply server detail states and assert the transient
feature fields. Add `renderMetadataCompact` cases:

```go
{
	name:    "feature enabled",
	enabled: true,
	source:  feature.AutomaticReviewSourceFeature,
	want:    "Auto mode  On (Feature)",
},
{
	name:    "global enabled",
	enabled: true,
	source:  feature.AutomaticReviewSourceGlobal,
	want:    "Auto mode  On (Global)",
},
{
	name:    "disabled",
	enabled: false,
	notWant: "Auto mode",
},
```

- [ ] **Step 2: Run Overview tests and verify RED**

Run:

```bash
go test ./internal/tui -run 'Overview.*Auto|Metadata.*Auto|APIFeature.*Auto' -count=1
```

Expected: failures because detail adaptation and metadata rendering omit the
state.

- [ ] **Step 3: Adapt and render the effective state**

In `applyAPIFeatureDetail`, copy mode, enabled, and source from the server
state to the feature. In `renderMetadataCompact`, append:

```go
if f.AutomaticReviewEnabled {
	b.WriteString(LabelStyle.Render("Auto mode"))
	b.WriteString("  " + SuccessStyle.Render("On ("+titleCaseSource+")") + "\n")
}
```

Map only the two closed source values to `Feature` and `Global`; do not render
arbitrary server text.

- [ ] **Step 4: Run Overview tests and verify GREEN**

Run:

```bash
go test ./internal/tui -run 'Overview.*Auto|Metadata.*Auto|APIFeature.*Auto' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit Overview transparency**

```bash
git add internal/tui/api_app.go internal/tui/detail.go internal/tui/api_app_test.go internal/tui/detail_test.go
git commit -m "Make active automatic review visible in feature overviews"
```

---

### Task 6: Full Verification

**Files:**
- Review: all files changed since `8f48af15`

- [ ] **Step 1: Format and inspect generated/source diffs**

Run:

```bash
gofmt -w internal/feature/feature.go internal/feature/feature_test.go internal/feature/store_test.go internal/orchestrator/lifecycle_delegates.go internal/orchestrator/update_feature_config_test.go internal/server/mutation.go internal/server/read_model.go internal/server/read_model_test.go internal/server/client_test.go cmd/agentico/main.go internal/tui/editconfig.go internal/tui/api_app.go internal/tui/autoreview_editconfig_test.go internal/tui/api_app_test.go internal/agent/phase.go internal/agent/autoreview_internal_test.go internal/tui/detail.go internal/tui/detail_test.go
git diff --check
git status --short
```

Expected: no formatting errors or whitespace failures; only intended files are
modified.

- [ ] **Step 2: Run the Fast suite**

Run:

```bash
make test-fast
```

Expected: PASS.

- [ ] **Step 3: Run the E2E Go tier**

Run:

```bash
go test ./test/e2e/... -count=1 -race
```

Expected: PASS.

- [ ] **Step 4: Run static analysis and build**

Run:

```bash
go vet ./...
go build ./...
```

Expected: both commands exit 0.

- [ ] **Step 5: Review requirement coverage**

Confirm from fresh test output and the final diff:

- feature mode resolves `default|enabled|disabled` correctly;
- workspace behavior and reviewer-model ownership are unchanged;
- fresh sessions use the effective feature policy;
- crash resumes keep their snapshot;
- the feature editor persists the mode;
- the Overview shows active Auto mode and its source;
- existing feature/config files remain compatible.

- [ ] **Step 6: Commit any verification-only fixes**

Stage each corrected file explicitly and commit with a message explaining the
regression or compatibility risk the fix prevents. Do not create an empty
commit when no fixes were required.
