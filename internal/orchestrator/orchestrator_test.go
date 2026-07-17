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

package orchestrator_test

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
	"go.uber.org/fx"
)

// ---------------------------------------------------------------------------
// Compile-time interface satisfaction checks
// ---------------------------------------------------------------------------

// These pass in Phase 1 — no adapters needed.
var _ ports.FeatureStore = (*feature.Store)(nil)
var _ ports.FeatureLifecycle = (*feature.Manager)(nil)

// Phase 2 — these now pass after session widening and adapter introduction.
var _ ports.SessionManager = (*session.Manager)(nil)
var _ ports.WorktreeOperator = (*git.WorktreeManager)(nil)

// ---------------------------------------------------------------------------
// TestOrchestrator_CreateFeature
// ---------------------------------------------------------------------------

func TestOrchestrator_CreateFeature(t *testing.T) {
	testFeature := &feature.Feature{
		ID:   "feat-123",
		Name: "test feature",
	}

	lifecycle := mocks.NewMockFeatureLifecycle()
	lifecycle.CreateFn = func(name, description string, repos []string, models config.ModelConfig,
		exitCriteria, inquireness string, images []string,
		opts ...feature.CreateOptions) (*feature.Feature, error) {
		return testFeature, nil
	}

	var hookCalled bool
	var hookFeature *feature.Feature

	o := orchestrator.New(
		orchestrator.Deps{
			Lifecycle: lifecycle,
			Store:     mocks.NewMockFeatureStore(),
		},
		orchestrator.Hooks{
			OnFeatureCreated: func(f *feature.Feature) {
				hookCalled = true
				hookFeature = f
			},
		},
	)

	f, err := o.CreateFeature("test feature", "description", []string{"/repo"},
		config.ModelConfig{}, "exit criteria", "ask questions", nil)
	if err != nil {
		t.Fatalf("CreateFeature: unexpected error: %v", err)
	}
	if f.ID != "feat-123" {
		t.Errorf("returned feature ID = %q, want %q", f.ID, "feat-123")
	}

	// Assert: hook was called with the feature
	if !hookCalled {
		t.Error("OnFeatureCreated hook was not called")
	}
	if hookFeature != testFeature {
		t.Error("OnFeatureCreated hook received wrong feature")
	}

	// Assert: event channel receives FeatureCreated event
	select {
	case ev := <-o.Events():
		if ev.Type != ports.FeatureCreated {
			t.Errorf("event type = %v, want FeatureCreated", ev.Type)
		}
		if ev.FeatureID != "feat-123" {
			t.Errorf("event FeatureID = %q, want %q", ev.FeatureID, "feat-123")
		}
		if ev.Feature != testFeature {
			t.Error("event Feature does not match returned feature")
		}
	default:
		t.Error("no event received on Events() channel")
	}

	// Assert: mock recorded the Create call
	if len(lifecycle.Calls) != 1 {
		t.Fatalf("lifecycle.Calls = %d, want 1", len(lifecycle.Calls))
	}
	if lifecycle.Calls[0].Method != "Create" {
		t.Errorf("lifecycle.Calls[0].Method = %q, want %q", lifecycle.Calls[0].Method, "Create")
	}
}

// ---------------------------------------------------------------------------
// TestOrchestrator_CreateFeature_Error
// ---------------------------------------------------------------------------

func TestOrchestrator_CreateFeature_Error(t *testing.T) {
	wantErr := errors.New("storage full")
	lifecycle := mocks.NewMockFeatureLifecycle()
	lifecycle.CreateFn = func(name, description string, repos []string, models config.ModelConfig,
		exitCriteria, inquireness string, images []string,
		opts ...feature.CreateOptions) (*feature.Feature, error) {
		return nil, wantErr
	}

	var hookCalled bool
	o := orchestrator.New(
		orchestrator.Deps{
			Lifecycle: lifecycle,
			Store:     mocks.NewMockFeatureStore(),
		},
		orchestrator.Hooks{
			OnFeatureCreated: func(f *feature.Feature) { hookCalled = true },
		},
	)

	_, err := o.CreateFeature("test", "desc", nil, config.ModelConfig{}, "", "", nil)
	if err == nil {
		t.Fatal("CreateFeature: expected error, got nil")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("error = %v, want wrapped %v", err, wantErr)
	}
	if !strings.Contains(err.Error(), "creating feature:") {
		t.Errorf("error message = %q, want prefix 'creating feature:'", err.Error())
	}

	// Assert: hook was NOT called
	if hookCalled {
		t.Error("OnFeatureCreated hook was called on error")
	}

	// Assert: no event emitted
	select {
	case ev := <-o.Events():
		t.Errorf("unexpected event emitted: %v", ev.Type)
	default:
		// expected
	}
}

