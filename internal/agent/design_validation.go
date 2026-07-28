// Copyright 2026 DoorDash, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

const defaultMaxDesignAttempts = 5

// DefaultMaxDesignAttempts is the initial autonomous Design revision budget.
const DefaultMaxDesignAttempts = defaultMaxDesignAttempts

// DesignLoopConfig configures the Design author/reviser and critic loop.
type DesignLoopConfig struct {
	PlanLoopConfig
}

// DesignLoopResult is the terminal result consumed by the orchestrator.
type DesignLoopResult struct {
	FinalStatus string
	Iterations  int
	LastError   string
}

func designRoleForAttempt(attempt int) Role {
	if attempt <= 1 {
		return RoleDesigner
	}
	return RoleDesignReviser
}

func designSpecForAttempt(attempt int) RoleSpec {
	if attempt <= 1 {
		return DesignerRoleSpec()
	}
	return DesignReviserRoleSpec()
}

func designValidators(manifestPath string) []validatorDomain {
	validators := []validatorDomain{{
		Name:     "Integrity",
		Template: "validate-design-integrity",
	}}
	if manifestPath != "" {
		validators = append(validators, validatorDomain{
			Name:     "Visual",
			Template: "validate-design-visual",
		})
	}
	return validators
}

// RunDesignValidationLoop creates a Design artifact, conditionally creates its
// mockup bundle through the companion skill, and iterates over two focused
// critics until approved or escalated.
func RunDesignValidationLoop(cfg DesignLoopConfig, sm ports.SessionManager) (result *DesignLoopResult, retErr error) {
	featureCtx := observe.SpanContextForFeature(
		cfg.Feature.ID,
		cfg.Feature.TraceID,
		cfg.Feature.Name,
		cfg.Feature.FeatureSpanID,
	).WithRun(cfg.Feature.ActiveRun)
	cfg.PhaseSpanCtx = featureCtx.Child()
	cfg.PlanLoopConfig.PhaseSpanCtx = cfg.PhaseSpanCtx
	phaseStart := time.Now()
	cfg.Observer.PhaseStarted(cfg.PhaseSpanCtx, feature.PhaseDesign.String())
	defer func() {
		var phaseErr error
		if retErr != nil {
			phaseErr = retErr
		} else if result != nil && result.FinalStatus != "approved" {
			phaseErr = errors.New(result.FinalStatus)
		}
		cfg.Observer.PhaseCompleted(cfg.PhaseSpanCtx, feature.PhaseDesign.String(), time.Since(phaseStart), phaseErr)
	}()

	artifactDir := filepath.Join(
		ActiveRunDir(cfg.StateDir, cfg.Feature),
		cfg.Feature.RefactorPrefix(),
		feature.PhaseDesign.DirName(),
	)
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating Design artifact directory: %w", err)
	}

	maxAttempts := cfg.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = defaultMaxDesignAttempts
	}

	startAttempt := LatestCompletedPlanAttempt(artifactDir)
	resumeValidation := false
	var criticFeedback string
	feedbackAttempt, feedback := latestPlanRevisionFeedbackAttempt(artifactDir)
	if feedbackAttempt > startAttempt {
		startAttempt = feedbackAttempt
		criticFeedback = feedback
	} else if startAttempt > 0 {
		meta, err := readPlanAttemptMeta(artifactDir, startAttempt)
		if err == nil {
			switch meta.ReviewStatus {
			case agentStatusApproved:
				return &DesignLoopResult{FinalStatus: "approved", Iterations: startAttempt}, nil
			case "VALIDATION_PENDING":
				resumeValidation = true
			case agentStatusChangesRequested:
				feedbackPath := filepath.Join(artifactDir, fmt.Sprintf("attempt-%02d", startAttempt), "validation-feedback.md")
				if data, readErr := os.ReadFile(feedbackPath); readErr == nil {
					criticFeedback = strings.TrimSpace(string(data))
				}
			}
		}
	}

	loopStart := startAttempt + 1
	if resumeValidation {
		loopStart = startAttempt
	}
	stalls := loadAxisStallState(artifactDir)

