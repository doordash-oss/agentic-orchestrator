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
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

const (
	BlockingLoopStatusSuccess           = "success"
	BlockingLoopStatusSafetyRail        = "safety_rail"
	BlockingLoopStatusProtocolViolation = "protocol_violation"
	BlockingLoopStatusInterrupted       = "interrupted"
	BlockingLoopStatusFailed            = "failed"
)

// BlockingLoopPromptInput carries per-iteration paths into a loop prompt
// builder.
type BlockingLoopPromptInput struct {
	Iteration             int
	IterationDir          string
	HandoffPath           string
	SeededCanonicalPath   string // "" for persistent-deliverable phases
	SeededQAPath          string
	PreviousCanonicalPath string // "" for persistent-deliverable phases
	// ResumeContext is the output of ResumeStrategy.Build for iterations > 1
	// (pending unit IDs + decisions-so-far + a deliverable path pointer). It is
	// "" on iteration 1 and for loops without a ResumeStrategy. Phase BuildPrompt
	// closures embed it verbatim when non-empty.
	ResumeContext string
}

// BlockingLoopRunInput is passed to a test seam or production session runner.
type BlockingLoopRunInput struct {
	Iteration    int
	IterationDir string
	SessionID    string
	Prompt       string
}

// BlockingLoopRunResult is the normalized result of one agent session.
type BlockingLoopRunResult struct {
	Status   string
	Handoff  contextSnapshot
	Cost     SessionCost
	ExitCode int
	QALog    []ports.QAPair
}

// BlockingLoopConfig parameterizes a fresh-session blocking phase loop.
type BlockingLoopConfig struct {
	Label        string
	Feature      *feature.Feature
	FeatureID    string
	FeatureStore ports.FeatureStore
	Phase        feature.Phase
	Role         Role
	Spec         RoleSpec
	ArtifactDir  string
	StateDir     string

	Model          string
	WorkDir        string
	AdditionalDirs []string
	AgentNames     []string
	EffortLevel    llm.EffortLevel

	SkillsDir     string
	GuidelinesDir string
	KBInfos       []KBInfo
	AskingClause  string

	DangerouslySkipPermissions bool
	PermissionCache            *permission.Cache
	BuildSession               BuildSessionFunc
	Observer                   *observe.Observer

	HandoffFilename   string
	ParseHandoff      func(string) (*ParsedHelperHandoff, error)
	Fingerprint       func(string) (string, error)
	CanonicalSelector func(string) (string, error)

	InitialPrompt string
	BuildPrompt   func(BlockingLoopPromptInput) (string, error)

	MaxConsecNoProgress         int
	MaxConsecFailures           int
	MaxConsecProtocolViolations int

	// ProgressStrategy, when non-nil, replaces fingerprint-of-prose progress
	// detection with net-pending-unit reduction parsed from the handoff's
	// `## Ledger` block. It also drives auto-completion: a CONTINUE handoff that
	// reports zero pending units is overridden to COMPLETE (the engine, not the
	// agent, is the authority on termination). When nil, the engine falls back
	// to Fingerprint.
	ProgressStrategy ProgressStrategy

	// ResumeStrategy, when non-nil, builds BlockingLoopPromptInput.ResumeContext
	// for iterations > 1 — the compact pending-IDs + decisions-so-far + a
	// deliverable path pointer. When nil, no resume context is injected.
	ResumeStrategy ResumeStrategy

	// PersistentDeliverablePath, when non-empty, declares the deliverable is a
	// single file edited in place across iterations (like InPlaceCanonical).
	// prepareBlockingLoopIteration will NOT copy it forward into each iteration
	// dir — this is what eliminates the re-read-the-whole-draft context blowup.
	PersistentDeliverablePath string

	AccumulateQALog  bool
	InPlaceCanonical bool

	SessionIDBase string
	TelemetryRole string
	RepoName      string

	RunSession func(context.Context, BlockingLoopRunInput) (BlockingLoopRunResult, error)
}

// BlockingLoopResult is the terminal outcome of a blocking phase loop.
type BlockingLoopResult struct {
	FinalStatus          string
	Iterations           int
	LastError            string
	TerminalIterationDir string
	CanonicalPath        string
	QALog                []ports.QAPair
}

// SelectNewestNonExcludedMarkdown returns the newest markdown file in dir
// after applying the contract-level artifact exclusions.
func SelectNewestNonExcludedMarkdown(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", fmt.Errorf("select newest markdown: empty dir")
	}
	return newestPhaseMarkdownArtifact(dir), nil
}

