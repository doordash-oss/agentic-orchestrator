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

package feature

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"gopkg.in/yaml.v3"
)

// ErrPipelineMismatch is returned when a submitted pipeline does not match
// the addressed record's pipeline. Pipeline identity is child-specific and
// must not change through a paired config update.
var ErrPipelineMismatch = errors.New("submitted pipeline does not match the addressed record")

// PairedConfigIntent is the durable intent for an in-flight paired
// configuration update. It is persisted on the parent before either record
// changes so startup reconciliation can roll an interrupted update forward
// exactly once.
type PairedConfigIntent struct {
	ChildID   string            `yaml:"child_id"`
	UpdatedAt time.Time         `yaml:"updated_at"`
	Input     PairedConfigInput `yaml:"input"`
}

// PairedConfigInput carries the Review configuration axes applied
// identically to both parent and active child. Pipeline identity is
// deliberately absent: it is child-specific and must not change.
type PairedConfigInput struct {
	Models              config.ModelConfig     `yaml:"models"`
	Effort              config.EffortConfig    `yaml:"effort"`
	Inquireness         Inquireness            `yaml:"inquireness"`
	Checkpoints         Checkpoints            `yaml:"checkpoints"`
	InputNotifications  InputNotificationsMode `yaml:"input_notifications"`
	AutomaticReviewMode AutomaticReviewMode    `yaml:"automatic_review_mode"`
}

// PairedConfigResult describes the outcome of a paired config update.
type PairedConfigResult struct {
	ParentID string
	ChildID  string
	Changed  bool
}

// applyPairedConfig writes the Review configuration axes from input onto the
// feature, normalizing checkpoints against the feature's own pipeline and
// publishability. Pipeline identity is never touched.
func applyPairedConfig(f *Feature, input PairedConfigInput) ConfigSnapshot {
	f.Models = input.Models
	f.Effort = input.Effort
	f.Inquireness = input.Inquireness
	f.Checkpoints = f.Pipeline.NormalizeCheckpoints(input.Checkpoints, f.IsPublishable())
	f.InputNotifications = PersistInputNotificationsMode(input.InputNotifications)
	f.AutomaticReviewMode = PersistAutomaticReviewMode(input.AutomaticReviewMode)
	return ConfigSnapshot{
		Models:              f.Models,
		Effort:              f.Effort,
		Inquireness:         f.Inquireness,
		Checkpoints:         f.Checkpoints,
		InputNotifications:  NormalizeInputNotificationsMode(f.InputNotifications),
		AutomaticReviewMode: NormalizeAutomaticReviewMode(f.AutomaticReviewMode),
	}
}

// configSnapshotOf captures the current Review configuration axes.
func configSnapshotOf(f *Feature) ConfigSnapshot {
	return ConfigSnapshot{
		Models:              f.Models,
		Effort:              f.Effort,
		Inquireness:         f.Inquireness,
		Checkpoints:         f.Checkpoints,
		InputNotifications:  NormalizeInputNotificationsMode(f.InputNotifications),
		AutomaticReviewMode: NormalizeAutomaticReviewMode(f.AutomaticReviewMode),
	}
}

