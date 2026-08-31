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
	"errors"
	"fmt"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// UpdatePairedFeatureConfig applies a serialized, intent-backed, recoverable
// paired Review configuration update to both the parent and its active child.
// The submitted pipeline must match the addressed record's pipeline. On
// success, emits refresh signals for both feature identifiers. Rejected or
// replayed no-op writes do not emit misleading partial-success events.
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
// an active child or the active child itself. Returns an error if active-child
// discovery fails so the caller fails closed rather than falling through to
// the single-record update path.
func (o *Orchestrator) DetectPairedConfigTarget(featureID string) (parentID string, childID string, paired bool, err error) {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil || f == nil {
		return "", "", false, err
	}
	if f.IsChild() && f.IsActiveChild() {
		return f.Parent.ParentID, f.ID, true, nil
	}
	if !f.IsChild() {
		cid, err := o.activeChildID(featureID)
		if err != nil {
			return "", "", false, fmt.Errorf("detecting paired config target: %w", err)
		}
		if cid != "" {
			return featureID, cid, true, nil
		}
	}
	return "", "", false, nil
}

// activeChildID returns the ID of the active child for the given parent, or
// empty string if none.
func (o *Orchestrator) activeChildID(parentID string) (string, error) {
	features, err := o.deps.Store.List()
	if err != nil {
		// PartialLoadError carries the features that DID load alongside the
		// per-ID load warnings. Treat it as soft: legacy or malformed records
		// predate child relationships and must not block the scan.
		var ple *feature.PartialLoadError
		if !errors.As(err, &ple) {
			return "", fmt.Errorf("listing features: %w", err)
		}
	}
	for _, f := range features {
		if f.IsChild() && f.Parent != nil && f.Parent.ParentID == parentID && f.Parent.CloseOutcome == "" {
			return f.ID, nil
		}
	}
	return "", nil
}