// RunBlockingLoop runs a reusable fresh-session phase loop until COMPLETE or a
// safety rail terminates it.
func RunBlockingLoop(ctx context.Context, cfg BlockingLoopConfig, sm ports.SessionManager) (*BlockingLoopResult, error) {
	cfg = normalizeBlockingLoopConfig(cfg)
	if err := validateBlockingLoopConfig(cfg, sm); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.ArtifactDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating blocking loop artifact dir: %w", err)
	}

	am := NewArtifactManager(cfg.ArtifactDir)
	startIter := am.LatestIteration()
	tracker := NewProgressTracker()
	consecutiveFailures, consecutiveProtocol, err := recoverBlockingLoopCounters(cfg, am, startIter, tracker)
	if err != nil {
		return nil, err
	}
	var qaLog []ports.QAPair
	if cfg.AccumulateQALog {
		var err error
		qaLog, err = recoverBlockingLoopQALog(cfg, am, startIter)
		if err != nil {
			return nil, err
		}
	}
	if startIter > 0 {
		iterDir := blockingLoopIterationDir(cfg.ArtifactDir, startIter)
		if meta, err := am.ReadMeta(iterDir); err == nil && meta.AgentStatus == agentStatusSuccess && meta.ReviewStatus == HelperHandoffComplete.String() {
			canonical, _ := cfg.CanonicalSelector(iterDir)
			if reason := validateBlockingLoopCanonical(canonical); reason != "" {
				return &BlockingLoopResult{
					FinalStatus:          BlockingLoopStatusProtocolViolation,
					Iterations:           startIter,
					LastError:            reason,
					TerminalIterationDir: iterDir,
				}, nil
			}
			if cfg.AccumulateQALog {
				if len(qaLog) > 0 {
					if _, err := WriteQAFile(qaLog, cfg.ArtifactDir); err != nil {
						return nil, fmt.Errorf("writing blocking loop phase-root qa file: %w", err)
					}
				}
			}
			return &BlockingLoopResult{
				FinalStatus:          BlockingLoopStatusSuccess,
				Iterations:           startIter,
				TerminalIterationDir: iterDir,
				CanonicalPath:        canonical,
				QALog:                qaLog,
			}, nil
		}
	}

	for iteration := startIter + 1; ; iteration++ {
		select {
		case <-ctx.Done():
			return &BlockingLoopResult{FinalStatus: BlockingLoopStatusInterrupted, Iterations: iteration - 1, LastError: ctx.Err().Error()}, nil
		default:
		}
		if isBlockingLoopFeatureInterrupted(cfg) {
			return &BlockingLoopResult{FinalStatus: BlockingLoopStatusInterrupted, Iterations: iteration - 1}, nil
		}

		iterStart := time.Now()
		iterDir, err := am.CreateIterationDir(iteration)
		if err != nil {
			return nil, fmt.Errorf("creating blocking loop iteration dir: %w", err)
		}
		seededPath, previousPath, err := prepareBlockingLoopIteration(cfg, iterDir, iteration)
		if err != nil {
			return nil, err
		}
		var seededQAPath string
		if cfg.AccumulateQALog {
			if err := seedBlockingLoopQA(iterDir, qaLog); err != nil {
				return nil, err
			}
			if len(qaLog) > 0 {
				seededQAPath = filepath.Join(iterDir, "qa-answers.md")
			}
		}
		prompt, err := buildBlockingLoopPrompt(cfg, BlockingLoopPromptInput{
			Iteration:             iteration,
			IterationDir:          iterDir,
			HandoffPath:           filepath.Join(iterDir, cfg.HandoffFilename),
			SeededCanonicalPath:   seededPath,
			SeededQAPath:          seededQAPath,
			PreviousCanonicalPath: previousPath,
		})
		if err != nil {
			return nil, err
		}

		sessionID := blockingLoopSessionID(cfg, iteration)
		runResult, err := runBlockingLoopIteration(ctx, cfg, sm, BlockingLoopRunInput{
			Iteration:    iteration,
			IterationDir: iterDir,
			SessionID:    sessionID,
			Prompt:       prompt,
		})
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || isBlockingLoopFeatureInterrupted(cfg) {
				return &BlockingLoopResult{FinalStatus: BlockingLoopStatusInterrupted, Iterations: iteration - 1, LastError: err.Error()}, nil
			}
			return nil, err
		}
		if cfg.AccumulateQALog {
			qaLog = appendUniqueQAPairs(qaLog, runResult.QALog...)
			if len(qaLog) > 0 {
				if _, err := WriteQAFile(qaLog, iterDir); err != nil {
					return nil, fmt.Errorf("writing blocking loop iteration qa file: %w", err)
				}
			}
		}

		status := strings.TrimSpace(runResult.Status)
		if status == "" {
			status = agentStatusSuccess
		}
		if status == agentStatusInterrupted || isBlockingLoopFeatureInterrupted(cfg) {
			return &BlockingLoopResult{FinalStatus: BlockingLoopStatusInterrupted, Iterations: iteration - 1, TerminalIterationDir: iterDir}, nil
		}
		if status == agentStatusFailed && sm != nil && waitForShutdownIntent(sm, shutdownDetectionGrace) {
			return &BlockingLoopResult{FinalStatus: BlockingLoopStatusInterrupted, Iterations: iteration - 1, TerminalIterationDir: iterDir}, nil
		}
		meta := IterationMeta{
			Iteration:   iteration,
			StartedAt:   iterStart,
			Duration:    time.Since(iterStart),
			ExitCode:    runResult.ExitCode,
			AgentStatus: status,
			CostUSD:     runResult.Cost.TotalCostUSD,
			Context:     blockingLoopContextMeta(runResult.Handoff),
		}

		if status == agentStatusMissingMarker || (status == agentStatusSuccess && !HasPhaseComplete(iterDir)) {
			reason := "SDK reported success but phase_complete was not present"
			done, err := recordBlockingLoopProtocolViolation(am, iterDir, meta, iteration, &consecutiveProtocol, cfg.MaxConsecProtocolViolations, reason)
			if err != nil || done != nil {
				return done, err
			}
			consecutiveFailures = 0
			continue
		}
		if status != agentStatusSuccess {
			consecutiveFailures++
			consecutiveProtocol = 0
			meta.AgentStatus = agentStatusFailed
			meta.ExitCode = exitCodeFromAgentStatus(agentStatusFailed)
			if err := am.WriteMeta(iterDir, meta); err != nil {
				return nil, fmt.Errorf("writing blocking loop failure meta: %w", err)
			}
			if consecutiveFailures >= cfg.MaxConsecFailures {
				return &BlockingLoopResult{
					FinalStatus:          BlockingLoopStatusSafetyRail,
					Iterations:           iteration,
					LastError:            fmt.Sprintf("%d consecutive agent failures", consecutiveFailures),
					TerminalIterationDir: iterDir,
				}, nil
			}
			continue
		}

		handoffPath := filepath.Join(iterDir, cfg.HandoffFilename)
		parsed, err := cfg.ParseHandoff(handoffPath)
		if err != nil {
			return nil, err
		}
		if !parsed.OK() {
			done, err := recordBlockingLoopProtocolViolation(am, iterDir, meta, iteration, &consecutiveProtocol, cfg.MaxConsecProtocolViolations, strings.Join(parsed.ProtocolViolations, "; "))
			if err != nil || done != nil {
				return done, err
			}
			consecutiveFailures = 0
			continue
		}

		canonicalPath, err := cfg.CanonicalSelector(iterDir)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(canonicalPath) == "" {
			done, err := recordBlockingLoopProtocolViolation(am, iterDir, meta, iteration, &consecutiveProtocol, cfg.MaxConsecProtocolViolations, "canonical markdown artifact is missing")
			if err != nil || done != nil {
				return done, err
			}
			consecutiveFailures = 0
			continue
		}
		if reason := validateBlockingLoopCanonical(canonicalPath); reason != "" {
			done, err := recordBlockingLoopProtocolViolation(am, iterDir, meta, iteration, &consecutiveProtocol, cfg.MaxConsecProtocolViolations, reason)
			if err != nil || done != nil {
				return done, err
			}
			consecutiveFailures = 0
			continue
		}

		consecutiveFailures = 0
		consecutiveProtocol = 0

		// Auto-complete: when a ProgressStrategy reports zero pending units, the
		// engine — not the agent — declares the phase done, overriding a CONTINUE
		// handoff. LedgerAbsent (-1) does NOT trigger this. (A COMPLETE handoff
		// that still reports pending units is rejected earlier by the ledger
		// parser as a protocol violation, so it never reaches here.)
		if cfg.ProgressStrategy != nil && parsed.State == HelperHandoffContinue {
			pending, perr := cfg.ProgressStrategy.CountPending(handoffPath)
			if perr != nil {
				return nil, perr
			}
			if pending == 0 {
				parsed.State = HelperHandoffComplete
			}
		}

		madeProgress := true
		if parsed.State == HelperHandoffContinue {
			if cfg.ProgressStrategy != nil {
				madeProgress, err = checkNetPendingProgress(handoffPath, cfg.ProgressStrategy, tracker)
			} else {
				madeProgress, err = tracker.CheckWithFingerprint(handoffPath, cfg.Fingerprint)
			}
			if err != nil {
				return nil, err
			}
		}
		meta.AgentStatus = agentStatusSuccess
		meta.ReviewStatus = parsed.State.String()
		meta.MadeProgress = madeProgress
		meta.ExitCode = 0
		if err := am.WriteMeta(iterDir, meta); err != nil {
			return nil, fmt.Errorf("writing blocking loop success meta: %w", err)
		}

		if parsed.State == HelperHandoffComplete {
			if cfg.AccumulateQALog && len(qaLog) > 0 {
				if _, err := WriteQAFile(qaLog, cfg.ArtifactDir); err != nil {
					return nil, fmt.Errorf("writing blocking loop phase-root qa file: %w", err)
				}
			}
			return &BlockingLoopResult{
				FinalStatus:          BlockingLoopStatusSuccess,
				Iterations:           iteration,
				TerminalIterationDir: iterDir,
				CanonicalPath:        canonicalPath,
				QALog:                qaLog,
			}, nil
		}
		if !madeProgress && tracker.NoProgressCount() >= cfg.MaxConsecNoProgress {
			return &BlockingLoopResult{
				FinalStatus:          BlockingLoopStatusSafetyRail,
				Iterations:           iteration,
				LastError:            fmt.Sprintf("no progress for %d consecutive continuations", tracker.NoProgressCount()),
				TerminalIterationDir: iterDir,
				CanonicalPath:        canonicalPath,
			}, nil
		}
	}
}