// UpdatePairedConfig atomically applies a paired Review configuration update
// to both the parent and its active child under the Store write lock. The
// submitted pipeline must match the addressed record's pipeline; a mismatch
// returns ErrPipelineMismatch without changing either record. Durable intent
// is written before the first record changes so startup reconciliation can
// roll an interrupted update forward exactly once. A racing child close or
// discard serializes to an all-old or all-new result under the same lock.
func (s *Store) UpdatePairedConfig(
	parentID string,
	input PairedConfigInput,
	submittedPipeline PipelineProfile,
	addressedID string,
) (*PairedConfigResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	parent, err := s.loadUnlocked(parentID)
	if err != nil {
		return nil, fmt.Errorf("loading parent: %w", err)
	}
	child, err := s.activeChildOfUnlocked(parentID)
	if err != nil {
		return nil, fmt.Errorf("looking up active child: %w", err)
	}
	if child == nil {
		return nil, fmt.Errorf("no active child for parent %s", parentID)
	}

	// A racing child close/discard sets CloseOutcome under this lock, so
	// if the child is no longer active the config edit cannot proceed.
	if child.Parent.CloseOutcome != "" {
		return nil, fmt.Errorf("child %s is no longer active (outcome %s)", child.ID, child.Parent.CloseOutcome)
	}
	if child.DiscardIntent != nil {
		return nil, fmt.Errorf("child %s has a pending discard intent", child.ID)
	}

	// Validate the submitted pipeline matches the addressed record.
	addressedPipeline := parent.EffectivePipeline()
	if addressedID == child.ID {
		addressedPipeline = child.EffectivePipeline()
	}
	if submittedPipeline != "" && submittedPipeline.IsValid() && submittedPipeline != addressedPipeline {
		return nil, fmt.Errorf("%w: submitted %s, addressed record %s", ErrPipelineMismatch, submittedPipeline, addressedPipeline)
	}

	parentBefore := configSnapshotOf(parent)
	childBefore := configSnapshotOf(child)

	// Step 1: Write durable intent on the parent before any record changes.
	intent := &PairedConfigIntent{
		ChildID:   child.ID,
		UpdatedAt: time.Now(),
		Input:     input,
	}
	parent.PendingConfigUpdate = intent
	if err := s.saveUnlocked(parent); err != nil {
		return nil, fmt.Errorf("saving parent with config intent: %w", err)
	}

	// Step 2: Apply to child.
	applyPairedConfig(child, input)
	if err := s.saveUnlocked(child); err != nil {
		// Clear the intent so the parent is not left with a stale intent.
		parent.PendingConfigUpdate = nil
		_ = s.saveUnlocked(parent)
		return nil, fmt.Errorf("saving child config: %w", err)
	}

	// Step 3: Apply to parent and clear intent.
	parentAfter := applyPairedConfig(parent, input)
	parent.PendingConfigUpdate = nil
	if err := s.saveUnlocked(parent); err != nil {
		return nil, fmt.Errorf("saving parent config: %w", err)
	}

	changed := !reflect.DeepEqual(parentBefore, parentAfter) || !reflect.DeepEqual(childBefore, parentAfter)

	return &PairedConfigResult{
		ParentID: parentID,
		ChildID:  child.ID,
		Changed:  changed,
	}, nil
}

// ReconcilePendingConfigUpdates rolls interrupted paired config updates
// forward exactly once. For every parent carrying a PendingConfigUpdate
// intent: apply the intent to both records and clear it. Idempotent.
func (s *Store) ReconcilePendingConfigUpdates() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.BaseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing features directory: %w", err)
	}
	var reconciled []string
	var errs []error
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.BaseDir, e.Name(), "feature.yaml"))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			errs = append(errs, fmt.Errorf("reading feature record %q during config-update scan: %w", e.Name(), err))
			continue
		}
		var parent Feature
		if err := yaml.Unmarshal(data, &parent); err != nil {
			errs = append(errs, fmt.Errorf("parsing feature record %q during config-update scan: %w", e.Name(), err))
			continue
		}
		if parent.PendingConfigUpdate == nil {
			continue
		}
		intent := parent.PendingConfigUpdate
		child, err := s.loadUnlocked(intent.ChildID)
		if err != nil {
			errs = append(errs, fmt.Errorf("parent %s: loading child %s for config reconciliation: %w", parent.ID, intent.ChildID, err))
			continue
		}
		applyPairedConfig(child, intent.Input)
		if err := s.saveUnlocked(child); err != nil {
			errs = append(errs, fmt.Errorf("parent %s: reconciling child %s config: %w", parent.ID, intent.ChildID, err))
			continue
		}
		applyPairedConfig(&parent, intent.Input)
		parent.PendingConfigUpdate = nil
		if err := s.writeFeatureYAMLUnlocked(parent.ID, &parent); err != nil {
			errs = append(errs, fmt.Errorf("clearing config intent on parent %s: %w", parent.ID, err))
			continue
		}
		reconciled = append(reconciled, parent.ID)
	}
	if len(errs) > 0 {
		return reconciled, errors.Join(errs...)
	}
	return reconciled, nil
}
