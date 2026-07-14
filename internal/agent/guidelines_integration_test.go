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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/guidelinedef"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// newGuidelinesTestRunner creates a PhaseRunner configured for guidelines
// injection tests, using a mock registry so BuildSession takes the production
// code path (not the BuildSessionFn shortcut).
func newGuidelinesTestRunner(t *testing.T) (*PhaseRunner, *mocks.MockProvider) {
	t.Helper()
	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, "state")
	os.MkdirAll(stateDir, 0o755)

	guidelinesDir := filepath.Join(tmpDir, "guidelines")
	if err := guidelinedef.ReconcileGuidelines(guidelinesDir); err != nil {
		t.Fatalf("ReconcileGuidelines: %v", err)
	}

	mockProv := &mocks.MockProvider{
		ProviderName: testMockIdentifier,
		Models:       []string{"test-model"},
		CLIDetected:  true,
		CommandArgs:  []string{"echo", "test"},
	}
	registry := llm.NewRegistry()
	registry.Register(mockProv)

	pr := &PhaseRunner{
		StateDir:      stateDir,
		GuidelinesDir: guidelinesDir,
		Registry:      registry,
	}
	return pr, mockProv
}

func TestBuildSession_GuidelinesInjection(t *testing.T) {
	pr, mockProv := newGuidelinesTestRunner(t)

	codeTouchingPhases := []feature.Phase{
		feature.PhaseDesign,
		feature.PhasePlan,
		feature.PhaseImplement,
		feature.PhaseReview,
	}

	for _, phase := range codeTouchingPhases {
		t.Run(phase.String(), func(t *testing.T) {
			mockProv.BuildCommandCalls = nil
			_, _, _, err := pr.BuildSession(BuildSessionOpts{
				Model:        "test-model",
				SystemPrompt: "Base system prompt",
				Phase:        phase,
				AgentNames:   []string{},
			})
			if err != nil {
				t.Fatalf("BuildSession error: %v", err)
			}
			if len(mockProv.BuildCommandCalls) != 1 {
				t.Fatalf("expected 1 BuildCommand call, got %d", len(mockProv.BuildCommandCalls))
			}
			sysPrompt := mockProv.BuildCommandCalls[0].Opts.SystemPrompt
			if !strings.Contains(sysPrompt, "Available Language Guidelines") {
				t.Error("system prompt missing guidelines preamble")
			}
			if !strings.Contains(sysPrompt, "Go") {
				t.Error("system prompt missing Go guideline entry")
			}
		})
	}
}

func TestBuildSession_GuidelinesNotInjected(t *testing.T) {
	pr, mockProv := newGuidelinesTestRunner(t)

	nonCodePhases := []feature.Phase{
		feature.PhaseResearch,
		feature.PhaseInquire,
		feature.PhaseKnowledgeBase,
		feature.PhasePublish,
	}

	for _, phase := range nonCodePhases {
		t.Run(phase.String(), func(t *testing.T) {
			mockProv.BuildCommandCalls = nil
			_, _, _, err := pr.BuildSession(BuildSessionOpts{
				Model:        "test-model",
				SystemPrompt: "Base system prompt",
				Phase:        phase,
				AgentNames:   []string{},
			})
			if err != nil {
				t.Fatalf("BuildSession error: %v", err)
			}
			if len(mockProv.BuildCommandCalls) != 1 {
				t.Fatalf("expected 1 BuildCommand call, got %d", len(mockProv.BuildCommandCalls))
			}
			sysPrompt := mockProv.BuildCommandCalls[0].Opts.SystemPrompt
			if strings.Contains(sysPrompt, "Available Language Guidelines") {
				t.Errorf("system prompt should NOT contain guidelines for phase %s", phase)
			}
		})
	}
}

func TestBuildSession_GuidelinesAdditionalDirs(t *testing.T) {
	pr, mockProv := newGuidelinesTestRunner(t)

	_, _, _, err := pr.BuildSession(BuildSessionOpts{
		Model:        "test-model",
		SystemPrompt: "Base system prompt",
		Phase:        feature.PhaseImplement,
		AgentNames:   []string{},
	})
	if err != nil {
		t.Fatalf("BuildSession error: %v", err)
	}
	if len(mockProv.BuildCommandCalls) != 1 {
		t.Fatalf("expected 1 BuildCommand call, got %d", len(mockProv.BuildCommandCalls))
	}

	dirs := mockProv.BuildCommandCalls[0].Opts.AdditionalDirs
	found := false
	for _, d := range dirs {
		if d == pr.GuidelinesDir {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("AdditionalDirs %v does not include GuidelinesDir %q", dirs, pr.GuidelinesDir)
	}
}

func TestBuildSession_GuidelinesEmptyDir(t *testing.T) {
	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, "state")
	os.MkdirAll(stateDir, 0o755)

	mockProv := &mocks.MockProvider{
		ProviderName: testMockIdentifier,
		Models:       []string{"test-model"},
		CLIDetected:  true,
		CommandArgs:  []string{"echo", "test"},
	}
	registry := llm.NewRegistry()
	registry.Register(mockProv)

	pr := &PhaseRunner{
		StateDir:      stateDir,
		GuidelinesDir: "", // intentionally empty
		Registry:      registry,
	}

	_, _, _, err := pr.BuildSession(BuildSessionOpts{
		Model:        "test-model",
		SystemPrompt: "Base system prompt",
		Phase:        feature.PhaseImplement,
		AgentNames:   []string{},
	})
	if err != nil {
		t.Fatalf("BuildSession error: %v", err)
	}
	if len(mockProv.BuildCommandCalls) != 1 {
		t.Fatalf("expected 1 BuildCommand call, got %d", len(mockProv.BuildCommandCalls))
	}

	sysPrompt := mockProv.BuildCommandCalls[0].Opts.SystemPrompt
	if strings.Contains(sysPrompt, "Available Language Guidelines") {
		t.Error("system prompt should NOT contain guidelines when GuidelinesDir is empty")
	}

	dirs := mockProv.BuildCommandCalls[0].Opts.AdditionalDirs
	for _, d := range dirs {
		if strings.Contains(d, "guidelines") {
			t.Errorf("AdditionalDirs should not include a guidelines path, got %v", dirs)
			break
		}
	}
}

func TestBuildSession_GuidelinesFinalReviewer(t *testing.T) {
	pr, mockProv := newGuidelinesTestRunner(t)

	// The final reviewer is an interactive session with Phase: PhaseReview and
	// a non-empty SystemPrompt, which triggers the guidelines preamble
	// injection in BuildSession.
	_, _, _, err := pr.BuildSession(BuildSessionOpts{
		Model:        "test-model",
		SystemPrompt: "You are a code reviewer.",
		Phase:        feature.PhaseReview,
		AgentNames:   []string{},
	})
	if err != nil {
		t.Fatalf("BuildSession error: %v", err)
	}
	if len(mockProv.BuildCommandCalls) != 1 {
		t.Fatalf("expected 1 BuildCommand call, got %d", len(mockProv.BuildCommandCalls))
	}
	sysPrompt := mockProv.BuildCommandCalls[0].Opts.SystemPrompt
	if !strings.Contains(sysPrompt, "Available Language Guidelines") {
		t.Error("final reviewer system prompt missing guidelines preamble")
	}
}

// Guidelines are no longer inlined in BuildReviewPrompt — they are discovered
// via the system prompt preamble injected by BuildSession. See
// TestBuildSession_GuidelinesFinalReviewer for the preamble test.
