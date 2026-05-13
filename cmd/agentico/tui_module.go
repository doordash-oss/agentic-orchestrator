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

package main

import (
	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/internal/tui"
	"go.uber.org/fx"
)

type tuiParams struct {
	fx.In
	FeatureManager  *feature.Manager
	SessionManager  *session.Manager
	PhaseRunner     *agent.PhaseRunner
	Registry        *llm.Registry
	EventCh         chan interface{} `name:"eventCh"`
	ConfigPath      string           `name:"configPath"`
	WorkspaceDir    string           `name:"workspaceDir"`
	DSP             bool             `name:"dsp"`
	Observer        *observe.Observer
	Orchestrator    *orchestrator.Orchestrator
	PermissionCache *permission.Cache
}

var tuiModule = fx.Module("tui",
	fx.Provide(func(p tuiParams) (tui.AppModel, error) {
		var opts []tui.AppOption
		opts = append(opts, tui.WithConfigPath(p.ConfigPath))
		opts = append(opts, tui.WithWorkspaceDir(p.WorkspaceDir))
		opts = append(opts, tui.WithPhaseRunner(p.PhaseRunner))
		opts = append(opts, tui.WithRegistry(p.Registry))
		if p.DSP {
			opts = append(opts, tui.WithDangerouslySkipPermissions())
		}
		opts = append(opts, tui.WithObserver(observeAdapter{observer: p.Observer}))
		return tui.NewAppModel(p.FeatureManager, p.SessionManager, p.Orchestrator, p.PermissionCache, p.EventCh, opts...)
	}),
)

type observeAdapter struct {
	observer *observe.Observer
}

func (a observeAdapter) PermissionRequested(sc tui.ObservabilityContext, sessionID, repoName string, iteration int, toolName, toolInput string) {
	if a.observer == nil {
		return
	}
	a.observer.PermissionRequested(toObserveContext(sc), sessionID, repoName, iteration, toolName, toolInput)
}

func (a observeAdapter) PermissionResolved(sc tui.ObservabilityContext, sessionID, repoName string, iteration int, toolName, decision string) {
	if a.observer == nil {
		return
	}
	a.observer.PermissionResolved(toObserveContext(sc), sessionID, repoName, iteration, toolName, decision)
}

func (a observeAdapter) QuestionAsked(sc tui.ObservabilityContext, sessionID, repoName string, iteration int, question string) {
	if a.observer == nil {
		return
	}
	a.observer.QuestionAsked(toObserveContext(sc), sessionID, repoName, iteration, question)
}

func (a observeAdapter) QuestionAnswered(sc tui.ObservabilityContext, sessionID, repoName string, iteration int, question, answer string) {
	if a.observer == nil {
		return
	}
	a.observer.QuestionAnswered(toObserveContext(sc), sessionID, repoName, iteration, question, answer)
}

func toObserveContext(sc tui.ObservabilityContext) observe.SpanContext {
	return observe.SpanContext{
		TraceID:      sc.TraceID,
		SpanID:       sc.SpanID,
		ParentSpanID: sc.ParentSpanID,
		FeatureID:    sc.FeatureID,
		FeatureName:  sc.FeatureName,
		RunNumber:    sc.RunNumber,
	}
}
