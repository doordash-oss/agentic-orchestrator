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
	"fmt"
	"os"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// UtilityRunConfig configures a bounded single-turn helper session for utility
// tasks such as summary and PR description generation.
type UtilityRunConfig struct {
	FeatureID   string
	SessionID   string
	Label       string
	Model       string
	Prompt      string
	WorkDir     string
	RepoName    string
	Timeout     time.Duration
	Phase       feature.Phase
	EffortLevel llm.EffortLevel
	// EffectiveEffort, when non-empty, overrides EffortLevel and is recorded
	// on the session for observability.
	EffectiveEffort llm.EffortLevel
	EffortSource    llm.EffortSource
	PermHandler     ports.PermissionHandler
	RequireText     bool
}

// UtilityRunResult captures the extracted text plus terminal helper state.
type UtilityRunResult struct {
	Text   string
	Status string
	Usage  llm.Usage
}

// RunUtilitySession runs a bounded single-turn helper session and returns the
// extracted text plus terminal session metadata.
func (pr *PhaseRunner) RunUtilitySession(ctx context.Context, cfg UtilityRunConfig) (*UtilityRunResult, error) {
	if cfg.SessionID == "" {
		return nil, fmt.Errorf("running utility session: missing session id")
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("running utility session: missing model")
	}
	if cfg.Prompt == "" {
		return nil, fmt.Errorf("running utility session: missing prompt")
	}

	workDir := cfg.WorkDir
	if workDir == "" {
		workDir = pr.StateDir
	}
	if workDir == "" {
		var err error
		workDir, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("running utility session: determining work dir: %w", err)
		}
	}
	effort := cfg.EffortLevel
	if effort == "" {
		effort = llm.EffortLow
	}

	result, err := pr.RunBoundedHelper(ctx, BoundedHelperConfig{
		SessionID:       cfg.SessionID,
		FeatureID:       cfg.FeatureID,
		Phase:           cfg.Phase,
		Model:           cfg.Model,
		Prompt:          cfg.Prompt,
		WorkDir:         workDir,
		RepoName:        cfg.RepoName,
		Timeout:         cfg.Timeout,
		EffortLevel:     effort,
		EffectiveEffort: cfg.EffectiveEffort,
		EffortSource:    cfg.EffortSource,
		PermHandler:     cfg.PermHandler,
		RequireOutput:   cfg.RequireText,
	})
	if result == nil {
		return nil, err
	}

	utilityResult := &UtilityRunResult{
		Text:   result.Output,
		Status: result.Status,
		Usage:  result.Usage,
	}
	if err != nil {
		label := cfg.Label
		if label == "" {
			label = "utility"
		}
		return utilityResult, fmt.Errorf("running %s session: %w", label, err)
	}
	return utilityResult, nil
}
