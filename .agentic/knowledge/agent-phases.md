# Agent Phases

## PhaseRunner

The `PhaseRunner` (`internal/agent/phase.go:60-74`) is the central orchestrator that launches agent sessions for each phase:

| Method | Description |
|--------|-------------|
| `RunKnowledgeBase(f)` | Build/update repo knowledge base |
| `RunResearchFromQuestions(f, questionsPath, kbInfos...)` | Launch research session driven by Inquire's questions file |
| `RunRoadmapPlanning(f, brainstormPath, kbInfos...)` | Launch roadmap planning session |
| `RunPhasePlanning(f, roadmapPath, phase, kbInfos...)` | Launch phase planning session |
| `RunImplementation(f, planPath, kbPaths...)` | Implementation with review loop → `chan *LoopResult` |
| `GetPhaseOutput(featureID, phase)` | Read phase output file |

## Completion Protocol

Agents signal phase completion by writing a `phase_complete` file to their output directory. The system injects instructions via `--append-system-prompt` telling agents:
- The output directory path
- To write `phase_complete` when finished
- Exit criteria and verification steps

## Implementation Loop

`RunImplementationLoop` (`internal/agent/implement.go:58`) orchestrates the implement→review cycle:

1. Build implementation prompt with plan, exit criteria, verification, feedback
2. Launch interactive Claude session
3. Wait for `phase_complete` or session end
4. Run review gate (non-interactive `--print` mode session)
5. Parse review output: `APPROVED` → done, `CHANGES_REQUESTED` → loop with feedback
6. Track iterations, consecutive failures, no-progress detection
7. Return `LoopResult` with final status

### LoopResult

| Field | Description |
|-------|-------------|
| `FinalStatus` | `review_passed` / `max_iterations_reached` / `failed` / `interrupted` / `need_input` |
| `Iterations` | Number of iterations executed |
| `LastError` | Last error encountered (if any) |

### ImplementConfig

Configuration for the implementation loop:

| Field | Description |
|-------|-------------|
| `Feature` | The feature being implemented |
| `PlanPath` | Path to the approved plan |
| `StateDir` | Feature state directory |
| `SessionManager` | Session manager instance |
| `CommandBuilder` / `InteractiveCommandBuilder` | Functions to build CLI commands |
| `SkipPermissions` | Whether to skip permission prompts |
| `KBPaths` | Knowledge base file paths |
| `MaxIterations` | Loop iteration limit |
| `MaxConsecutiveFailures` | Failure threshold |
| `MaxConsecutiveNoProgress` | No-progress threshold |

## Plan Validation (Two-Tier)

Planning uses a two-tier model: roadmap planning and phase planning, each with its own validation loop. Validation always runs the specialized multi-validator pipeline — there is no single-critic path.

**Roadmap planning** (`RunRoadmapPlanningLoop`): generates a high-level roadmap breaking the feature into phases, then validates via `runRoadmapMultiValidatorPlanValidation` (axes selected by `roadmapValidatorsForRisk`: Architecture + Scope, plus Security + Performance for high-risk). If any axis returns `CHANGES_REQUESTED`, revises using the `revise-roadmap` skill and re-validates up to `MaxPlanIterations`.

**Phase planning** (`RunPhasePlanningLoop`): for each phase in the roadmap, generates a detailed implementation plan, then validates via `runPhasePlanMultiValidatorValidation` (axes selected by `phasePlanValidatorsForRisk`: Structural + Grounding + Scope, plus Security + Performance + Testing for high-risk). If any axis returns `CHANGES_REQUESTED`, revises using the `revise-phase-plan` skill and re-validates up to `MaxPlanIterations`.

Both return `PlanLoopResult` with status and iterations.

## Command Builders

| Function | Description |
|----------|-------------|
| `BuildClaudeCommand(model, prompt, skipPerms)` | Non-interactive `--print` mode command |
| `BuildInteractiveClaudeCommand(model, prompt, systemPrompt, disallowed, skipPerms, dirs...)` | Interactive session with system prompt injection |
| `BuildChatCommand(model, prompt, systemPrompt, resumeID)` | Chat mode command |
| `DetectAvailableCLIs()` | Check for `claude` and `codex` on PATH |

### Claude CLI Flags Used

| Flag | Purpose |
|------|---------|
| `--model` | Model selection |
| `--print` | Non-interactive mode |
| `--output-format stream-json` | JSON SDK protocol output |
| `--append-system-prompt` | System prompt injection |
| `--disallowed-tools` | Restrict tool access |
| `--dangerously-skip-permissions` | Skip permission prompts |
| `--resume <sessionID>` | Resume a previous session |
| `--add-dir` | Additional directory access |

## Prompt Builders

| Function | Description |
|----------|-------------|
| `BuildResearchFromQuestionsPrompt(f, skillsDir, questionsPath, kbInfos...)` | Research prompt driven by Inquire-produced questions (no description leak) |
| `BuildRoadmapPrompt(f, brainstormPath, kbInfos...)` | Roadmap planning prompt |
| `BuildRoadmapRevisionPrompt(f, feedback, kbInfos...)` | Roadmap revision prompt |
| `BuildPhasePlanPrompt(f, roadmapPath, phase, kbInfos...)` | Phase planning prompt |
| `BuildPhasePlanRevisionPrompt(f, phase, feedback, kbInfos...)` | Phase plan revision prompt |
| `BuildImplementPrompt(planPath, exitCriteria, verification, progress, feedback, helpAnswers, iteration, kbPaths...)` | Implementation prompt |
| `BuildReviewPrompt(plan, exitCriteria, verification, progress, iterDir, iteration, verificationSteps)` | Review gate prompt |
| `BuildPRDescriptionPrompt(diff, plan)` | PR description generation |
| `BuildSummaryPrompt(name, description)` | Feature summary generation |

## Repo Classifier

The CNB (Complement Naive Bayes) classifier (`internal/agent/classifier.go`) suggests repos for a feature:

| Component | Description |
|-----------|-------------|
| `CNBClassifier` | Fits on repo features (docs, deps, dirs, extensions), predicts matches |
| `ClassifierIndex` | Wraps classifier with repo-level indexing |
| `RepoSuggester` | Combines classification + `@repo` mentions in description |
| `SelectByThreshold` | Select repos above a confidence threshold |

## Review Parsing

| Component | Description |
|-----------|-------------|
| `ReviewStatus` enum | `ReviewApproved` / `ReviewChangesRequested` / `ReviewFailed` |
| `ParseReviewFeedback(path)` | Parse the structured `## Findings` / `## Suggestions` / `## Verdict` handoff file produced by review and validator helpers |
| `VerificationStep` | Verification command and its result |

## Other Agent Components

| Component | File | Description |
|-----------|------|-------------|
| `RunDescriptionGeneration` | `describe.go` | Generate PR title/body via Claude |
| `RunSummaryGeneration` | `summarize.go` | Generate feature summary |
| `CheckCLIVersion` | `version.go` | Verify claude CLI version ≥ 1.0.0 |
| `RepoFeatures` | `features.go` | Extract repo features for classification |
| `KBStateDir` / `KBLockPath` | KB files | Knowledge base directory/lock management |