// RunBlockingLoopAsync runs RunBlockingLoop in a goroutine and returns a
// buffered result channel matching the other loop dispatch APIs.
func (pr *PhaseRunner) RunBlockingLoopAsync(ctx context.Context, cfg BlockingLoopConfig) (chan *BlockingLoopResult, error) {
	if cfg.BuildSession == nil {
		cfg.BuildSession = pr.BuildSession
	}
	if cfg.StateDir == "" {
		cfg.StateDir = filepath.Join(pr.StateDir, cfg.FeatureID)
	}
	if cfg.SkillsDir == "" {
		cfg.SkillsDir = pr.SkillsDir
	}
	if cfg.GuidelinesDir == "" {
		cfg.GuidelinesDir = pr.GuidelinesDir
	}
	if cfg.Observer == nil {
		cfg.Observer = pr.Observer
	}
	if cfg.FeatureStore == nil {
		cfg.FeatureStore = pr.FeatureStore
	}
	resultCh := make(chan *BlockingLoopResult, 1)
	go func() {
		result, err := RunBlockingLoop(ctx, cfg, pr.SessionManager)
		if err != nil {
			resultCh <- &BlockingLoopResult{FinalStatus: BlockingLoopStatusFailed, LastError: err.Error()}
			return
		}
		resultCh <- result
	}()
	return resultCh, nil
}