func TestNewWiresVerificationProgressToEventStream(t *testing.T) {
	t.Parallel()
	phaseRunner := &agent.PhaseRunner{}
	o := orchestrator.New(orchestrator.Deps{PhaseRunner: phaseRunner}, orchestrator.Hooks{})

	if phaseRunner.OnVerificationProgress == nil {
		t.Fatal("PhaseRunner.OnVerificationProgress is nil")
	}
	phaseRunner.OnVerificationProgress("feat-live")

	select {
	case ev := <-o.Events():
		if ev.Type != ports.VerificationProgress || ev.FeatureID != "feat-live" {
			t.Fatalf("event = %+v, want VerificationProgress for feat-live", ev)
		}
	default:
		t.Fatal("verification progress did not reach orchestrator event stream")
	}
}

// ---------------------------------------------------------------------------
// TestOrchestrator_CreateFeature_NilHook
// ---------------------------------------------------------------------------

func TestOrchestrator_CreateFeature_NilHook(t *testing.T) {
	lifecycle := mocks.NewMockFeatureLifecycle()
	lifecycle.CreateFn = func(name, description string, repos []string, models config.ModelConfig,
		exitCriteria, inquireness string, images []string,
		opts ...feature.CreateOptions) (*feature.Feature, error) {
		return &feature.Feature{ID: "feat-1"}, nil
	}

	// All hooks are nil (zero value).
	o := orchestrator.New(
		orchestrator.Deps{
			Lifecycle: lifecycle,
			Store:     mocks.NewMockFeatureStore(),
		},
		orchestrator.Hooks{},
	)

	f, err := o.CreateFeature("test", "desc", nil, config.ModelConfig{}, "", "", nil)
	if err != nil {
		t.Fatalf("CreateFeature: %v", err)
	}
	if f.ID != "feat-1" {
		t.Errorf("feature ID = %q, want %q", f.ID, "feat-1")
	}
}

// ---------------------------------------------------------------------------
// TestOrchestrator_StubMethods
// ---------------------------------------------------------------------------

// TestOrchestrator_StubMethods asserts the orchestrator has no remaining
// ErrNotImplemented-returning stubs. This test intentionally exercises an
// empty list and passes; if a new stub is introduced, wire it into the slice to
// get a regression guard for free.
func TestOrchestrator_StubMethods(t *testing.T) {
	o := orchestrator.New(orchestrator.Deps{}, orchestrator.Hooks{})

	tests := []struct {
		name string
		call func(o *orchestrator.Orchestrator) error
	}{}

	if len(tests) == 0 {
		t.Log("no remaining ErrNotImplemented stubs on *orchestrator.Orchestrator")
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call(o)
			if !errors.Is(err, orchestrator.ErrNotImplemented) {
				t.Errorf("%s returned %v, want ErrNotImplemented", tt.name, err)
			}
		})
	}

	// Touch o so Go does not consider the variable unused when the slice is empty.
	_ = o
}

// ---------------------------------------------------------------------------
// TestOrchestrator_Events
// ---------------------------------------------------------------------------

func TestOrchestrator_Events(t *testing.T) {
	o := orchestrator.New(orchestrator.Deps{}, orchestrator.Hooks{})

	ch := o.Events()
	if ch == nil {
		t.Fatal("Events() returned nil channel")
	}
	if cap(ch) != 256 {
		t.Errorf("Events() channel capacity = %d, want 256", cap(ch))
	}
}

