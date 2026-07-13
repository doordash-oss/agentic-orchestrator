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
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"gopkg.in/yaml.v3"
)

// RunDir returns the absolute directory for a specific run of a feature.
// e.g., filepath.Join(stateDir, featureID, "runs", "run-003").
func RunDir(stateDir, featureID string, runNumber int) string {
	if runNumber <= 0 {
		runNumber = 1
	}
	return filepath.Join(stateDir, featureID, "runs", feature.RunDirName(runNumber))
}

// ActiveRunDir returns the absolute directory for the feature's active run.
// Uses a defensive fallback for shadow-field code paths that construct Feature
// values in tests and elsewhere without going through the Store: ActiveRun=0
// is treated as run-001 to keep existing call sites stable.
func ActiveRunDir(stateDir string, f *feature.Feature) string {
	if f == nil {
		return ""
	}
	rn := f.ActiveRun
	if rn <= 0 {
		rn = 1
	}
	return RunDir(stateDir, f.ID, rn)
}

// PhaseDir returns the base directory for a roadmap phase's artifacts within
// the feature's active run. e.g., runs/run-001/phase-01/.
func PhaseDir(stateDir string, f *feature.Feature, phase int) string {
	return filepath.Join(ActiveRunDir(stateDir, f), fmt.Sprintf("phase-%02d", phase))
}

// PhasePlanDir returns the plan subdirectory for a roadmap phase.
// e.g., runs/run-001/phase-01/plan/.
func PhasePlanDir(stateDir string, f *feature.Feature, phase int) string {
	return filepath.Join(PhaseDir(stateDir, f, phase), "plan")
}

// PhaseImplementDir returns the implement subdirectory for a roadmap phase.
// e.g., runs/run-001/phase-01/implement/.
func PhaseImplementDir(stateDir string, f *feature.Feature, phase int) string {
	return filepath.Join(PhaseDir(stateDir, f, phase), "implement")
}

// PhaseTestingContractDir returns the base directory for a roadmap phase's
// testing contract artifact. The contract lives at the phase root so
// implementation and review share one binding file.
func PhaseTestingContractDir(stateDir string, f *feature.Feature, phase int) string {
	return PhaseDir(stateDir, f, phase)
}

// PhaseTestingContractPath returns the absolute path to a roadmap phase's
// compiled testing contract artifact.
func PhaseTestingContractPath(stateDir string, f *feature.Feature, phase int) string {
	return filepath.Join(PhaseTestingContractDir(stateDir, f, phase), "testing-contract.yaml")
}

func cycleArtifactRoot(stateDir string, f *feature.Feature, repoName string, cycleType feature.RepoCycleType) string {
	root := filepath.Join(ActiveRunDir(stateDir, f), cycleArtifactDirName(f, repoName, cycleType))
	if repoName != "" {
		root = filepath.Join(root, repoName)
	}
	return root
}

// CycleTestingContractPath returns the absolute path to a cycle-scoped
// compiled testing contract artifact.
func CycleTestingContractPath(stateDir string, f *feature.Feature, repoName string, cycleType feature.RepoCycleType) string {
	return filepath.Join(cycleArtifactRoot(stateDir, f, repoName, cycleType), "testing-contract.yaml")
}

// LatestCycleImplementationVerificationReportPath returns the most recent
// implementation verification report under a cycle-scoped implement root.
func LatestCycleImplementationVerificationReportPath(stateDir string, f *feature.Feature, repoName string, cycleType feature.RepoCycleType) string {
	implementDir := filepath.Join(cycleArtifactRoot(stateDir, f, repoName, cycleType), "implement")
	iteration := NewArtifactManager(implementDir).LatestIteration()
	if iteration == 0 {
		return ""
	}
	return filepath.Join(implementDir, fmt.Sprintf("iteration-%02d", iteration), "verification-report.yaml")
}

// RoadmapDir returns the roadmap artifacts directory within the active run.
// e.g., runs/run-001/roadmap/.
func RoadmapDir(stateDir string, f *feature.Feature) string {
	return filepath.Join(ActiveRunDir(stateDir, f), "roadmap")
}

