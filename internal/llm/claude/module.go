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

package claude

import (
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"go.uber.org/fx"
)

// Module registers the Claude provider in the LLM registry, honoring an
// optional CLI binary override from config (providers.claude.cli).
var Module = fx.Module("llm-claude",
	fx.Invoke(func(r *llm.Registry, cfg *config.Config) {
		r.Register(&Provider{binary: cfg.ProviderCLI("claude", defaultBinary)})
	}),
)