func normalizeBlockingLoopConfig(cfg BlockingLoopConfig) BlockingLoopConfig {
	if cfg.Label == "" {
		cfg.Label = "blocking-loop"
	}
	if cfg.FeatureID == "" && cfg.Feature != nil {
		cfg.FeatureID = cfg.Feature.ID
	}
	if cfg.HandoffFilename != "" {
		cfg.HandoffFilename = filepath.Base(cfg.HandoffFilename)
	}
	if cfg.CanonicalSelector == nil {
		cfg.CanonicalSelector = SelectNewestNonExcludedMarkdown
	}
	if cfg.MaxConsecNoProgress <= 0 {
		cfg.MaxConsecNoProgress = defaultPlanningMaxConsecutiveNoProgress
	}
	if cfg.MaxConsecFailures <= 0 {
		cfg.MaxConsecFailures = defaultPlanningMaxConsecutiveFailures
	}
	if cfg.MaxConsecProtocolViolations <= 0 {
		cfg.MaxConsecProtocolViolations = DefaultMaxConsecutiveProtocolViolations
	}
	if cfg.TelemetryRole == "" {
		cfg.TelemetryRole = cfg.Label
	}
	return cfg
}

func isBlockingLoopFeatureInterrupted(cfg BlockingLoopConfig) bool {
	return isFeatureInterrupted(cfg.FeatureStore, cfg.FeatureID)
}

func validateBlockingLoopConfig(cfg BlockingLoopConfig, sm ports.SessionManager) error {
	if strings.TrimSpace(cfg.ArtifactDir) == "" {
		return fmt.Errorf("blocking loop %s: artifact dir is empty", cfg.Label)
	}
	if strings.TrimSpace(cfg.HandoffFilename) == "" {
		return fmt.Errorf("blocking loop %s: handoff filename is empty", cfg.Label)
	}
	if cfg.ParseHandoff == nil {
		return fmt.Errorf("blocking loop %s: handoff parser is nil", cfg.Label)
	}
	if cfg.Fingerprint == nil && cfg.ProgressStrategy == nil {
		return fmt.Errorf("blocking loop %s: fingerprint function or progress strategy is required", cfg.Label)
	}
	if cfg.RunSession == nil {
		if cfg.BuildSession == nil {
			return fmt.Errorf("blocking loop %s: build session function is nil", cfg.Label)
		}
		if sm == nil {
			return fmt.Errorf("blocking loop %s: session manager is nil", cfg.Label)
		}
	}
	return nil
}

