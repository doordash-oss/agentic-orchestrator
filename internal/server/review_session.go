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

package server

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"gopkg.in/yaml.v3"
)

const (
	reviewModePlan   = "plan"
	reviewModeGate   = "gate"
	reviewModeRewind = "rewind"

	reviewDecisionProceed = "proceed"
	reviewDecisionIterate = "iterate"

	reviewArtifactPrompt = "prompt"
)

type reviewDecisionFunc func(featureID string, req ReviewDecisionRequest) error

type reviewSessionService struct {
	store   FeatureReader
	decider reviewDecisionFunc
	now     func() time.Time
	locks   *reviewSessionLockSet
}

type reviewSessionLockSet struct {
	locks sync.Map
}

func newReviewSessionLockSet() *reviewSessionLockSet {
	return &reviewSessionLockSet{}
}

func (s *reviewSessionLockSet) lock(featureID, reviewID string) func() {
	if s == nil {
		return func() {}
	}
	value, _ := s.locks.LoadOrStore(featureID+"\x00"+reviewID, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

type reviewSessionContext struct {
	feature        *feature.Feature
	run            *feature.Run
	runDir         string
	reviewID       string
	reviewMode     string
	targetPhase    feature.Phase
	artifactID     string
	sourcePath     string
	sourceRevision string
	canIterate     bool
	roadmap        bool
	phasePlan      bool
}

type reviewSessionMeta struct {
	ReviewID       string    `yaml:"review_id"`
	FeatureID      string    `yaml:"feature_id"`
	RunNumber      int       `yaml:"run_number"`
	ArtifactID     string    `yaml:"artifact_id"`
	ReviewMode     string    `yaml:"review_mode"`
	TargetPhase    string    `yaml:"target_phase"`
	SourcePath     string    `yaml:"source_path"`
	SourceRevision string    `yaml:"source_revision"`
	DraftRevision  string    `yaml:"draft_revision"`
	CanIterate     bool      `yaml:"can_iterate"`
	Roadmap        bool      `yaml:"roadmap,omitempty"`
	PhasePlan      bool      `yaml:"phase_plan,omitempty"`
	CreatedAt      time.Time `yaml:"created_at"`
	UpdatedAt      time.Time `yaml:"updated_at"`
}

func newReviewSessionService(store FeatureReader, decider reviewDecisionFunc, lockSets ...*reviewSessionLockSet) *reviewSessionService {
	locks := newReviewSessionLockSet()
	if len(lockSets) > 0 && lockSets[0] != nil {
		locks = lockSets[0]
	}
	return &reviewSessionService{
		store:   store,
		decider: decider,
		locks:   locks,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
}

func (s *reviewSessionService) Create(featureID string) (ReviewSessionResponse, error) {
	ctx, err := s.resolveContext(featureID)
	if err != nil {
		return ReviewSessionResponse{}, err
	}
	unlock := s.locks.lock(featureID, ctx.reviewID)
	defer unlock()
	source, err := os.ReadFile(ctx.sourcePath)
	if err != nil {
		return ReviewSessionResponse{}, fmt.Errorf("read review artifact: %w", err)
	}
	sessionDir := reviewSessionDir(ctx.runDir, ctx.reviewID)
	metaPath := filepath.Join(sessionDir, "metadata.yaml")
	draftPath := filepath.Join(sessionDir, "draft.md")
	if meta, draft, ok := s.loadExistingDraft(metaPath, draftPath, ctx); ok {
		return reviewSessionResponseFromMeta(meta, string(draft)), nil
	}
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return ReviewSessionResponse{}, fmt.Errorf("create review session dir: %w", err)
	}
	now := s.now()
	meta := reviewSessionMeta{
		ReviewID:       ctx.reviewID,
		FeatureID:      featureID,
		RunNumber:      ctx.run.RunNumber,
		ArtifactID:     ctx.artifactID,
		ReviewMode:     ctx.reviewMode,
		TargetPhase:    ctx.targetPhase.DirName(),
		SourcePath:     ctx.sourcePath,
		SourceRevision: ctx.sourceRevision,
		DraftRevision:  textRevision(source),
		CanIterate:     ctx.canIterate,
		Roadmap:        ctx.roadmap,
		PhasePlan:      ctx.phasePlan,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := os.WriteFile(draftPath, source, 0o644); err != nil {
		return ReviewSessionResponse{}, fmt.Errorf("write review draft: %w", err)
	}
	if err := writeReviewSessionMeta(metaPath, meta); err != nil {
		return ReviewSessionResponse{}, err
	}
	return reviewSessionResponseFromMeta(meta, string(source)), nil
}

// Read returns an existing active review session without re-seeding or
// otherwise changing the persisted draft. Callers use it after invalidation
// events and before reconcile flows, where Create's reopen semantics are unsafe.
func (s *reviewSessionService) Read(featureID string) (ReviewSessionResponse, error) {
	ctx, err := s.resolveContext(featureID)
	if err != nil {
		return ReviewSessionResponse{}, err
	}
	unlock := s.locks.lock(featureID, ctx.reviewID)
	defer unlock()
	meta, draftPath, _, err := s.loadMetaForFeature(featureID, ctx.reviewID)
	if err != nil {
		return ReviewSessionResponse{}, err
	}
	draft, err := os.ReadFile(draftPath)
	if err != nil {
		return ReviewSessionResponse{}, fmt.Errorf("read review draft: %w", err)
	}
	return reviewSessionResponseFromMeta(meta, string(draft)), nil
}

// ValidateDraft performs only synchronous advisory checks. It deliberately
// evaluates the supplied text instead of stored draft state so the desktop can
// explain whether the exact pending buffer is eligible for proceeding.
func (s *reviewSessionService) ValidateDraft(featureID, reviewID string, req ReviewDraftValidationRequest) (ReviewDraftValidationResponse, error) {
	unlock := s.locks.lock(featureID, reviewID)
	defer unlock()
	meta, _, _, err := s.loadMetaForFeature(featureID, reviewID)
	if err != nil {
		return ReviewDraftValidationResponse{}, err
	}
	response := ReviewDraftValidationResponse{
		APIVersion: APIVersion,
		FeatureID:  featureID,
		ReviewID:   reviewID,
		Revision:   textRevision([]byte(req.Text)),
		Findings:   []ReviewDraftValidationFinding{},
	}
	if meta.PhasePlan || meta.ArtifactID == feature.PhasePlan.DirName() {
		response.Applicable = true
		response.Findings = phasePlanValidationFindings(req.Text)
		response.Valid = len(response.Findings) == 0
		return response, nil
	}
	response.Applicable = false
	response.Valid = true
	return response, nil
}

func phasePlanValidationFindings(text string) []ReviewDraftValidationFinding {
	findings := make([]ReviewDraftValidationFinding, 0)
	if !strings.HasPrefix(strings.TrimSpace(text), "# ") {
		findings = append(findings, ReviewDraftValidationFinding{Code: "missing_title", Message: "Phase plans need a top-level Markdown title."})
	}
	if len(agent.ParsePlanTasks(text)) == 0 {
		findings = append(findings, ReviewDraftValidationFinding{Code: "missing_tasks", Message: "Phase plans need at least one task under the Tasks section."})
	}
	return findings
}

func (s *reviewSessionService) SaveDraft(featureID, reviewID string, req ReviewDraftUpdateRequest) (ReviewSessionResponse, error) {
	unlock := s.locks.lock(featureID, reviewID)
	defer unlock()
	meta, draftPath, metaPath, err := s.loadMetaForFeature(featureID, reviewID)
	if err != nil {
		return ReviewSessionResponse{}, err
	}
	if req.BaseRevision != meta.DraftRevision {
		return ReviewSessionResponse{}, staleReviewRevisionError(reviewID, meta.DraftRevision)
	}
	meta.DraftRevision = textRevision([]byte(req.Text))
	meta.UpdatedAt = s.now()
	if err := os.WriteFile(draftPath, []byte(req.Text), 0o644); err != nil {
		return ReviewSessionResponse{}, fmt.Errorf("write review draft: %w", err)
	}
	if err := writeReviewSessionMeta(metaPath, meta); err != nil {
		return ReviewSessionResponse{}, err
	}
	return reviewSessionResponseFromMeta(meta, req.Text), nil
}

func (s *reviewSessionService) SubmitDecision(featureID, reviewID string, req ReviewSessionDecisionRequest) (ReviewSessionDecisionResponse, error) {
	if req.Decision != reviewDecisionProceed && req.Decision != reviewDecisionIterate {
		return ReviewSessionDecisionResponse{}, fmt.Errorf("decision must be proceed or iterate")
	}
	unlock := s.locks.lock(featureID, reviewID)
	defer unlock()
	meta, draftPath, _, err := s.loadMetaForFeature(featureID, reviewID)
	if err != nil {
		return ReviewSessionDecisionResponse{}, err
	}
	if req.BaseRevision != meta.DraftRevision {
		return ReviewSessionDecisionResponse{}, staleReviewRevisionError(reviewID, meta.DraftRevision)
	}
	draft, err := os.ReadFile(draftPath)
	if err != nil {
		return ReviewSessionDecisionResponse{}, fmt.Errorf("read review draft: %w", err)
	}
	if err := os.WriteFile(meta.SourcePath, draft, 0o644); err != nil {
		return ReviewSessionDecisionResponse{}, fmt.Errorf("commit review draft: %w", err)
	}
	decisionReq := ReviewDecisionRequest{
		Decision:  req.Decision,
		Phase:     meta.TargetPhase,
		PhasePlan: meta.PhasePlan,
		Roadmap:   meta.Roadmap,
		IsRewind:  meta.ReviewMode == reviewModeRewind,
	}
	if s.decider != nil {
		if err := s.decider(featureID, decisionReq); err != nil {
			return ReviewSessionDecisionResponse{}, err
		}
	}
	return ReviewSessionDecisionResponse{
		APIVersion: APIVersion,
		FeatureID:  featureID,
		ReviewID:   reviewID,
		Decision:   req.Decision,
		Result:     "submitted",
	}, nil
}

func (s *reviewSessionService) loadExistingDraft(metaPath, draftPath string, ctx reviewSessionContext) (reviewSessionMeta, []byte, bool) {
	meta, err := readReviewSessionMeta(metaPath)
	if err != nil || meta.SourceRevision != ctx.sourceRevision {
		return reviewSessionMeta{}, nil, false
	}
	draft, err := os.ReadFile(draftPath)
	if err != nil {
		return reviewSessionMeta{}, nil, false
	}
	if meta.CanIterate != ctx.canIterate {
		meta.CanIterate = ctx.canIterate
		meta.UpdatedAt = s.now()
		_ = writeReviewSessionMeta(metaPath, meta)
	}
	return meta, draft, true
}

func (s *reviewSessionService) loadMetaForFeature(featureID, reviewID string) (reviewSessionMeta, string, string, error) {
	f, err := s.store.Load(featureID)
	if err != nil {
		return reviewSessionMeta{}, "", "", err
	}
	runNumber := f.ActiveRun
	if f.Run() != nil && f.Run().RunNumber > 0 {
		runNumber = f.Run().RunNumber
	}
	sessionDir := reviewSessionDir(s.store.RunDir(featureID, runNumber), reviewID)
	metaPath := filepath.Join(sessionDir, "metadata.yaml")
	meta, err := readReviewSessionMeta(metaPath)
	if err != nil {
		return reviewSessionMeta{}, "", "", err
	}
	if meta.FeatureID != featureID || meta.ReviewID != reviewID {
		return reviewSessionMeta{}, "", "", fmt.Errorf("review session does not match feature")
	}
	return meta, filepath.Join(sessionDir, "draft.md"), metaPath, nil
}

func (s *reviewSessionService) resolveContext(featureID string) (reviewSessionContext, error) {
	if s == nil || s.store == nil {
		return reviewSessionContext{}, fmt.Errorf("review session store is unavailable")
	}
	f, err := s.store.Load(featureID)
	if err != nil {
		return reviewSessionContext{}, err
	}
	if !f.Status.IsNeedsReview() {
		return reviewSessionContext{}, &ActionConflictError{Message: "feature is not paused on a review gate", Target: map[string]any{"feature_id": featureID}}
	}
	run := f.Run()
	if run == nil || run.RunNumber <= 0 {
		return reviewSessionContext{}, fmt.Errorf("active run is unavailable")
	}
	ctx := reviewSessionContext{feature: f, run: run, runDir: s.store.RunDir(featureID, run.RunNumber)}
	ctx.reviewMode, ctx.targetPhase = reviewSessionTarget(f)
	ctx.artifactID, ctx.roadmap, ctx.phasePlan = reviewArtifactIDForContext(f, ctx.reviewMode, ctx.targetPhase)
	if ctx.artifactID == "" {
		return reviewSessionContext{}, fmt.Errorf("review artifact is unavailable")
	}
	var path string
	if ctx.artifactID == descriptionReviewArtifact {
		var ok bool
		path, ok = descriptionReviewArtifactPathForRun(f.ID, ctx.runDir, f.EffectivePipeline(), run)
		if !ok {
			return reviewSessionContext{}, fmt.Errorf("review artifact %q not found", ctx.artifactID)
		}
	} else {
		rel, ok := run.Artifacts[ctx.artifactID]
		if !ok && f.Artifacts != nil {
			rel, ok = f.Artifacts[ctx.artifactID]
		}
		if !ok {
			return reviewSessionContext{}, fmt.Errorf("review artifact %q not found", ctx.artifactID)
		}
		var okPath bool
		path, okPath = resolveRunArtifactPath(ctx.runDir, rel)
		if !okPath {
			return reviewSessionContext{}, fmt.Errorf("invalid review artifact target")
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return reviewSessionContext{}, fmt.Errorf("read review artifact: %w", err)
	}
	ctx.sourcePath = path
	ctx.sourceRevision = textRevision(data)
	ctx.canIterate = planReviewCanIterate(ctx)
	ctx.reviewID = deterministicReviewID(featureID, run.RunNumber, ctx.reviewMode, ctx.targetPhase.DirName(), ctx.artifactID)
	return ctx, nil
}

func planReviewCanIterate(ctx reviewSessionContext) bool {
	if ctx.reviewMode != reviewModePlan {
		return false
	}
	status := strings.TrimSpace(agent.LastAttemptReviewStatus(filepath.Dir(ctx.sourcePath)))
	return status != agent.ReviewApproved.String()
}

func reviewSessionTarget(f *feature.Feature) (string, feature.Phase) {
	if f != nil && f.IsRewind && f.PendingReviewPhase != nil {
		return reviewModeRewind, *f.PendingReviewPhase
	}
	if f != nil && f.Status == feature.StatusPlanNeedsReview && f.PendingReviewPhase == nil {
		return reviewModePlan, feature.PhaseImplement
	}
	if f != nil && f.PendingReviewPhase != nil {
		return reviewModeGate, *f.PendingReviewPhase
	}
	switch f.Status {
	case feature.StatusPromptNeedsReview:
		return reviewModeGate, feature.PhaseInquire
	case feature.StatusInquiryNeedsReview:
		return reviewModeGate, feature.PhaseResearch
	case feature.StatusResearchNeedsReview:
		return reviewModeGate, feature.PhaseDesign
	case feature.StatusDesignNeedsReview:
		return reviewModeGate, feature.PhasePlan
	default:
		return reviewModePlan, feature.PhaseImplement
	}
}

func reviewArtifactIDForContext(f *feature.Feature, mode string, target feature.Phase) (artifactID string, roadmap bool, phasePlan bool) {
	if mode == reviewModePlan {
		if f.CurrentRoadmapPhase > 0 {
			return fmt.Sprintf("phase-%d-plan", f.CurrentRoadmapPhase), false, true
		}
		if hasArtifactID(f, "roadmap") {
			return "roadmap", true, false
		}
		return feature.PhasePlan.DirName(), false, false
	}
	if mode == reviewModeRewind {
		switch target {
		case feature.PhaseInquire:
			return descriptionReviewArtifact, false, false
		case feature.PhaseResearch:
			return feature.PhaseInquire.DirName(), false, false
		case feature.PhaseDesign:
			return feature.PhaseResearch.DirName(), false, false
		case feature.PhasePlan:
			if hasArtifactID(f, feature.PhaseDesign.DirName()) {
				return feature.PhaseDesign.DirName(), false, false
			}
			return feature.PhaseResearch.DirName(), false, false
		case feature.PhaseImplement:
			if f.PendingRewindReviewRoadmapPhase != nil && *f.PendingRewindReviewRoadmapPhase > 0 {
				return fmt.Sprintf("phase-%d-plan", *f.PendingRewindReviewRoadmapPhase), false, true
			}
			return feature.PhasePlan.DirName(), false, false
		}
	}
	switch target {
	case feature.PhaseInquire:
		return reviewArtifactPrompt, false, false
	case feature.PhaseResearch:
		return feature.PhaseInquire.DirName(), false, false
	case feature.PhaseDesign:
		return feature.PhaseResearch.DirName(), false, false
	case feature.PhasePlan:
		if hasArtifactID(f, feature.PhaseDesign.DirName()) {
			return feature.PhaseDesign.DirName(), false, false
		}
		return feature.PhaseResearch.DirName(), false, false
	case feature.PhaseImplement:
		if f.TotalRoadmapPhases > 0 && f.CurrentRoadmapPhase == 0 {
			return "roadmap", true, false
		}
		if f.CurrentRoadmapPhase > 0 {
			return fmt.Sprintf("phase-%d-plan", f.CurrentRoadmapPhase), false, true
		}
		return feature.PhasePlan.DirName(), false, false
	default:
		return "", false, false
	}
}

func hasArtifactID(f *feature.Feature, artifactID string) bool {
	if f == nil {
		return false
	}
	if f.Run() != nil && f.Run().Artifacts != nil {
		if _, ok := f.Run().Artifacts[artifactID]; ok {
			return true
		}
	}
	if f.Artifacts != nil {
		_, ok := f.Artifacts[artifactID]
		return ok
	}
	return false
}

func reviewSessionDir(runDir, reviewID string) string {
	return filepath.Join(runDir, "reviews", reviewID)
}

func deterministicReviewID(parts ...any) string {
	var b strings.Builder
	for _, part := range parts {
		fmt.Fprintf(&b, "%v\n", part)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return "review-" + hex.EncodeToString(sum[:])[:16]
}

func textRevision(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func writeReviewSessionMeta(path string, meta reviewSessionMeta) error {
	data, err := yaml.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal review session metadata: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write review session metadata: %w", err)
	}
	return nil
}

func readReviewSessionMeta(path string) (reviewSessionMeta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return reviewSessionMeta{}, err
	}
	var meta reviewSessionMeta
	if err := yaml.Unmarshal(data, &meta); err != nil {
		return reviewSessionMeta{}, fmt.Errorf("parse review session metadata: %w", err)
	}
	return meta, nil
}

func reviewSessionResponseFromMeta(meta reviewSessionMeta, text string) ReviewSessionResponse {
	return ReviewSessionResponse{
		APIVersion:     APIVersion,
		FeatureID:      meta.FeatureID,
		ReviewID:       meta.ReviewID,
		ReviewMode:     meta.ReviewMode,
		TargetPhase:    meta.TargetPhase,
		RunNumber:      meta.RunNumber,
		ArtifactID:     meta.ArtifactID,
		Text:           text,
		DraftRevision:  meta.DraftRevision,
		SourceRevision: meta.SourceRevision,
		CanIterate:     meta.CanIterate,
	}
}

func staleReviewRevisionError(reviewID, current string) error {
	return &ActionConflictError{
		Message: "review draft revision is stale",
		Target: map[string]any{
			"review_id":         reviewID,
			"current_revision":  current,
			"expected_revision": current,
		},
	}
}
