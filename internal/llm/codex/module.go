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

package codex

import (
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"go.uber.org/fx"
)

// Module registers the Codex provider in the LLM registry and exports it
// for downstream modules (e.g. agent) to configure. An optional CLI binary
// override is read from config (providers.codex.cli).
var Module = fx.Module("llm-codex",
	fx.Provide(func(cfg *config.Config) *Provider {
		return &Provider{binary: cfg.ProviderCLI("codex", defaultBinary)}
	}),
	fx.Invoke(func(r *llm.Registry, p *Provider) {
		r.Register(p)
	}),
)