// checkNetPendingProgress counts pending units in the current handoff via the
// ProgressStrategy and records them on the tracker. Returns true iff the count
// strictly decreased vs the prior iteration (net progress). A LedgerAbsent (-1)
// count never trips the stall rail by itself (see ProgressTracker.CheckPendingCount).
func checkNetPendingProgress(handoffPath string, ps ProgressStrategy, tracker *ProgressTracker) (bool, error) {
	pending, err := ps.CountPending(handoffPath)
	if err != nil {
		return false, err
	}
	return tracker.CheckPendingCount(pending), nil
}

func recoverBlockingLoopCounters(cfg BlockingLoopConfig, am *ArtifactManager, latest int, tracker *ProgressTracker) (int, int, error) {
	var consecutiveFailures int
	var consecutiveProtocol int
	for i := 1; i <= latest; i++ {
		iterDir := blockingLoopIterationDir(cfg.ArtifactDir, i)
		meta, err := am.ReadMeta(iterDir)
		if err != nil {
			continue
		}
		switch meta.AgentStatus {
		case agentStatusFailed, agentStatusAPIError:
			consecutiveFailures++
			consecutiveProtocol = 0
			continue
		case agentStatusProtocolViolation:
			consecutiveProtocol++
			consecutiveFailures = 0
			continue
		default:
			consecutiveFailures = 0
			consecutiveProtocol = 0
		}
		if meta.AgentStatus == agentStatusSuccess && meta.ReviewStatus == HelperHandoffContinue.String() {
			handoffPath := filepath.Join(iterDir, cfg.HandoffFilename)
			// Replay the SAME progress predicate the live loop used, so the
			// no-progress counter survives a restart identically (Decision 1
			// invariant). Net-pending for migrated phases; fingerprint otherwise.
			if cfg.ProgressStrategy != nil {
				pending, perr := cfg.ProgressStrategy.CountPending(handoffPath)
				if perr != nil {
					return 0, 0, perr
				}
				_ = tracker.CheckPendingCount(pending)
			} else if _, err := tracker.CheckWithFingerprint(handoffPath, cfg.Fingerprint); err != nil {
				return 0, 0, err
			}
		}
	}
	return consecutiveFailures, consecutiveProtocol, nil
}

func recoverBlockingLoopQALog(cfg BlockingLoopConfig, am *ArtifactManager, latest int) ([]ports.QAPair, error) {
	var qaLog []ports.QAPair
	for i := 1; i <= latest; i++ {
		iterDir := blockingLoopIterationDir(cfg.ArtifactDir, i)
		if _, err := am.ReadMeta(iterDir); err != nil {
			continue
		}
		pairs, err := ReadQAFile(filepath.Join(iterDir, "qa-answers.md"))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		qaLog = appendUniqueQAPairs(qaLog, pairs...)
	}
	return qaLog, nil
}

func seedBlockingLoopQA(iterDir string, qaLog []ports.QAPair) error {
	path := filepath.Join(iterDir, "qa-answers.md")
	if len(qaLog) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing stale blocking loop qa file: %w", err)
		}
		return nil
	}
	if _, err := WriteQAFile(qaLog, iterDir); err != nil {
		return fmt.Errorf("seeding blocking loop qa file: %w", err)
	}
	return nil
}

func appendUniqueQAPairs(existing []ports.QAPair, incoming ...ports.QAPair) []ports.QAPair {
	if len(incoming) == 0 {
		return existing
	}
	seen := make(map[string]struct{}, len(existing)+len(incoming))
	for _, pair := range existing {
		seen[qaPairKey(pair)] = struct{}{}
	}
	out := append([]ports.QAPair(nil), existing...)
	for _, pair := range incoming {
		key := qaPairKey(pair)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, pair)
	}
	return out
}

func qaPairKey(pair ports.QAPair) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%t\x00%.12g", pair.Question, pair.Answer, pair.Notes, pair.AutoPicked, pair.Confidence)
}

