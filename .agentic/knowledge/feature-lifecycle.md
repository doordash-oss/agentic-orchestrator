# Feature Lifecycle

## Feature Type

The `Feature` struct (`internal/feature/feature.go:244`) is the central domain entity, persisted as YAML:

| Field | Type | Description |
|-------|------|-------------|
| `ID` | `string` | Random 16-byte hex string |
| `Name` | `string` | Human-readable name |
| `Slug` | `string` | URL-safe slug (lowercase, alphanumeric + hyphens) |
| `Description` | `string` | Feature description |
| `Summary` | `string` | AI-generated summary |
| `Status` | `Status` | Current lifecycle status (enum) |
| `CurrentPhase` | `Phase` | Current execution phase |
| `CurrentIteration` | `int` | Implementation iteration counter |
| `Repos` | `[]FeatureRepo` | Target repositories with worktree paths |
| `Models` | `ModelConfig` | Per-phase model selection |
| `ExitCriteria` | `string` | Success criteria for implementation |
| `Inquireness` | `Inquireness` | How much the agent should ask questions |
| `Verification` | `map[string]string` | Per-repo verification commands |
| `DependsOn` | `[]string` | Feature IDs this depends on |
| `ParentID` | `string` | Parent feature ID (for split features) |
| `Children` | `[]string` | Child feature IDs (for split features) |
| `PermissionsQueue` | `[]PermissionRequest` | Pending permission approvals |
| `HelpQueue` | `[]HelpRequest` | Pending questions from agents |
| `Artifacts` | `map[string]string` | Phase output paths |
| `PRURL` | `string` | GitHub PR URL after publishing |
| `PhaseTimings` | `map[string]time.Duration` | Elapsed time per phase |
| `PhaseCosts` | `map[string]float64` | USD cost per phase |
| `RiskLevel` | `RiskLevel` | Blast radius classification: `low`, `medium`, `high` |
| `ResumeSessionID` | `string` | Claude CLI session ID for `--resume` |

## Phase Enum

Defined at `internal/feature/feature.go:11-20`:

| Phase | LogicalOrder | DirName |
|-------|-------------|---------|
| `PhaseKnowledgeBase` | 0 | `knowledgebase` |
| `PhaseResearch` | 1 | `research` |
| `PhasePlan` | 2 | `plan` |
| `PhaseImplement` | 3 | `implement` |
| `PhasePublish` | 4 | `publish` |
| `PhaseReview` | 5 | `review` |

## Status Enum

15 statuses defined at `internal/feature/feature.go:100-118`:

| Status | Description |
|--------|-------------|
| `Created` | Initial state, feature just created |
| `BuildingKB` | Knowledge base generation in progress |
| `Researching` | Research phase running |
| `PlanReady` | Research done, ready for planning |
| `Planning` | Planning phase running |
| `ImplementReady` | Plan approved, ready for implementation |
| `Implementing` | Implementation phase running |
| `ReviewPassed` | Code review passed |
| `PRReady` | Ready for PR creation |
| `Published` | PR created on GitHub |
| `Done` | Feature complete |
| `Failed` | Phase failed |
| `Interrupted` | Session interrupted |
| `DependencyBlocked` | Waiting on dependent features |
| `SplitParent` | Parent feature that was split into children |

## Valid Transitions

Defined at `internal/feature/feature.go:292-308`:

```
Created → BuildingKB → Created → Researching → PlanReady → Planning
    → ImplementReady → Implementing → ReviewPassed → PRReady → Published → Done

Planning → SplitParent → Done (when all children complete)

Any running → Failed / Interrupted
Failed → Created / Researching / ImplementReady / PRReady (retry paths)
Interrupted → Researching / Planning / Implementing (resume paths)
ImplementReady ↔ DependencyBlocked (for sequential dependencies)
```

## Risk Classification

Features have an optional `RiskLevel` field (`low`, `medium`, `high`) that classifies the blast radius of a change. This is part of the advanced review gate:

- **Low**: Internal refactors, docs, config, isolated bug fixes. Only architecture + testing validators run.
- **Medium**: New features in existing modules, non-breaking API additions. All 4 validators run.
- **High**: New services, breaking API changes, cross-system integration, auth/payment changes. All 4 validators run; senior review recommended.

Risk level is set during feature creation (wizard step) and stored in `feature.yaml`.