designAttemptLoop:
	for attempt := loopStart; attempt <= maxAttempts; attempt++ {
		if isFeatureInterrupted(cfg.FeatureStore, cfg.Feature.ID) {
			return &DesignLoopResult{FinalStatus: "interrupted", Iterations: attempt - 1}, nil
		}

		attemptDir := filepath.Join(artifactDir, fmt.Sprintf("attempt-%02d", attempt))
		if err := os.MkdirAll(attemptDir, 0o755); err != nil {
			return nil, fmt.Errorf("creating Design attempt directory: %w", err)
		}
		setDesignIteration(cfg, attempt)

		if resumeValidation {
			resumeValidation = false
		} else {
			designPath := resolveDesignArtifactPath(cfg.FeatureStore, cfg.Feature.ID, artifactDir)
			manifestPath := existingMockupManifestPath(artifactDir)
			prompt := BuildDesignPrompt(
				cfg.Feature,
				cfg.SkillsDir,
				cfg.GuidelinesDir,
				cfg.ResearchArtifactPath,
				cfg.QAFilePaths,
				cfg.KBInfos...,
			)
			if attempt > 1 {
				prompt = BuildDesignRevisionPrompt(
					designPath,
					manifestPath,
					criticFeedback,
					attempt,
				)
			}

			spec := designSpecForAttempt(attempt)
			systemPrompt := BuildRoleSystemPrompt(BuildRoleSystemPromptInput{
				Spec:          spec,
				IterationDir:  attemptDir,
				SkillsDir:     cfg.SkillsDir,
				GuidelinesDir: cfg.GuidelinesDir,
				KBInfos:       cfg.KBInfos,
				AskingClause:  cfg.AskingClause,
			})
			addDirs := cfg.AdditionalDirs
			if len(addDirs) == 0 {
				addDirs = []string{ActiveRunDir(cfg.StateDir, cfg.Feature)}
			}
			sessionAttempt := nextPlanSessionAttempt(artifactDir, attempt)
			for {
				RemovePhaseComplete(attemptDir)
				cmd, env, sessOpts, err := cfg.BuildSession(BuildSessionOpts{
					Model:                          cfg.Feature.Models.Planning,
					Prompt:                         prompt,
					SystemPrompt:                   systemPrompt,
					AdditionalDirs:                 addDirs,
					PIDDir:                         filepath.Join(cfg.StateDir, cfg.Feature.ID),
					PermHandler:                    permHandlerFor(cfg.DangerouslySkipPermissions, cfg.PermissionCache, cfg.RepoName),
					WorkDir:                        cfg.WorkDir,
					EffortLevel:                    planEffortLevel(cfg.PlanLoopConfig),
					Phase:                          feature.PhaseDesign,
					SystemPromptHasUsefulResources: true,
					MarkerPath:                     filepath.Join(attemptDir, PhaseCompleteFile),
				})
				if err != nil {
					return nil, fmt.Errorf("building Design session (attempt %d): %w", attempt, err)
				}
				sessOpts = enableTruncatedTurnAutoResume(sessOpts)
				if cfg.EffectiveEffort != "" {
					sessOpts.EffectiveEffort = cfg.EffectiveEffort
					sessOpts.EffortSource = cfg.EffortSource
				}
				if cfg.FinishOrViolateNudge {
					sessOpts.TurnMode = ports.TurnModeInteractive
				}
				WriteDebugPrompts(attemptDir, sessOpts.DebugSystemPrompt, prompt)
				sessOpts.PermCacheScope = cfg.RepoName

				sessionID := planAttemptSessionID(fmt.Sprintf("%s-design-%02d", cfg.Feature.ID, attempt), sessionAttempt)
				sessionCtx := cfg.PhaseSpanCtx.Child()
				if attempt == 1 {
					sessOpts.AskUserAutoPick = askUserAutoPickConfig(
						cfg.FeatureStore,
						cfg.Observer,
						cfg.Feature,
						ports.AskUserAutoPickPurposeDesign,
						sessionCtx,
						sessionID,
						cfg.RepoName,
						0,
					)
				}
				startSession := resolveSessionStartFunc(cfg.SessionStartFunc, sm)
				sess, err := startSession(sessionID, cfg.Feature.ID, feature.PhaseDesign, cmd, cfg.WorkDir, env, sessOpts)
				if err != nil {
					if errors.Is(err, ports.ErrSessionShuttingDown) {
						return &DesignLoopResult{FinalStatus: "interrupted", Iterations: attempt - 1}, nil
					}
					return nil, fmt.Errorf("starting Design session (attempt %d): %w", attempt, err)
				}

				providerName := ""
				if sessOpts != nil {
					providerName = sessOpts.ProviderName
				}
				cfg.Observer.SessionStarted(
					sessionCtx,
					feature.PhaseDesign.String(),
					sessionID,
					providerName,
					cfg.Feature.Models.Planning,
					cfg.RepoName,
					string(cfg.EffectiveEffort),
					string(cfg.EffortSource),
				)
				sessionStart := time.Now()
				logPath := filepath.Join(attemptDir, "output.txt")
				if logFile, createErr := os.Create(logPath); createErr == nil {
					sess.SetLogFile(logFile)
				}

				agentStatus := waitForStatusDetailed(sess, sm, sessionID, waitForStatusOptions{
					ReadyCheck: func() bool {
						if HasPhaseComplete(attemptDir) {
							sess.SetHasUnansweredQuestion(false)
							return true
						}
						return false
					},
					FinishOrViolateNudge: cfg.FinishOrViolateNudge,
					MissingArtifacts:     []string{"design markdown"},
				}).Status
				cost := ExtractSessionCost(sess)
				cfg.Observer.SessionEnded(
					sessionCtx,
					feature.PhaseDesign.String(),
					sessionID,
					cfg.RepoName,
					toSessionUsage(cost),
					time.Since(sessionStart),
					sessionErrFromAgentStatus(agentStatus),
				)
				_ = os.WriteFile(logPath, []byte(sess.MessageLog().Text()), 0o644)
				recordDesignSessionCost(cfg, sessionID, cost)

				if agentStatus == agentStatusSuccess {
					if attempt == 1 {
						if _, err := WriteQAFile(sess.QALog(), artifactDir); err != nil {
							return nil, fmt.Errorf("writing Design Q&A: %w", err)
						}
					}
					meta := PlanAttemptMeta{
						Attempt:      attempt,
						AgentStatus:  agentStatusSuccess,
						ReviewStatus: "VALIDATION_PENDING",
					}
					if sessionAttempt > 1 {
						meta.SessionAttempt = sessionAttempt
					}
					_ = WritePlanAttemptMeta(artifactDir, meta)
					break
				}
				if agentStatus == "FAILED" && sm != nil && sm.IsShuttingDown() {
					return &DesignLoopResult{FinalStatus: "interrupted", Iterations: attempt - 1}, nil
				}
				if agentStatus == agentStatusMissingMarker {
					criticFeedback = formatPlanContractViolationFeedback(spec.Role, missingPhaseCompleteViolations())
					_ = writeDesignAttemptFeedback(artifactDir, attempt, criticFeedback)
					if attempt >= maxAttempts {
						return &DesignLoopResult{
							FinalStatus: BoundedHelperStatusProtocolViolation,
							Iterations:  attempt,
							LastError:   criticFeedback,
						}, nil
					}
					continue designAttemptLoop
				}
				_ = WritePlanAttemptMeta(artifactDir, PlanAttemptMeta{
					Attempt:        attempt,
					SessionAttempt: sessionAttempt,
					AgentStatus:    "FAILED",
				})
				if shouldRetryPlanInfrastructureSession(agentStatus, sess, cost, time.Since(sessionStart), sessionAttempt) {
					sessionAttempt++
					continue
				}
				return &DesignLoopResult{
					FinalStatus: "failed",
					Iterations:  attempt,
					LastError:   "Design session did not complete successfully",
				}, nil
			}
		}

		role := designRoleForAttempt(attempt)
		outcome, violations, err := Validate(feature.PhaseDesign, role, attemptDir)
		if err != nil {
			return nil, fmt.Errorf("validating Design contract (attempt %d): %w", attempt, err)
		}
		if !outcome.OK {
			criticFeedback = formatPlanContractViolationFeedback(role, violations)
			_ = writeDesignAttemptFeedback(artifactDir, attempt, criticFeedback)
			if attempt >= maxAttempts {
				return &DesignLoopResult{
					FinalStatus: BoundedHelperStatusProtocolViolation,
					Iterations:  attempt,
					LastError:   criticFeedback,
				}, nil
			}
			continue
		}

		designPath := resolveDesignArtifactPath(cfg.FeatureStore, cfg.Feature.ID, artifactDir)
		if designPath == "" {
			return nil, fmt.Errorf("Design attempt %d produced no design markdown", attempt)
		}
		manifestPath := existingMockupManifestPath(artifactDir)
		validators := designValidators(manifestPath)
		setValidatingDesign(cfg, true)
		results, reviewStatus, feedback, reviewErr := runValidatorSet(
			cfg.PlanLoopConfig,
			sm,
			attempt,
			attemptDir,
			designPath,
			validators,
			validationArtifactDesign,
			planValidationExtras{MockupManifestPath: manifestPath},
		)
		setValidatingDesign(cfg, false)

		if isFeatureInterrupted(cfg.FeatureStore, cfg.Feature.ID) {
			return &DesignLoopResult{FinalStatus: "interrupted", Iterations: attempt}, nil
		}
		if reviewErr != nil {
			if isProtocolViolationError(reviewErr) {
				criticFeedback = feedback
				if strings.TrimSpace(criticFeedback) == "" {
					criticFeedback = fmt.Sprintf("Design validation failed: %v", reviewErr)
				}
				_ = writeDesignAttemptFeedback(artifactDir, attempt, criticFeedback)
				if attempt >= maxAttempts {
					return &DesignLoopResult{
						FinalStatus: BoundedHelperStatusProtocolViolation,
						Iterations:  attempt,
						LastError:   reviewErr.Error(),
					}, nil
				}
				continue
			}
			_ = WritePlanAttemptMeta(artifactDir, PlanAttemptMeta{
				Attempt:      attempt,
				AgentStatus:  agentStatusSuccess,
				ReviewStatus: "FAILED",
			})
			return &DesignLoopResult{
				FinalStatus: "failed",
				Iterations:  attempt,
				LastError:   reviewErr.Error(),
			}, nil
		}

		stallResults := make([]ValidatorResult, 0, len(results))
		for _, validatorResult := range results {
			if !strings.EqualFold(validatorResult.Domain, "Visual") {
				stallResults = append(stallResults, validatorResult)
			}
		}
		stalled, axis, count, verdicts, digests := stalls.observe(attempt, designPath, stallResults)
		for _, validatorResult := range results {
			axisName := strings.ToLower(validatorResult.Domain)
			if _, tracked := verdicts[axisName]; !tracked {
				verdicts[axisName] = validatorResult.Status.String()
			}
		}
		meta := PlanAttemptMeta{
			Attempt:      attempt,
			AgentStatus:  agentStatusSuccess,
			ReviewStatus: reviewStatus.String(),
			AxisVerdicts: verdicts,
			AxisDigests:  digests,
		}
		_ = WritePlanAttemptMeta(artifactDir, meta)

		switch reviewStatus {
		case ReviewApproved:
			if err := recordDesignArtifacts(cfg, designPath, manifestPath); err != nil {
				return nil, err
			}
			return &DesignLoopResult{FinalStatus: "approved", Iterations: attempt}, nil
		case ReviewChangesRequested:
			criticFeedback = feedback
			_ = os.WriteFile(filepath.Join(attemptDir, "validation-feedback.md"), []byte(feedback), 0o644)
			if stalled {
				if err := recordDesignArtifacts(cfg, designPath, manifestPath); err != nil {
					return nil, err
				}
				return &DesignLoopResult{
					FinalStatus: "needs_human_review",
					Iterations:  attempt,
					LastError:   fmt.Sprintf("%s critic stalled for %d attempts", axis, count),
				}, nil
			}
		default:
			criticFeedback = "Design validation produced no clear result."
			_ = os.WriteFile(filepath.Join(attemptDir, "validation-feedback.md"), []byte(criticFeedback), 0o644)
		}
	}

	designPath := resolveDesignArtifactPath(cfg.FeatureStore, cfg.Feature.ID, artifactDir)
	if designPath != "" {
		if err := recordDesignArtifacts(cfg, designPath, existingMockupManifestPath(artifactDir)); err != nil {
			return nil, err
		}
	}
	return &DesignLoopResult{
		FinalStatus: "needs_human_review",
		Iterations:  maxAttempts,
		LastError:   fmt.Sprintf("Design not approved after %d attempts", maxAttempts),
	}, nil
}