func prepareBlockingLoopIteration(cfg BlockingLoopConfig, iterDir string, iteration int) (string, string, error) {
	RemovePhaseComplete(iterDir)
	_ = os.Remove(filepath.Join(iterDir, cfg.HandoffFilename))
	if cfg.InPlaceCanonical || cfg.PersistentDeliverablePath != "" {
		// Persistent-deliverable phases edit a single file in place; never copy
		// the (potentially huge) prior deliverable into the new iteration dir.
		return "", "", nil
	}
	if err := removeBlockingLoopCanonicalCandidates(iterDir); err != nil {
		return "", "", err
	}
	if iteration <= 1 {
		return "", "", nil
	}
	previousPath := latestPriorBlockingLoopCanonical(cfg, iteration-1)
	if previousPath == "" {
		return "", "", nil
	}
	seededPath := filepath.Join(iterDir, filepath.Base(previousPath))
	if err := copyBlockingLoopFile(previousPath, seededPath); err != nil {
		return "", "", fmt.Errorf("seeding prior canonical markdown: %w", err)
	}
	return seededPath, previousPath, nil
}

func removeBlockingLoopCanonicalCandidates(iterDir string) error {
	matches, err := filepath.Glob(filepath.Join(iterDir, "*.md"))
	if err != nil {
		return fmt.Errorf("scanning blocking loop markdown candidates: %w", err)
	}
	for _, path := range matches {
		if IsArtifactExcluded(filepath.Base(path)) {
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing stale canonical candidate %s: %w", path, err)
		}
	}
	return nil
}

func latestPriorBlockingLoopCanonical(cfg BlockingLoopConfig, previousIteration int) string {
	for i := previousIteration; i >= 1; i-- {
		iterDir := blockingLoopIterationDir(cfg.ArtifactDir, i)
		path, err := cfg.CanonicalSelector(iterDir)
		if err == nil && strings.TrimSpace(path) != "" {
			return path
		}
	}
	return ""
}

func copyBlockingLoopFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read source: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("mkdir destination: %w", err)
	}
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("stat source: %w", err)
	}
	if err := os.WriteFile(dst, data, info.Mode().Perm()); err != nil {
		return fmt.Errorf("write destination: %w", err)
	}
	return nil
}

func validateBlockingLoopCanonical(path string) string {
	if strings.TrimSpace(path) == "" {
		return "canonical markdown artifact is missing"
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Sprintf("canonical markdown artifact %s is missing", path)
		}
		return fmt.Sprintf("canonical markdown artifact %s could not be inspected: %v", path, err)
	}
	if info.IsDir() {
		return fmt.Sprintf("canonical markdown artifact %s is a directory", path)
	}
	if info.Size() == 0 {
		return fmt.Sprintf("canonical markdown artifact %s is empty", path)
	}
	return ""
}

func buildBlockingLoopPrompt(cfg BlockingLoopConfig, in BlockingLoopPromptInput) (string, error) {
	if cfg.BuildPrompt != nil {
		if in.Iteration > 1 && cfg.ResumeStrategy != nil {
			// Resume from the most recent PRIOR iteration that left a valid ledger
			// handoff — not blindly iteration N-1, which may have been a protocol
			// violation or failure that left no parseable ledger. Skipping straight
			// to N-1 there would silently drop the pending-unit list.
			if priorHandoff := latestPriorLedgerHandoff(cfg, in.Iteration-1); priorHandoff != "" {
				rc, rerr := cfg.ResumeStrategy.Build(in.Iteration, priorHandoff, cfg.PersistentDeliverablePath)
				if rerr != nil {
					return "", rerr
				}
				in.ResumeContext = rc
			}
		}
		return cfg.BuildPrompt(in)
	}
	return cfg.InitialPrompt, nil
}

// latestPriorLedgerHandoff walks iterations backward from fromIteration to 1 and
// returns the path of the most recent handoff that parses to a non-nil ledger,
// or "" when none exists (e.g. only protocol-violation iterations so far).
func latestPriorLedgerHandoff(cfg BlockingLoopConfig, fromIteration int) string {
	if cfg.ParseHandoff == nil {
		return ""
	}
	for i := fromIteration; i >= 1; i-- {
		handoffPath := filepath.Join(blockingLoopIterationDir(cfg.ArtifactDir, i), cfg.HandoffFilename)
		if _, err := os.Stat(handoffPath); err != nil {
			continue
		}
		parsed, err := cfg.ParseHandoff(handoffPath)
		if err != nil || parsed == nil || parsed.Ledger == nil {
			continue
		}
		return handoffPath
	}
	return ""
}

func runBlockingLoopIteration(ctx context.Context, cfg BlockingLoopConfig, sm ports.SessionManager, in BlockingLoopRunInput) (BlockingLoopRunResult, error) {
	if cfg.RunSession != nil {
		return cfg.RunSession(ctx, in)
	}
	return runBlockingLoopProviderSession(ctx, cfg, sm, in)
}

