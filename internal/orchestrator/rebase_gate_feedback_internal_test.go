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

// Real-git coverage of the phase-exit rebase gate feedback: the same per-repo
// mechanical checks the integration gate runs, surfaced as fix-round feedback
// at the moment the implement loop is about to declare success.

package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// TestRebaseGateFeedback_ViolationNamesRepoAndFact verifies a child whose
// branch does not contain the persisted target produces feedback naming the
// repo, the catalog title, and the catalog remediation hint — with no
// gate-code string leaking into the implementer-facing text.
func TestRebaseGateFeedback_ViolationNamesRepoAndFact(t *testing.T) {
	fx := newRebaseGateFixture(t, false) // target not merged -> ancestor violation
	o := fx.orchestrator()

	fb := o.rebaseGateFeedback(fx.child)
	if fb == "" {
		t.Fatalf("rebaseGateFeedback = empty, want a violation report")
	}
	if !strings.Contains(fb, fx.repos[0].name) {
		t.Errorf("feedback does not name the violated repo %q:\n%s", fx.repos[0].name, fb)
	}
	if !strings.Contains(fb, "Pass branch diverged from target") {
		t.Errorf("feedback does not carry the catalog title:\n%s", fb)
	}
	if !strings.Contains(fb, "Rebase the pass branch onto its target and retry, or discard the pass.") {
		t.Errorf("feedback does not carry the catalog remediation hint:\n%s", fb)
	}
	if strings.Contains(fb, "rebase_gate_") {
		t.Errorf("feedback leaks a gate-code string:\n%s", fb)
	}
}

// TestRebaseGateFeedback_ConflictMarkersListFiles verifies conflict-marker
// violations carry the catalog title, the offending files, and the catalog
// remediation hint in the feedback.
func TestRebaseGateFeedback_ConflictMarkersListFiles(t *testing.T) {
	fx := newRebaseGateFixture(t, true)
	marked := strings.Join([]string{
		"<" + "<<<<" + "<< ours",
		"our change",
		"=" + "=====" + "=",
		"their change",
		">" + ">>>>" + ">> theirs",
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(fx.repos[0].childWT, "conflicted.txt"), []byte(marked), 0o644); err != nil {
		t.Fatalf("write conflicted file: %v", err)
	}
	childIntegrationGit(t, fx.repos[0].childWT, "add", "conflicted.txt")
	childIntegrationGit(t, fx.repos[0].childWT, "commit", "-m", "add conflict markers")

	o := fx.orchestrator()
	fb := o.rebaseGateFeedback(fx.child)
	if fb == "" {
		t.Fatalf("rebaseGateFeedback = empty, want a conflict-marker violation")
	}
	if !strings.Contains(fb, "Unresolved conflict markers") {
		t.Errorf("feedback does not carry the catalog title:\n%s", fb)
	}
	if !strings.Contains(fb, "conflicted.txt") {
		t.Errorf("feedback does not list the conflicted file:\n%s", fb)
	}
	if !strings.Contains(fb, "Resolve the conflict markers in the worktree and retry.") {
		t.Errorf("feedback does not carry the catalog remediation hint:\n%s", fb)
	}
}

// TestRebaseGateFeedback_SatisfiedChildReturnsEmpty verifies a child that
// merged its persisted target cleanly produces no feedback.
func TestRebaseGateFeedback_SatisfiedChildReturnsEmpty(t *testing.T) {
	fx := newRebaseGateFixture(t, true)
	o := fx.orchestrator()

	if fb := o.rebaseGateFeedback(fx.child); fb != "" {
		t.Errorf("rebaseGateFeedback = %q, want empty for a satisfied child", fb)
	}
}

// TestRebaseGateFeedback_NonRebaseKindsEmpty verifies non-rebase features are
// never gated.
func TestRebaseGateFeedback_NonRebaseKindsEmpty(t *testing.T) {
	fx := newRebaseGateFixture(t, false)
	o := fx.orchestrator()

	if fb := o.rebaseGateFeedback(nil); fb != "" {
		t.Errorf("rebaseGateFeedback(nil) = %q, want empty", fb)
	}
	plain := &feature.Feature{ID: "plain"}
	if fb := o.rebaseGateFeedback(plain); fb != "" {
		t.Errorf("rebaseGateFeedback(non-child) = %q, want empty", fb)
	}
	refactor := &feature.Feature{
		ID:     "refactor-child",
		Parent: &feature.ChildRelationship{ParentID: "p", Kind: feature.ChildKindRefactor},
	}
	if fb := o.rebaseGateFeedback(refactor); fb != "" {
		t.Errorf("rebaseGateFeedback(refactor child) = %q, want empty", fb)
	}
}

// TestNew_PhaseExitGateWiredForRebaseChildrenOnly verifies orchestrator.New
// installs the phase-exit gate factory: rebase children get a live gate
// backed by rebaseGateFeedback, everything else gets nil (no-op).
func TestNew_PhaseExitGateWiredForRebaseChildrenOnly(t *testing.T) {
	fx := newRebaseGateFixture(t, false)
	pr := &agent.PhaseRunner{}
	New(Deps{Lifecycle: fx.mgr, Store: fx.store, Worktrees: fx.wm, PhaseRunner: pr}, Hooks{})

	if pr.PhaseExitGateFor == nil {
		t.Fatalf("PhaseExitGateFor not wired by New")
	}
	gate := pr.PhaseExitGateFor(fx.child)
	if gate == nil {
		t.Fatalf("gate = nil for a rebase child, want a live gate")
	}
	fb := gate()
	if fb == "" || !strings.Contains(fb, fx.repos[0].name) {
		t.Errorf("gate() = %q, want rebaseGateFeedback naming repo %q", fb, fx.repos[0].name)
	}

	if g := pr.PhaseExitGateFor(nil); g != nil {
		t.Errorf("gate for nil feature = non-nil, want nil")
	}
	if g := pr.PhaseExitGateFor(&feature.Feature{ID: "plain"}); g != nil {
		t.Errorf("gate for non-child feature = non-nil, want nil")
	}
	refactor := &feature.Feature{
		ID:     "refactor-child",
		Parent: &feature.ChildRelationship{ParentID: "p", Kind: feature.ChildKindRefactor},
	}
	if g := pr.PhaseExitGateFor(refactor); g != nil {
		t.Errorf("gate for refactor child = non-nil, want nil")
	}
}