// ---------------------------------------------------------------------------
// TestOrchestrator_NoTUIImport — source-scan regression guard
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// A5. Hooks_RecoveryScanned_NewSignature
//
// Pure compile check that the hook signatures accept (items int) /
// (featureID, repoName, action string). If a future edit reverts either
// signature this test fails to compile.
// ---------------------------------------------------------------------------

func TestHooks_RecoveryScanned_NewSignature(t *testing.T) {
	var scanned int
	var sawFeature, sawRepo, sawAction string

	hooks := orchestrator.Hooks{
		OnRecoveryScanned: func(items []ports.RecoveryItem) { scanned = len(items) },
		OnRecoveryAction: func(featureID, repoName, action string) {
			sawFeature = featureID
			sawRepo = repoName
			sawAction = action
		},
	}
	o := orchestrator.New(orchestrator.Deps{}, hooks)
	if o == nil {
		t.Fatal("New returned nil")
	}

	// Use the locals so a future refactor can't accidentally optimize them away.
	_ = scanned
	_ = sawFeature
	_ = sawRepo
	_ = sawAction
}

func TestOrchestrator_FxIntegration(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	stateDir := filepath.Join(tmpDir, "features")

	var orch *orchestrator.Orchestrator

	app := fx.New(
		fx.Supply(
			fx.Annotate(configPath, fx.ResultTags(`name:"configPath"`)),
			fx.Annotate(stateDir, fx.ResultTags(`name:"stateDir"`)),
			fx.Annotate(make(chan any, 100), fx.ResultTags(`name:"eventCh"`)),
			fx.Annotate(false, fx.ResultTags(`name:"dsp"`)),
		),
		config.Module,
		feature.Module,
		git.Module,
		session.Module,
		llm.Module,
		observe.Module,
		permission.Module,
		agent.Module,
		orchestrator.Module,
		fx.Populate(&orch),
		fx.NopLogger,
	)

	ctx := context.Background()
	if err := app.Start(ctx); err != nil {
		t.Fatalf("fx.Start failed: %v", err)
	}

	if orch == nil {
		t.Fatal("orchestrator was not resolved by fx")
	}

	// Verify the orchestrator is functional (event channel is set up).
	if orch.Events() == nil {
		t.Error("orchestrator Events() channel is nil")
	}

	if err := app.Stop(ctx); err != nil {
		t.Fatalf("fx.Stop failed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestOrchestrator_NoTUIImport — source-scan regression guard
// ---------------------------------------------------------------------------

func TestOrchestrator_NoTUIImport(t *testing.T) {
	dirs := []string{
		filepath.Join("..", "ports"),
		filepath.Join("..", "orchestrator"),
	}

	for _, dir := range dirs {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatalf("glob %s: %v", dir, err)
		}

		for _, path := range files {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading %s: %v", path, err)
			}
			content := string(data)
			if strings.Contains(content, `"github.com/doordash-oss/agentic-orchestrator/internal/tui"`) ||
				strings.Contains(content, `"github.com/doordash-oss/agentic-orchestrator/internal/tui`) {
				t.Errorf("%s imports internal/tui — ports and orchestrator must not depend on the TUI layer", path)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// T24. TestNoErrNotImplementedMethods
//
// No method on *orchestrator.Orchestrator should have a body that simply
// returns ErrNotImplemented. Walks each production .go file and parses it with
// go/ast to find function declarations whose body is a single return statement
// returning an identifier named "ErrNotImplemented".
// ---------------------------------------------------------------------------

func TestPhase7_NoErrNotImplementedMethods(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "orchestrator", "*.go"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}

	fset := token.NewFileSet()
	var offenders []string

	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			if len(fn.Body.List) != 1 {
				continue
			}
			ret, ok := fn.Body.List[0].(*ast.ReturnStmt)
			if !ok || len(ret.Results) != 1 {
				continue
			}
			ident, ok := ret.Results[0].(*ast.Ident)
			if !ok {
				continue
			}
			if ident.Name == "ErrNotImplemented" {
				offenders = append(offenders, fn.Name.Name+" in "+path)
			}
		}
	}

	if len(offenders) > 0 {
		t.Errorf("found %d ErrNotImplemented-returning methods after Phase 7: %v", len(offenders), offenders)
	}
}
