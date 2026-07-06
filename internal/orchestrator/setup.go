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

type setupRunner interface {
	RunSetup(featureID string, opts ...feature.SetupRunnerOptions) error
	RetrySetup(featureID string, opts ...feature.SetupRunnerOptions) error
}

func (o *Orchestrator) RunSetup(featureID string) error {
	if err := o.runSetupWith(false, featureID); err != nil {
		return err
	}
	return o.StartFeature(featureID)
}

func (o *Orchestrator) RetrySetup(featureID string) error {
	if err := o.runSetupWith(true, featureID); err != nil {
		return err
	}
	return o.StartFeature(featureID)
}

func (o *Orchestrator) runSetupWith(retry bool, featureID string) error {
	runner, ok := o.deps.Lifecycle.(setupRunner)
	if !ok {
		return fmt.Errorf("setup runner not configured")
	}
	opt := feature.SetupRunnerOptions{
		OnEvent: func(ev feature.SetupEvent) {
			o.emitSetupEvent(ev)
		},
	}
	if retry {
		return runner.RetrySetup(featureID, opt)
	}
	return runner.RunSetup(featureID, opt)
}

func (o *Orchestrator) emitSetupEvent(ev feature.SetupEvent) {
	if o.hooks.OnSetupEvent != nil {
		o.hooks.OnSetupEvent(ev)
	}
	pe := setupPortsEvent(ev)
	if pe.Type == ports.SetupFailed {
		o.emitEventBlocking(pe)
		return
	}
	o.emitEvent(pe)
}

func setupPortsEvent(ev feature.SetupEvent) ports.Event {
	eventType := ports.SetupProgress
	switch ev.Kind {
	case feature.SetupEventStarted:
		eventType = ports.SetupStarted
	case feature.SetupEventCompleted:
		eventType = ports.SetupCompleted
	case feature.SetupEventFailed:
		eventType = ports.SetupFailed
	}
	return ports.Event{
		Type:        eventType,
		FeatureID:   ev.FeatureID,
		RunNumber:   ev.RunNumber,
		Attempt:     ev.Attempt,
		SetupLog:    ev.LogPath,
		SetupTask:   ev.TaskKey,
		SetupKind:   ev.TaskKind,
		SetupStatus: ev.TaskStatus,
		RepoName:    ev.Repo,
		Path:        ev.Path,
		Branch:      ev.Branch,
		Message:     string(ev.Kind),
		Error:       setupEventError(ev),
	}
}

func setupEventError(ev feature.SetupEvent) error {
	if ev.Error == "" {
		return nil
	}
	return fmt.Errorf("%s", ev.Error)
}