// RefactorBaseDir returns the base directory for a refactor cycle's artifacts
// within the active run. e.g., runs/run-001/refactor-1/.
func RefactorBaseDir(stateDir string, f *feature.Feature, n int) string {
	return filepath.Join(ActiveRunDir(stateDir, f), fmt.Sprintf("refactor-%d", n))
}

type IterationMeta struct {
	Iteration    int           `yaml:"iteration"`
	StartedAt    time.Time     `yaml:"started_at"`
	Duration     time.Duration `yaml:"duration"`
	ExitCode     int           `yaml:"exit_code"`
	AgentStatus  string        `yaml:"agent_status"`
	ReviewStatus string        `yaml:"review_status"`
	MadeProgress bool          `yaml:"made_progress"`
	CostUSD      float64       `yaml:"cost_usd,omitempty"`
	Context      *ContextMeta  `yaml:"context,omitempty"`
}

type ContextMeta struct {
	Provider           string `yaml:"provider,omitempty"`
	ThresholdPct       int    `yaml:"threshold_pct,omitempty"`
	FinalPct           int    `yaml:"final_pct,omitempty"`
	TotalTokens        int    `yaml:"total_tokens,omitempty"`
	WindowTokens       int    `yaml:"window_tokens,omitempty"`
	BaselineTokens     int    `yaml:"baseline_tokens,omitempty"`
	HandoffTriggered   bool   `yaml:"handoff_triggered"`
	HandoffPct         int    `yaml:"handoff_pct,omitempty"`
	HandoffTotalTokens int    `yaml:"handoff_total_tokens,omitempty"`
}

// HandoffArtifact identifies a handoff input without embedding its contents.
// Paths are relative to the implementation artifact root so receipts remain
// portable when a state directory is moved or attached to an issue.
type HandoffArtifact struct {
	Path   string `yaml:"path"`
	SHA256 string `yaml:"sha256"`
}

// ImplementationHandoffReceipt is the privacy-safe, machine-readable join
// between an implementation iteration that hit the context threshold and the
// fresh iteration that resumes from its durable artifacts.
type ImplementationHandoffReceipt struct {
	Version               int             `yaml:"version"`
	FeatureID             string          `yaml:"feature_id"`
	Phase                 string          `yaml:"phase"`
	RoadmapPhase          int             `yaml:"roadmap_phase,omitempty"`
	Repo                  string          `yaml:"repo,omitempty"`
	FromIterationID       string          `yaml:"from_iteration_id"`
	ToIterationID         string          `yaml:"to_iteration_id"`
	SessionID             string          `yaml:"session_id"`
	Provider              string          `yaml:"provider,omitempty"`
	Trigger               string          `yaml:"trigger"`
	ObservedPct           int             `yaml:"observed_pct"`
	ThresholdPct          int             `yaml:"threshold_pct"`
	ObservedTotalTokens   int             `yaml:"observed_total_tokens,omitempty"`
	ContextWindowTokens   int             `yaml:"context_window_tokens,omitempty"`
	ContextBaselineTokens int             `yaml:"context_baseline_tokens,omitempty"`
	CanonicalArtifact     HandoffArtifact `yaml:"canonical_artifact"`
	VerificationArtifact  HandoffArtifact `yaml:"verification_artifact"`
	IntentionallyDropped  []string        `yaml:"intentionally_dropped"`
	NextSafeAction        string          `yaml:"next_safe_action"`
	CreatedAt             time.Time       `yaml:"created_at"`
}

// ArtifactManager handles iteration directory creation and file writing.
type ArtifactManager struct {
	BaseDir string
}

func NewArtifactManager(baseDir string) *ArtifactManager {
	return &ArtifactManager{BaseDir: baseDir}
}

func (am *ArtifactManager) CreateIterationDir(n int) (string, error) {
	dir := filepath.Join(am.BaseDir, fmt.Sprintf("iteration-%02d", n))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating iteration directory: %w", err)
	}
	return dir, nil
}

func (am *ArtifactManager) WriteResponse(iterDir string, response string) error {
	return os.WriteFile(filepath.Join(iterDir, "response.txt"), []byte(response), 0o644)
}

func (am *ArtifactManager) WriteMeta(iterDir string, meta IterationMeta) error {
	data, err := yaml.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshaling meta: %w", err)
	}
	return os.WriteFile(filepath.Join(iterDir, "meta.yaml"), data, 0o644)
}

