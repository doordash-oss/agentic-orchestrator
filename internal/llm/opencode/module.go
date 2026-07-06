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

package opencode

import (
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"go.uber.org/fx"
)

// Module registers the OpenCode provider in the LLM registry. It is included
// only when OpenCode is explicitly requested (`--providers opencode`) or
// auto-registered because the config already selects an `opencode:` model;
// OpenCode is not part of the unconditional default provider set. Once
// registered and ready it discovers and contributes a model catalog like the
// other providers.
var Module = fx.Module("llm-opencode",
	fx.Provide(New),
	fx.Invoke(func(r *llm.Registry, p *Provider) {
		r.Register(p)
	}),
)
