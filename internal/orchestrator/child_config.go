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

package orchestrator

import (
	"fmt"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// UpdatePairedFeatureConfig applies a paired Review configuration update to
// both the parent and its active child atomically. The submitted pipeline
// must match the addressed record's pipeline. On success, emits refresh
// signals for both feature identifiers. Rejected or replayed no-op writes
// do not emit misleading partial-success events.
func (o *Orchestrator) UpdatePairedFeatureConfig(parentID string, input feature.PairedConfigInput, submittedPipeline feature.PipelineProfile, addressedID string) error {
	type pairedConfigUpdater interface {
		UpdatePairedConfig(parentID string, input feature.PairedConfigInput, submittedPipeline feature.PipelineProfile, addressedID string) (*feature.PairedConfigResult, error)
	}
	updater, ok := o.deps.Store.(pairedConfigUpdater)
	if !ok {
		return fmt.Errorf("paired config update is not supported by the configured store")
	}
	result, err := updater.UpdatePairedConfig(parentID, input, submittedPipeline, addressedID)
	if err != nil {
		return err
	}
	if !result.Changed {
		return nil
	}
	o.emitEvent(ports.Event{
		Type:      ports.FeatureConfigChanged,
		FeatureID: result.ParentID,
	})
	o.emitEvent(ports.Event{
		Type:      ports.FeatureConfigChanged,
		FeatureID: result.ChildID,
	})
	return nil
}

// DetectPairedConfigTarget determines whether a config mutation addressed to
// featureID should be routed through the paired operation. Returns the parent
// ID, child ID, and true when the addressed feature is either a parent with
// an active child or the active child itself.
func (o *Orchestrator) DetectPairedConfigTarget(featureID string) (parentID string, childID string, paired bool) {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil || f == nil {
		return "", "", false
	}
	if f.IsChild() && f.IsActiveChild() {
		return f.Parent.ParentID, f.ID, true
	}
	if !f.IsChild() {
		cid, _ := o.activeChildID(featureID)
		if cid != "" {
			return featureID, cid, true
		}
	}
	return "", "", false
}

// activeChildID returns the ID of the active child for the given parent, or
// empty string if none.
func (o *Orchestrator) activeChildID(parentID string) (string, error) {
	features, err := o.deps.Store.List()
	if err != nil {
		return "", fmt.Errorf("listing features: %w", err)
	}
	for _, f := range features {
		if f.IsChild() && f.Parent != nil && f.Parent.ParentID == parentID && f.Parent.CloseOutcome == "" {
			return f.ID, nil
		}
	}
	return "", nil
}