func existingMockupManifestPath(artifactDir string) string {
	path := filepath.Join(artifactDir, "mockups", "manifest.yaml")
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() == 0 {
		return ""
	}
	return path
}

func resolveDesignArtifactPath(store ports.FeatureStore, featureID, artifactDir string) string {
	if store != nil {
		if f, err := store.Load(featureID); err == nil && f.Artifacts != nil {
			if path := f.Artifacts[feature.DesignArtifactKey]; path != "" {
				if filepath.IsAbs(path) {
					if _, statErr := os.Stat(path); statErr == nil {
						return path
					}
				}
			}
		}
	}
	canonical := filepath.Join(artifactDir, "design.md")
	if info, err := os.Stat(canonical); err == nil && info.Mode().IsRegular() {
		return canonical
	}
	var bestPath string
	var bestModTime int64
	matches, _ := filepath.Glob(filepath.Join(artifactDir, "*.md"))
	for _, match := range matches {
		if IsArtifactExcluded(filepath.Base(match)) {
			continue
		}
		info, err := os.Stat(match)
		if err != nil || info.IsDir() {
			continue
		}
		if modified := info.ModTime().UnixNano(); bestPath == "" || modified > bestModTime {
			bestPath = match
			bestModTime = modified
		}
	}
	return bestPath
}

