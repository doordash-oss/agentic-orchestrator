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

package mocks

import (
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// MockProvider implements llm.LLMProvider and all segregated interfaces
// (PromptAdapter, CostCalculator, CatalogProvider). All return values
// are configurable via exported fields. Method calls are recorded for assertion.
type MockProvider struct {
	// LLMProvider fields
	ProviderName string
	Models       []string
	CLIDetected  bool
	CommandArgs  []string
	CommandEnv   []string
	Protocol     *MockProtocol
	Hint         string

	// PromptAdapter
	QuestionsClause string

	// CostCalculator
	CostPerCall   float64
	ContextWindow int

	// CatalogProvider
	Catalog []llm.ModelInfo

	// VersionInfo
	VersionInfoResult string
	VersionInfoErr    error
	MinVer            [3]int

	// EnvVars
	EnvVarsExclude []string

	// Call tracking
	BuildCommandCalls []BuildCommandCall
	NewProtocolCalls  int
}

// BuildCommandCall records arguments passed to BuildCommand.
type BuildCommandCall struct {
	Opts llm.CommandBuildOpts
}

// --- LLMProvider ---

func (p *MockProvider) Name() string { return p.ProviderName }

func (p *MockProvider) MatchesModel(model string) bool {
	for _, m := range p.Models {
		if m == model {
			return true
		}
	}
	return false
}

func (p *MockProvider) DetectCLI() bool { return p.CLIDetected }

func (p *MockProvider) AvailableModels() []string { return p.Models }

func (p *MockProvider) BuildCommand(opts llm.CommandBuildOpts) ([]string, []string, error) {
	p.BuildCommandCalls = append(p.BuildCommandCalls, BuildCommandCall{Opts: opts})
	return p.CommandArgs, p.CommandEnv, nil
}

func (p *MockProvider) NewProtocol(opts llm.ProtocolOpts) llm.Protocol {
	p.NewProtocolCalls++
	if p.Protocol != nil {
		return p.Protocol
	}
	// Return a default MockProtocol with a standard success sequence.
	return NewMockProtocol(StandardSequence("mock response")...)
}

func (p *MockProvider) InstallHint() string { return p.Hint }

func (p *MockProvider) VersionInfo() (string, error) {
	return p.VersionInfoResult, p.VersionInfoErr
}

func (p *MockProvider) MinVersion() [3]int { return p.MinVer }

func (p *MockProvider) EnvVarsToExclude() []string { return p.EnvVarsExclude }

// --- PromptAdapter ---

func (p *MockProvider) AskingQuestionsClause() string { return p.QuestionsClause }

// --- CostCalculator ---

func (p *MockProvider) ComputeCost(model string, inputTokens, outputTokens int64) float64 {
	return p.CostPerCall
}

func (p *MockProvider) ContextWindowForModel(model string) int { return p.ContextWindow }

// --- CatalogProvider ---

func (p *MockProvider) ModelCatalog() []llm.ModelInfo { return p.Catalog }