## Specialized Plan Validation

Plan validation always runs the specialized multi-validator pipeline. Roadmap and per-phase plan artifacts are each evaluated by a set of axis-specific validators (selected by `roadmapValidatorsForRisk` / `phasePlanValidatorsForRisk` based on risk level). Axes:

| Validator | Template | Focus |
|-----------|----------|-------|
| Architecture (roadmap) | `validate-roadmap-architecture.md` | Architectural soundness at the strategic / cross-phase level |
| Scope (roadmap) | `validate-roadmap-scope.md` | Phase decomposition, scope coverage |
| Structural (phase plan) | `validate-phase-plan-structural.md` | Phase plan format, exit criteria, verifiability |
| Grounding (phase plan) | `validate-phase-plan-grounding.md` | Symbol existence, prior-phase awareness |
| Scope (phase plan) | `validate-phase-plan-scope.md` | Per-phase scope adherence |
| Security (high-risk add-on) | `validate-plan-security.md` | Auth, injection prevention, data protection |
| Performance (high-risk add-on) | `validate-plan-performance.md` | Scalability, query efficiency, failure modes |
| Testing (high-risk phase plan add-on) | `validate-plan-testing.md` | Coverage adequacy, edge cases, regression protection |

Each validator produces an independent APPROVED/CHANGES_REQUESTED verdict in its `validation-<axis>-feedback.md` handoff file and may include a `## Sticky Approval` block whose `frozen_sections` survive across revise attempts. If any validator requests changes, the combined feedback is passed to the plan revision session.

See `docs/advanced-review-gate/` for the full methodology documentation.

## Store

The `Store` type wraps filesystem persistence:

- **Base directory**: `~/.agentic-workflow/features/`
- **Per feature**: `<baseDir>/<featureID>/feature.yaml`
- **Operations**: `Save`, `Load`, `Delete`, `List`, `Modify` (atomic read-modify-write)

## Manager

The `Manager` (`internal/feature/manager.go:16-20`) orchestrates feature lifecycle:

### Creation
| Method | Description |
|--------|-------------|
| `Create(...)` | Create feature with worktree, repos, verification |
| `CreateLinked(...)` | Create chained dependent features (one per repo) |
| `CreateIndependent(...)` | Create independent features (one per repo) |
| `CreateFromSplit(...)` | Split parent into child subfeatures (transactional) |

### Phase Transitions
| Method | Description |
|--------|-------------|
| `StartResearch/Planning/Implementation/KnowledgeBase` | Start a phase |
| `CompleteResearch/Planning/Implementation/KnowledgeBase` | Complete a phase |
| `MarkPRReady/Published/Failed` | Terminal state transitions |

### Lifecycle Management
| Method | Description |
|--------|-------------|
| `RestartFromBeginning` | Reset to Created (cascade-deletes children) |
| `Delete` | Delete feature and cascade-delete children |
| `CanAdvance` | Check if dependencies are satisfied |
| `CheckAndCompleteParent` | Auto-complete parent when all children Done |
| `FindUnblockedDependents` | Find blocked children that can now proceed |
| `Unblock` | Transition DependencyBlocked → ImplementReady |

### Worktree
| Method | Description |
|--------|-------------|
| `EnsureWorktree` | Create worktree if missing |
| `RecreateWorktree` | Delete and recreate worktree |
| `CleanWorktree` | Reset worktree to base branch |

## Dependencies

Defined in `internal/feature/dependency.go`:

| Function | Description |
|----------|-------------|
| `ValidateDependencies(features)` | Cycle detection via DFS |
| `TopologicalSort(features)` | Kahn's algorithm topological sort |
| `BlockedFeatures(features)` | Find features blocked by incomplete dependencies |

## Feature Splitting

Defined in `internal/feature/split.go`:

- `SplitSpec` / `SubfeatureSpec` — YAML-parseable split specification
- `ParseSplitSpec(data)` — Parse and validate split YAML
- `validateSplitCycles(spec)` — Detect dependency cycles in split spec
- `IsSplitArtifact(filename)` — Check if file is a split plan artifact

Split creates child features from a parent:
1. Parent transitions to `SplitParent` status
2. Children inherit repos, models, and other settings
3. Children can have inter-dependencies
4. Parent auto-completes when all children reach `Done`
5. Cascade delete: deleting parent deletes all children