func writeDesignAttemptFeedback(artifactDir string, attempt int, feedback string) error {
	attemptDir := filepath.Join(artifactDir, fmt.Sprintf("attempt-%02d", attempt))
	if err := os.WriteFile(filepath.Join(attemptDir, "validation-feedback.md"), []byte(feedback), 0o644); err != nil {
		return err
	}
	return WritePlanAttemptMeta(artifactDir, PlanAttemptMeta{
		Attempt:      attempt,
		AgentStatus:  agentStatusSuccess,
		ReviewStatus: agentStatusChangesRequested,
	})
}

func setDesignIteration(cfg DesignLoopConfig, attempt int) {
	if cfg.FeatureStore == nil {
		return
	}
	_ = cfg.FeatureStore.Modify(cfg.Feature.ID, func(f *feature.Feature) error {
		f.DesignIteration = attempt
		return nil
	})
}

func setValidatingDesign(cfg DesignLoopConfig, validating bool) {
	if cfg.FeatureStore == nil {
		return
	}
	_ = cfg.FeatureStore.Modify(cfg.Feature.ID, func(f *feature.Feature) error {
		f.ValidatingDesign = validating
		return nil
	})
}

func recordDesignSessionCost(cfg DesignLoopConfig, sessionID string, cost SessionCost) {
	if cfg.FeatureStore == nil || cost.TotalCostUSD <= 0 {
		return
	}
	_ = cfg.FeatureStore.Modify(cfg.Feature.ID, func(f *feature.Feature) error {
		f.RecordSessionCost(feature.SessionCostRecord{
			SessionID:     sessionID,
			PhaseKey:      feature.PhaseDesign.DirName(),
			ObserverPhase: feature.PhaseDesign.String(),
			RepoName:      cfg.RepoName,
			CostUSD:       cost.TotalCostUSD,
		})
		return nil
	})
}

func recordDesignArtifacts(cfg DesignLoopConfig, designPath, manifestPath string) error {
	if cfg.FeatureStore == nil {
		return nil
	}
	return cfg.FeatureStore.Modify(cfg.Feature.ID, func(f *feature.Feature) error {
		if f.Artifacts == nil {
			f.Artifacts = make(map[string]string)
		}
		f.Artifacts[feature.DesignArtifactKey] = designPath
		if manifestPath != "" {
			f.Artifacts[feature.DesignMockupsArtifactKey] = manifestPath
		} else {
			delete(f.Artifacts, feature.DesignMockupsArtifactKey)
		}
		return nil
	})
}