// WriteImplementationHandoffReceipt writes a compact receipt into the source
// iteration. It fingerprints the durable inputs the next iteration will use,
// but never copies prompts, transcripts, or artifact contents into the receipt.
func (am *ArtifactManager) WriteImplementationHandoffReceipt(iterDir string, receipt ImplementationHandoffReceipt) error {
	progressPath := filepath.Join(am.BaseDir, "progress.md")
	verificationPath := filepath.Join(iterDir, "verification-report.yaml")

	progressHash, err := Fingerprint(progressPath)
	if err != nil {
		return fmt.Errorf("fingerprinting progress artifact: %w", err)
	}
	verificationHash, err := Fingerprint(verificationPath)
	if err != nil {
		return fmt.Errorf("fingerprinting verification artifact: %w", err)
	}

	receipt.CanonicalArtifact = HandoffArtifact{Path: "progress.md", SHA256: progressHash}
	receipt.VerificationArtifact = HandoffArtifact{
		Path:   filepath.ToSlash(filepath.Join(filepath.Base(iterDir), "verification-report.yaml")),
		SHA256: verificationHash,
	}
	data, err := yaml.Marshal(receipt)
	if err != nil {
		return fmt.Errorf("marshaling implementation handoff receipt: %w", err)
	}
	return os.WriteFile(filepath.Join(iterDir, "handoff-receipt.yaml"), data, 0o644)
}

// WriteDebugPrompts writes the system and user prompts to files for debugging.
func WriteDebugPrompts(dir, systemPrompt, userPrompt string) {
	_ = os.WriteFile(filepath.Join(dir, "system-prompt.md"), []byte(systemPrompt), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "user-prompt.md"), []byte(userPrompt), 0o644)
}

// WriteValidatorSystemPrompt writes only the system prompt for a validator session.
// The user prompt is already saved separately as validation-{domain}-prompt.md.
func WriteValidatorSystemPrompt(dir, prefix, systemPrompt string) {
	_ = os.WriteFile(filepath.Join(dir, prefix+"-system-prompt.md"), []byte(systemPrompt), 0o644)
}

// LatestIteration returns the highest iteration number that completed
// (has a meta.yaml file), or 0 if none exist. Directories without
// meta.yaml are incomplete iterations that should be re-run.
func (am *ArtifactManager) LatestIteration() int {
	entries, err := os.ReadDir(am.BaseDir)
	if err != nil {
		return 0
	}
	latest := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(e.Name(), "iteration-%d", &n); err == nil && n > latest {
			metaPath := filepath.Join(am.BaseDir, e.Name(), "meta.yaml")
			if _, statErr := os.Stat(metaPath); statErr == nil {
				latest = n
			}
		}
	}
	return latest
}

// ReadMeta reads and parses the meta.yaml file from an iteration directory.
func (am *ArtifactManager) ReadMeta(iterDir string) (IterationMeta, error) {
	data, err := os.ReadFile(filepath.Join(iterDir, "meta.yaml"))
	if err != nil {
		return IterationMeta{}, err
	}
	var meta IterationMeta
	if err := yaml.Unmarshal(data, &meta); err != nil {
		return IterationMeta{}, err
	}
	return meta, nil
}

func (am *ArtifactManager) WriteSummary(summaryPath string, meta IterationMeta) error {
	f, err := os.OpenFile(summaryPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("opening summary file: %w", err)
	}
	defer func() { _ = f.Close() }()

	line := fmt.Sprintf("iteration=%d status=%s review=%s progress=%v duration=%s cost=$%.4f",
		meta.Iteration, meta.AgentStatus, meta.ReviewStatus, meta.MadeProgress, meta.Duration, meta.CostUSD)
	if meta.Context != nil {
		line += fmt.Sprintf(" context=%d%%/%d%% tokens=%d/%d handoff=%v",
			meta.Context.FinalPct,
			meta.Context.ThresholdPct,
			meta.Context.TotalTokens,
			meta.Context.WindowTokens,
			meta.Context.HandoffTriggered,
		)
	}
	line += "\n"
	_, err = f.WriteString(line)
	return err
}