func runBlockingLoopProviderSession(ctx context.Context, cfg BlockingLoopConfig, sm ports.SessionManager, in BlockingLoopRunInput) (BlockingLoopRunResult, error) {
	select {
	case <-ctx.Done():
		return BlockingLoopRunResult{Status: agentStatusInterrupted}, nil
	default:
	}
	systemPrompt := BuildRoleSystemPrompt(BuildRoleSystemPromptInput{
		Spec:          cfg.Spec,
		IterationDir:  in.IterationDir,
		SkillsDir:     cfg.SkillsDir,
		GuidelinesDir: cfg.GuidelinesDir,
		KBInfos:       cfg.KBInfos,
		Model:         cfg.Model,
		AskingClause:  cfg.AskingClause,
	})
	workDir := cfg.WorkDir
	if workDir == "" {
		workDir = cfg.StateDir
	}
	pidDir := cfg.StateDir
	if pidDir == "" && cfg.FeatureID != "" {
		pidDir = filepath.Join(filepath.Dir(cfg.ArtifactDir), cfg.FeatureID)
	}
	sessionCtx := blockingLoopSessionContext(cfg)
	additionalDirs := append([]string(nil), cfg.AdditionalDirs...)
	if cfg.InPlaceCanonical && strings.TrimSpace(in.IterationDir) != "" {
		additionalDirs = append(additionalDirs, in.IterationDir)
	}
	command, env, sessOpts, err := cfg.BuildSession(BuildSessionOpts{
		Model:                          cfg.Model,
		Prompt:                         in.Prompt,
		SystemPrompt:                   systemPrompt,
		AdditionalDirs:                 additionalDirs,
		AgentNames:                     append([]string(nil), cfg.AgentNames...),
		PIDDir:                         pidDir,
		PermHandler:                    permHandlerFor(cfg.DangerouslySkipPermissions, cfg.PermissionCache, cfg.RepoName),
		RepoName:                       cfg.RepoName,
		WorkDir:                        workDir,
		EffortLevel:                    cfg.EffortLevel,
		Phase:                          cfg.Phase,
		SystemPromptHasUsefulResources: true,
		MarkerPath:                     filepath.Join(in.IterationDir, PhaseCompleteFile),
	})
	if err != nil {
		return BlockingLoopRunResult{}, fmt.Errorf("building %s session: %w", cfg.Label, err)
	}
	if sessOpts == nil {
		sessOpts = &ports.SessionOpts{}
	}
	sessOpts = enableTruncatedTurnAutoResume(sessOpts)
	WriteDebugPrompts(in.IterationDir, sessOpts.DebugSystemPrompt, in.Prompt)
	sessOpts.Iteration = in.Iteration
	sessOpts.PermCacheScope = cfg.RepoName
	sessOpts.AskUserAutoPick = askUserAutoPickConfig(
		cfg.FeatureStore,
		cfg.Observer,
		cfg.Feature,
		interactiveAutoPickPurpose(cfg.Phase),
		sessionCtx,
		in.SessionID,
		cfg.RepoName,
		in.Iteration,
	)

	select {
	case <-ctx.Done():
		return BlockingLoopRunResult{Status: agentStatusInterrupted}, nil
	default:
	}
	if isBlockingLoopFeatureInterrupted(cfg) {
		return BlockingLoopRunResult{Status: agentStatusInterrupted}, nil
	}

	sess, err := sm.StartSession(in.SessionID, cfg.FeatureID, cfg.Phase, command, workDir, env, sessOpts)
	if err != nil {
		if errors.Is(err, ports.ErrSessionShuttingDown) {
			return BlockingLoopRunResult{Status: agentStatusInterrupted}, nil
		}
		return BlockingLoopRunResult{}, fmt.Errorf("starting %s session: %w", cfg.Label, err)
	}

	sessionStart := time.Now()
	providerName := ""
	if sessOpts != nil {
		providerName = sessOpts.ProviderName
	}
	cfg.Observer.SessionStarted(sessionCtx, cfg.TelemetryRole, in.SessionID, providerName, cfg.Model, cfg.RepoName)
	if cfg.Observer != nil {
		tracker := &ContextReadTracker{
			KBBaseDir:     filepath.Join(filepath.Dir(cfg.StateDir), "knowledge-base"),
			SkillsDir:     cfg.SkillsDir,
			GuidelinesDir: cfg.GuidelinesDir,
			Observer:      cfg.Observer,
		}
		tracker.Install(sess, sessionCtx, cfg.TelemetryRole, in.SessionID)
	}

	logPath := filepath.Join(in.IterationDir, "response.txt")
	if logFile, err := os.Create(logPath); err == nil {
		sess.SetLogFile(logFile)
	}

	waitResult := waitForStatusDetailed(sess, sm, in.SessionID, waitForStatusOptions{
		ReadyCheck: func() bool {
			if HasPhaseComplete(in.IterationDir) {
				sess.SetHasUnansweredQuestion(false)
				return true
			}
			return false
		},
		EnableContextHandoff:          true,
		ContextHandoffRole:            blockingLoopContextHandoffRole(cfg),
		ContextHandoffDisabled:        sessOpts.ContextHandoffDisabled,
		ContextHandoffThresholdTokens: sessOpts.ContextHandoffThresholdTokens,
		OnContextHandoff: func(snap contextSnapshot) {
			cfg.Observer.ContextHandoffTriggered(
				sessionCtx,
				cfg.TelemetryRole,
				in.SessionID,
				cfg.RepoName,
				sess.ProviderName(),
				in.Iteration,
				snap.Pct,
				snap.ThresholdPct,
				snap.ThresholdTokens,
				snap.TotalTokens,
				snap.WindowTokens,
				snap.BaselineTokens,
			)
		},
	})
	cost := ExtractSessionCost(sess)
	cfg.Observer.SessionEnded(sessionCtx, cfg.TelemetryRole, in.SessionID, cfg.RepoName, toSessionUsage(cost), time.Since(sessionStart), sessionErrFromLogicalAgentStatus(waitResult.Status, sess))
	return BlockingLoopRunResult{
		Status:   waitResult.Status,
		Handoff:  waitResult.Handoff,
		Cost:     cost,
		ExitCode: exitCodeFromAgentStatus(waitResult.Status),
		QALog:    sess.QALog(),
	}, nil
}

func recordBlockingLoopProtocolViolation(am *ArtifactManager, iterDir string, meta IterationMeta, iteration int, consecutive *int, limit int, reason string) (*BlockingLoopResult, error) {
	*consecutive = *consecutive + 1
	meta.AgentStatus = agentStatusProtocolViolation
	meta.ReviewStatus = "protocol_violation"
	meta.ExitCode = exitCodeFromAgentStatus(agentStatusProtocolViolation)
	if err := am.WriteMeta(iterDir, meta); err != nil {
		return nil, fmt.Errorf("writing blocking loop protocol meta: %w", err)
	}
	if *consecutive >= limit {
		return &BlockingLoopResult{
			FinalStatus:          BlockingLoopStatusProtocolViolation,
			Iterations:           iteration,
			LastError:            reason,
			TerminalIterationDir: iterDir,
		}, nil
	}
	return nil, nil
}

func blockingLoopIterationDir(artifactDir string, iteration int) string {
	return filepath.Join(artifactDir, fmt.Sprintf("iteration-%02d", iteration))
}

func blockingLoopSessionID(cfg BlockingLoopConfig, iteration int) string {
	base := cfg.SessionIDBase
	if base == "" {
		base = strings.Trim(strings.ReplaceAll(cfg.Label, " ", "-"), "-")
		if cfg.FeatureID != "" {
			base = cfg.FeatureID + "-" + base
		}
	}
	return fmt.Sprintf("%s-%02d", base, iteration)
}

func blockingLoopContextMeta(handoff contextSnapshot) *ContextMeta {
	if handoff.ThresholdTokens <= 0 {
		return nil
	}
	return &ContextMeta{
		ThresholdTokens:    handoff.ThresholdTokens,
		ThresholdPct:       handoff.ThresholdPct,
		FinalPct:           handoff.Pct,
		TotalTokens:        handoff.TotalTokens,
		WindowTokens:       handoff.WindowTokens,
		BaselineTokens:     handoff.BaselineTokens,
		HandoffTriggered:   handoff.TotalTokens >= handoff.ThresholdTokens,
		HandoffPct:         handoff.Pct,
		HandoffTotalTokens: handoff.TotalTokens,
	}
}

func blockingLoopContextHandoffRole(cfg BlockingLoopConfig) string {
	if skillName := strings.TrimSpace(cfg.Spec.SkillName); skillName != "" {
		return skillName
	}
	return cfg.TelemetryRole
}

func blockingLoopSessionContext(cfg BlockingLoopConfig) observe.SpanContext {
	if cfg.Feature == nil {
		return observe.SpanContext{}
	}
	return observe.SpanContextForFeature(cfg.Feature.ID, cfg.Feature.TraceID, cfg.Feature.Name, cfg.Feature.FeatureSpanID).WithRun(cfg.Feature.ActiveRun).Child()
}
