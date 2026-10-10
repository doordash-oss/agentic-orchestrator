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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// These tests drive the planning loop only through the two PhaseRunner entry
// points and read the result channel, so they pin what each artifact kind
// produces on disk and in the feature store independently of how the loop
// is structured.

const (
	planEntryResearchPath = "/inputs/research-input.md"
	planEntryRoadmapPath  = "/inputs/approved-roadmap.md"
)

var planEntryPhase = RoadmapPhase{
	Number: 1,
	Name:   "Entry Phase",
	Type:   "tdd-fill-in",
	Goal:   "Pin the planning loop through the entry point",
}

type planEntryKind struct {
	name         string
	dirParts     []string
	artifactFile string
	planText     string
	sessionBase  string
	artifactKey  string
	costKey      string
	creatorSpec  roleSpec
	reviserSpec  roleSpec
	notApproved  string
	run          func(pr *PhaseRunner, f *feature.Feature) (chan *PlanLoopResult, error)
	// initialPrompt and revisionPrompt call the unchanged builders with the
	// inputs the entry point is expected to forward.
	initialPrompt  func(f *feature.Feature) string
	revisionPrompt func(f *feature.Feature, previousPath, feedback string, attempt int, approvals []axisApproval) string
	// setMaxAttempts configures the attempt budget the entry point reads.
	setMaxAttempts func(pr *PhaseRunner, f *feature.Feature, n int)
}

func (k planEntryKind) artifactDir(stateDir string) string {
	return filepath.Join(append([]string{stateDir, "test-plan-001", "runs", "run-001"}, k.dirParts...)...)
}

func (k planEntryKind) runEntry(t *testing.T, pr *PhaseRunner, f *feature.Feature) *PlanLoopResult {
	t.Helper()
	resultCh, err := k.run(pr, f)
	return awaitPlanLoopResult(t, resultCh, err)
}

func (k planEntryKind) sessionID(attempt int) string {
	return fmt.Sprintf(k.sessionBase, attempt)
}

func planEntryKinds() []planEntryKind {
	return []planEntryKind{
		{
			name:         "roadmap",
			dirParts:     []string{"roadmap"},
			artifactFile: "roadmap.md",
			planText:     validRoadmapText(),
			sessionBase:  "test-plan-001-roadmap-%02d",
			artifactKey:  "roadmap",
			costKey:      "plan",
			creatorSpec:  roadmapCreatorRoleSpec,
			reviserSpec:  roadmapReviserRoleSpec,
			notApproved:  "roadmap not approved after %d attempts",
			run: func(pr *PhaseRunner, f *feature.Feature) (chan *PlanLoopResult, error) {
				return pr.RunPlanningWithValidation(f, planEntryResearchPath, nil)
			},
			initialPrompt: func(f *feature.Feature) string {
				return buildRoadmapPromptWithResearch(f, "", planEntryResearchPath, nil)
			},
			revisionPrompt: func(f *feature.Feature, previousPath, feedback string, attempt int, approvals []axisApproval) string {
				return buildRoadmapRevisionPrompt("", previousPath, feedback, attempt, approvals)
			},
			setMaxAttempts: func(_ *PhaseRunner, f *feature.Feature, n int) { f.MaxPlanIterations = n },
		},
		{
			name:         "phase plan",
			dirParts:     []string{"phase-01", "plan"},
			artifactFile: "plan.md",
			planText:     frontendPlanTextWithNoneRequiredTrio(),
			sessionBase:  "test-plan-001-phase-01-plan-%02d",
			artifactKey:  "phase-1-plan",
			costKey:      "phase-1-plan",
			creatorSpec:  phasePlanCreatorRoleSpec,
			reviserSpec:  phasePlanReviserRoleSpec,
			notApproved:  "phase plan not approved after %d attempts",
			run: func(pr *PhaseRunner, f *feature.Feature) (chan *PlanLoopResult, error) {
				return pr.RunPhasePlanning(f, planEntryRoadmapPath, planEntryPhase, nil, nil)
			},
			initialPrompt: func(f *feature.Feature) string {
				return buildPhasePlanPromptWithResearch(f, planEntryRoadmapPath, "", planEntryPhase, nil)
			},
			revisionPrompt: func(f *feature.Feature, previousPath, feedback string, attempt int, approvals []axisApproval) string {
				return buildPhasePlanRevisionPrompt(f, "", previousPath, feedback, planEntryPhase, attempt, approvals)
			},
			setMaxAttempts: func(pr *PhaseRunner, _ *feature.Feature, n int) { setPhasePlanDefaultAttempts(pr, n) },
		},
	}
}

func setPhasePlanDefaultAttempts(pr *PhaseRunner, n int) {
	pr.Config = &config.Config{Defaults: config.DefaultsConfig{MaxPhasePlanIterations: n}}
}

func newPlanEntryRunner(sm ports.SessionManager, store ports.FeatureStore, stateDir string, buildSession buildSessionFunc) *PhaseRunner {
	return &PhaseRunner{
		SessionManager:             sm,
		FeatureStore:               store,
		StateDir:                   stateDir,
		DangerouslySkipPermissions: true,
		BuildSessionFn:             buildSession,
	}
}

func awaitPlanLoopResult(t *testing.T, resultCh chan *PlanLoopResult, err error) *PlanLoopResult {
	t.Helper()
	if err != nil {
		t.Fatalf("starting planning loop: %v", err)
	}
	select {
	case result := <-resultCh:
		if result == nil {
			t.Fatal("planning loop returned a nil result")
		}
		return result
	case <-time.After(3 * time.Minute):
		t.Fatal("planning loop did not return")
		return nil
	}
}

func runRoadmapEntry(t *testing.T, pr *PhaseRunner, f *feature.Feature) *PlanLoopResult {
	t.Helper()
	resultCh, err := pr.RunPlanningWithValidation(f, "", nil)
	return awaitPlanLoopResult(t, resultCh, err)
}

func runPhasePlanEntry(t *testing.T, pr *PhaseRunner, f *feature.Feature, phase RoadmapPhase, priorPhasePlanPaths []string) *PlanLoopResult {
	t.Helper()
	resultCh, err := pr.RunPhasePlanning(f, "", phase, nil, priorPhasePlanPaths)
	return awaitPlanLoopResult(t, resultCh, err)
}

type planEntryFixture struct {
	stateDir    string
	workDir     string
	scriptsDir  string
	artifactDir string
	store       *feature.Store
	feature     *feature.Feature
	sm          *session.Manager
}

func newPlanEntryFixture(t *testing.T, kind planEntryKind) *planEntryFixture {
	t.Helper()
	stateDir := t.TempDir()
	fx := &planEntryFixture{
		stateDir:    stateDir,
		workDir:     filepath.Join(stateDir, "work"),
		scriptsDir:  filepath.Join(stateDir, "scripts"),
		artifactDir: kind.artifactDir(stateDir),
		store:       feature.NewStore(stateDir),
		feature:     newTestPlanFeature(t, filepath.Join(stateDir, "work")),
	}
	for _, d := range []string{fx.workDir, fx.scriptsDir, fx.artifactDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", d, err)
		}
	}
	fx.feature.Pipeline = feature.PipelineLarge
	fx.sm = session.NewManager(make(chan interface{}, 100))
	t.Cleanup(fx.sm.Shutdown)
	return fx
}

func (fx *planEntryFixture) save(t *testing.T) {
	t.Helper()
	if err := fx.store.Save(fx.feature); err != nil {
		t.Fatalf("Save(feature): %v", err)
	}
}

func (fx *planEntryFixture) writeArtifactSnippet(kind planEntryKind) string {
	return fmt.Sprintf("cat > %q <<'PLANENTRYEOF'\n%s\nPLANENTRYEOF\n", filepath.Join(fx.artifactDir, kind.artifactFile), kind.planText)
}

func (fx *planEntryFixture) readFile(t *testing.T, rel ...string) string {
	t.Helper()
	path := filepath.Join(append([]string{fx.artifactDir}, rel...)...)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	return string(data)
}

func (fx *planEntryFixture) load(t *testing.T) *feature.Feature {
	t.Helper()
	loaded, err := fx.store.Load(fx.feature.ID)
	if err != nil {
		t.Fatalf("Load(feature): %v", err)
	}
	return loaded
}

// plannerCostRecords returns the session-cost records of planner sessions in
// launch order; validator helpers record under the review observer phase.
func plannerCostRecords(f *feature.Feature) []feature.SessionCostRecord {
	var records []feature.SessionCostRecord
	for _, record := range f.SessionCosts {
		if record.ObserverPhase == "plan" {
			records = append(records, record)
		}
	}
	return records
}

func plannerBuildOpts(captured []BuildSessionOpts) []BuildSessionOpts {
	var planner []BuildSessionOpts
	for _, opts := range captured {
		if !isReviewHelper(opts.PermHandler) {
			planner = append(planner, opts)
		}
	}
	return planner
}

func wantPlannerCostRecords(kind planEntryKind, attempts int) []feature.SessionCostRecord {
	var records []feature.SessionCostRecord
	for attempt := 1; attempt <= attempts; attempt++ {
		records = append(records, feature.SessionCostRecord{
			SessionID:     kind.sessionID(attempt),
			PhaseKey:      kind.costKey,
			ObserverPhase: "plan",
			RepoName:      "test-repo",
			CostUSD:       0.001,
		})
	}
	return records
}

func TestPlanningEntryPointsApproveFirstAttempt(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	for _, kind := range planEntryKinds() {
		t.Run(kind.name, func(t *testing.T) {
			fx := newPlanEntryFixture(t, kind)
			fx.save(t)
			planScript := testutil.WriteScript(t, fx.scriptsDir, "plan.sh",
				testutil.JSONLInit+"\n"+fx.writeArtifactSnippet(kind)+testutil.JSONLSuccess+"\n")
			criticScript := testutil.WriteScript(t, fx.scriptsDir, "critic.sh",
				testutil.JSONLInit+"\n"+testutil.WriteAnyValidatorApproved(fx.stateDir)+"\n"+testutil.JSONLSuccess+"\n")
			buildSession, captured := capturingBuildSession(planScript, criticScript)
			pr := newPlanEntryRunner(fx.sm, fx.store, fx.stateDir, buildSession)

			result := kind.runEntry(t, pr, fx.feature)

			if want := (PlanLoopResult{FinalStatus: "approved", Iterations: 1}); *result != want {
				t.Fatalf("result = %+v, want %+v", *result, want)
			}
			if got, want := fx.readFile(t, "attempt-01", "meta.yaml"), "attempt: 1\nagent_status: SUCCESS\nreview_status: APPROVED\n"; got != want {
				t.Errorf("attempt-01/meta.yaml = %q, want %q", got, want)
			}
			if got, want := fx.readFile(t, "attempt-01", "user-prompt.md"), kind.initialPrompt(fx.feature); got != want {
				t.Errorf("attempt-01/user-prompt.md differs from the initial builder output:\n got: %q\nwant: %q", got, want)
			}
			planner := plannerBuildOpts(*captured)
			if len(planner) != 1 {
				t.Fatalf("planner sessions = %d, want 1", len(planner))
			}
			wantSystem := buildRoleSystemPrompt(roleSystemPromptInput{Spec: kind.creatorSpec, IterationDir: filepath.Join(fx.artifactDir, "attempt-01")})
			if planner[0].SystemPrompt != wantSystem {
				t.Errorf("planner system prompt differs from the creator role spec prompt:\n got: %q\nwant: %q", planner[0].SystemPrompt, wantSystem)
			}
			if planner[0].Model != "planner" || planner[0].Phase != feature.PhasePlan {
				t.Errorf("planner opts Model=%q Phase=%v, want planner/plan", planner[0].Model, planner[0].Phase)
			}

			loaded := fx.load(t)
			if got := plannerCostRecords(loaded); !reflect.DeepEqual(got, wantPlannerCostRecords(kind, 1)) {
				t.Errorf("planner cost records = %+v, want %+v", got, wantPlannerCostRecords(kind, 1))
			}
			wantArtifact := filepath.Join(fx.artifactDir, kind.artifactFile)
			if got := loaded.Artifacts[kind.artifactKey]; got != wantArtifact {
				t.Errorf("Artifacts[%q] = %q, want %q", kind.artifactKey, got, wantArtifact)
			}
			if loaded.PlanIteration != 1 {
				t.Errorf("PlanIteration = %d, want 1", loaded.PlanIteration)
			}
			if wantFrontend := kind.artifactKey != "roadmap"; loaded.RoadmapPhaseFrontend(1) != wantFrontend {
				t.Errorf("RoadmapPhaseFrontend(1) = %v, want %v", loaded.RoadmapPhaseFrontend(1), wantFrontend)
			}
			if loaded.ValidatingPlan {
				t.Error("ValidatingPlan left set after the loop returned")
			}
		})
	}
}

func TestPlanningEntryPointsExhaustAttemptsWithoutApproval(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	wantMeta := map[string][2]string{
		"roadmap": {
			"attempt: 1\nagent_status: SUCCESS\nreview_status: CHANGES_REQUESTED\n",
			"attempt: 2\nagent_status: SUCCESS\nreview_status: CHANGES_REQUESTED\n",
		},
		"phase plan": {
			"attempt: 1\nagent_status: SUCCESS\nreview_status: CHANGES_REQUESTED\naxis_verdicts:\n    scope: CHANGES_REQUESTED\n    structural: CHANGES_REQUESTED\naxis_digests:\n    scope: c3d6dd4c624afe9f33e4d367227f617e1e97a90e1e3901d9b1654915e2a0b22b\n    structural: c3d6dd4c624afe9f33e4d367227f617e1e97a90e1e3901d9b1654915e2a0b22b\n",
			"attempt: 2\nagent_status: SUCCESS\nreview_status: CHANGES_REQUESTED\naxis_verdicts:\n    scope: CHANGES_REQUESTED\n    structural: CHANGES_REQUESTED\naxis_digests:\n    scope: c3d6dd4c624afe9f33e4d367227f617e1e97a90e1e3901d9b1654915e2a0b22b\n    structural: c3d6dd4c624afe9f33e4d367227f617e1e97a90e1e3901d9b1654915e2a0b22b\n",
		},
	}
	for _, kind := range planEntryKinds() {
		t.Run(kind.name, func(t *testing.T) {
			fx := newPlanEntryFixture(t, kind)
			fx.feature.RiskLevel = feature.RiskLow
			planScript := testutil.WriteScript(t, fx.scriptsDir, "plan.sh",
				testutil.JSONLInit+"\n"+fx.writeArtifactSnippet(kind)+testutil.JSONLSuccess+"\n")
			criticScript := testutil.WriteScript(t, fx.scriptsDir, "critic.sh",
				testutil.JSONLInit+"\n"+testutil.WriteAnyValidatorChangesRequested(fx.stateDir, "- **High**: the plan is still missing the rollback step")+"\n"+testutil.JSONLSuccess+"\n")
			buildSession, captured := capturingBuildSession(planScript, criticScript)
			pr := newPlanEntryRunner(fx.sm, fx.store, fx.stateDir, buildSession)
			kind.setMaxAttempts(pr, fx.feature, 2)
			fx.save(t)

			result := kind.runEntry(t, pr, fx.feature)

			want := PlanLoopResult{FinalStatus: "needs_human_review", Iterations: 2, LastError: fmt.Sprintf(kind.notApproved, 2)}
			if *result != want {
				t.Fatalf("result = %+v, want %+v", *result, want)
			}
			for i, wantContent := range wantMeta[kind.name] {
				if got := fx.readFile(t, fmt.Sprintf("attempt-%02d", i+1), "meta.yaml"); got != wantContent {
					t.Errorf("attempt-%02d/meta.yaml = %q, want %q", i+1, got, wantContent)
				}
			}
			artifactPath := filepath.Join(fx.artifactDir, kind.artifactFile)
			feedback := fx.readFile(t, "attempt-01", "validation-feedback.md")
			if !strings.Contains(feedback, "the plan is still missing the rollback step") {
				t.Fatalf("attempt-01/validation-feedback.md missing critic finding:\n%s", feedback)
			}
			wantRevision := kind.revisionPrompt(fx.feature, artifactPath, feedback, 2, nil)
			if got := fx.readFile(t, "attempt-02", "user-prompt.md"); got != wantRevision {
				t.Errorf("attempt-02/user-prompt.md differs from the revision builder output:\n got: %q\nwant: %q", got, wantRevision)
			}
			planner := plannerBuildOpts(*captured)
			if len(planner) != 2 {
				t.Fatalf("planner sessions = %d, want 2", len(planner))
			}
			for i, spec := range []roleSpec{kind.creatorSpec, kind.reviserSpec} {
				wantSystem := buildRoleSystemPrompt(roleSystemPromptInput{Spec: spec, IterationDir: filepath.Join(fx.artifactDir, fmt.Sprintf("attempt-%02d", i+1))})
				if planner[i].SystemPrompt != wantSystem {
					t.Errorf("attempt %d system prompt differs from the %s role spec prompt", i+1, spec.Role)
				}
			}

			loaded := fx.load(t)
			if got := plannerCostRecords(loaded); !reflect.DeepEqual(got, wantPlannerCostRecords(kind, 2)) {
				t.Errorf("planner cost records = %+v, want %+v", got, wantPlannerCostRecords(kind, 2))
			}
			if got := loaded.Artifacts[kind.artifactKey]; got != artifactPath {
				t.Errorf("Artifacts[%q] = %q, want %q", kind.artifactKey, got, artifactPath)
			}
			if loaded.RoadmapPhaseFrontend(1) {
				t.Error("an unapproved plan recorded the roadmap-phase frontend flag")
			}
		})
	}
}

// TestPlanningEntryPointsAttemptBudget pins each entry point's attempt
// computation: the roadmap takes the feature's plan-iteration limit (ten when
// unset) and ignores the phase-plan default; the phase plan takes the
// configured phase-plan default (ten when unset), raised by the feature limit.
func TestPlanningEntryPointsAttemptBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	kinds := planEntryKinds()
	roadmap, phasePlan := kinds[0], kinds[1]
	cases := []struct {
		name          string
		kind          planEntryKind
		configDefault int
		featureLimit  int
		wantAttempts  int
	}{
		{name: "roadmap default", kind: roadmap, wantAttempts: 10},
		{name: "roadmap feature limit ignores phase default", kind: roadmap, configDefault: 1, featureLimit: 2, wantAttempts: 2},
		{name: "phase plan default", kind: phasePlan, wantAttempts: 10},
		{name: "phase plan configured default", kind: phasePlan, configDefault: 1, wantAttempts: 1},
		{name: "phase plan raised by feature limit", kind: phasePlan, configDefault: 1, featureLimit: 2, wantAttempts: 2},
		{name: "phase plan feature limit below default", kind: phasePlan, configDefault: 3, featureLimit: 2, wantAttempts: 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newPlanEntryFixture(t, tc.kind)
			fx.feature.MaxPlanIterations = tc.featureLimit
			fx.save(t)
			// The planner never writes its artifact, so every attempt ends in
			// a contract violation and the loop runs its whole budget.
			planScript := testutil.WriteScript(t, fx.scriptsDir, "plan.sh",
				testutil.JSONLInit+"\n"+testutil.JSONLSuccess+"\n")
			pr := newPlanEntryRunner(fx.sm, fx.store, fx.stateDir, mockBuildSession(planScript, ""))
			if tc.configDefault > 0 {
				setPhasePlanDefaultAttempts(pr, tc.configDefault)
			}

			result := tc.kind.runEntry(t, pr, fx.feature)

			if result.FinalStatus != BoundedHelperStatusProtocolViolation || result.Iterations != tc.wantAttempts {
				t.Fatalf("result = %+v, want protocol_violation after %d attempts", *result, tc.wantAttempts)
			}
			wantRole := tc.kind.reviserSpec.Role
			if tc.wantAttempts == 1 {
				wantRole = tc.kind.creatorSpec.Role
			}
			lastDir := filepath.Join(fx.artifactDir, fmt.Sprintf("attempt-%02d", tc.wantAttempts))
			if !strings.Contains(result.LastError, string(wantRole)) || !strings.Contains(result.LastError, lastDir) {
				t.Errorf("LastError = %q, want %s violation in %s", result.LastError, wantRole, lastDir)
			}
			for attempt := 1; attempt <= tc.wantAttempts; attempt++ {
				want := fmt.Sprintf("attempt: %d\nagent_status: SUCCESS\nreview_status: CHANGES_REQUESTED\n", attempt)
				if got := fx.readFile(t, fmt.Sprintf("attempt-%02d", attempt), "meta.yaml"); got != want {
					t.Errorf("attempt-%02d/meta.yaml = %q, want %q", attempt, got, want)
				}
			}
			if _, err := os.Stat(filepath.Join(fx.artifactDir, fmt.Sprintf("attempt-%02d", tc.wantAttempts+1))); !os.IsNotExist(err) {
				t.Errorf("attempt beyond the budget exists (stat err = %v)", err)
			}
			if got := plannerCostRecords(fx.load(t)); !reflect.DeepEqual(got, wantPlannerCostRecords(tc.kind, tc.wantAttempts)) {
				t.Errorf("planner cost records = %+v, want %+v", got, wantPlannerCostRecords(tc.kind, tc.wantAttempts))
			}
		})
	}
}

// fakeStartSessionManager replaces only session start; everything else the
// planning loop reaches on the manager is the shutdown probe.
type fakeStartSessionManager struct {
	ports.SessionManager
	mu    sync.Mutex
	ids   []string
	start func(id string) (ports.SessionHandle, error)
}

func (m *fakeStartSessionManager) StartSession(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*ports.SessionOpts) (ports.SessionHandle, error) {
	m.mu.Lock()
	m.ids = append(m.ids, id)
	m.mu.Unlock()
	return m.start(id)
}

func (m *fakeStartSessionManager) IsShuttingDown() bool { return false }

func (m *fakeStartSessionManager) sessionIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.ids...)
}

func stubPlannerBuildSession(err error) buildSessionFunc {
	return func(opts BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
		if err != nil {
			return nil, nil, nil, err
		}
		return []string{"echo", "unused"}, nil, &ports.SessionOpts{PIDDir: opts.PIDDir}, nil
	}
}

func TestPlanningEntryPointsFailureTexts(t *testing.T) {
	failedPlanner := func(string) (ports.SessionHandle, error) {
		sess := newTerminalStatusTestSession(ports.SessionFailed)
		// Spent tokens mark the failure as real work, not a retryable
		// infrastructure blip.
		sess.AccumulatedUsageVal = llm.Usage{InputTokens: 10}
		return sess, nil
	}
	cases := []struct {
		name         string
		buildErr     error
		start        func(string) (ports.SessionHandle, error)
		wantLastErr  map[string]string
		wantMeta     string
		wantSessions int
	}{
		{
			name:     "session build fails",
			buildErr: errors.New("no provider"),
			wantLastErr: map[string]string{
				"roadmap":    "building roadmap session (attempt 1): no provider",
				"phase plan": "building phase plan session (attempt 1): no provider",
			},
		},
		{
			name:  "session start fails",
			start: func(string) (ports.SessionHandle, error) { return nil, errors.New("spawn refused") },
			wantLastErr: map[string]string{
				"roadmap":    "starting roadmap session (attempt 1): spawn refused",
				"phase plan": "starting phase plan session (attempt 1): spawn refused",
			},
			wantSessions: 1,
		},
		{
			name:  "planner session fails",
			start: failedPlanner,
			wantLastErr: map[string]string{
				"roadmap":    "roadmap session did not complete successfully",
				"phase plan": "phase plan session did not complete",
			},
			wantMeta:     "attempt: 1\nsession_attempt: 1\nagent_status: FAILED\nreview_status: \"\"\n",
			wantSessions: 1,
		},
	}
	for _, tc := range cases {
		for _, kind := range planEntryKinds() {
			t.Run(tc.name+"/"+kind.name, func(t *testing.T) {
				fx := newPlanEntryFixture(t, kind)
				fx.save(t)
				sm := &fakeStartSessionManager{start: tc.start}
				pr := newPlanEntryRunner(sm, fx.store, fx.stateDir, stubPlannerBuildSession(tc.buildErr))

				result := kind.runEntry(t, pr, fx.feature)

				if result.FinalStatus != "failed" || result.LastError != tc.wantLastErr[kind.name] {
					t.Fatalf("result = %+v, want failed with %q", *result, tc.wantLastErr[kind.name])
				}
				if got := sm.sessionIDs(); len(got) != tc.wantSessions || (len(got) == 1 && got[0] != kind.sessionID(1)) {
					t.Errorf("started sessions = %v, want %d session(s) named %s", got, tc.wantSessions, kind.sessionID(1))
				}
				if tc.wantMeta != "" {
					if got := fx.readFile(t, "attempt-01", "meta.yaml"); got != tc.wantMeta {
						t.Errorf("attempt-01/meta.yaml = %q, want %q", got, tc.wantMeta)
					}
				}
			})
		}
	}
}

// TestPlanningEntryPointsRecordPlannerCostByInlineRule pins the planner cost
// rule: a record only when the USD total is positive, keyed by the kind's
// phase key even when the feature has an active timing key, and carrying
// neither the cost source nor credits.
func TestPlanningEntryPointsRecordPlannerCostByInlineRule(t *testing.T) {
	credits := int64(7_000_000)
	cases := []struct {
		name    string
		usage   llm.Usage
		wantUSD float64
	}{
		{name: "credits only", usage: llm.Usage{CostSource: "provider", CostCreditsMicros: &credits}},
		{name: "usd with source and credits", usage: llm.Usage{CostUSD: 0.25, CostSource: "provider", CostCreditsMicros: &credits}, wantUSD: 0.25},
	}
	for _, tc := range cases {
		for _, kind := range planEntryKinds() {
			t.Run(tc.name+"/"+kind.name, func(t *testing.T) {
				fx := newPlanEntryFixture(t, kind)
				fx.feature.Pipeline = feature.PipelineMedium
				fx.feature.ActiveTimingKey = "active-timing-key"
				fx.save(t)
				sm := &fakeStartSessionManager{start: func(string) (ports.SessionHandle, error) {
					if err := os.WriteFile(filepath.Join(fx.artifactDir, kind.artifactFile), []byte(kind.planText), 0o644); err != nil {
						return nil, err
					}
					sess := newUtilityTestSession()
					sess.setRootIntent(validSuccessCompletionIntent())
					sess.CostVal = &llm.ResultMessage{Subtype: testResultSuccessValue, StopReason: "end_turn"}
					sess.AccumulatedUsageVal = tc.usage
					sess.StatusChVal <- agentStatusSuccess
					return sess, nil
				}}
				pr := newPlanEntryRunner(sm, fx.store, fx.stateDir, stubPlannerBuildSession(nil))

				result := kind.runEntry(t, pr, fx.feature)

				if result.FinalStatus != "approved" {
					t.Fatalf("result = %+v, want approved", *result)
				}
				var want []feature.SessionCostRecord
				if tc.wantUSD > 0 {
					want = []feature.SessionCostRecord{{
						SessionID:     kind.sessionID(1),
						PhaseKey:      kind.costKey,
						ObserverPhase: "plan",
						RepoName:      "test-repo",
						CostUSD:       tc.wantUSD,
					}}
				}
				if got := fx.load(t).SessionCosts; !reflect.DeepEqual(got, want) {
					t.Errorf("SessionCosts = %+v, want %+v", got, want)
				}
			})
		}
	}
}

// planModelProvider is a catalog provider whose only model is the catalog
// default for every role; it resolves no other model name.
type planModelProvider struct {
	effortCatalogProvider
}

func (p *planModelProvider) MatchesModel(model string) bool { return model == "catalog-planner" }
func (p *planModelProvider) CompletionToolName() string     { return "catalog_complete_tool" }
func (p *planModelProvider) AskingQuestionsClause() string  { return "CATALOG-ASKING-CLAUSE" }

// TestPlanningEntryPointsPassRawPlanningModel pins the model rule: the
// session receives the feature's raw planning model while the entry point
// resolves the role model only for the asking clause, completion tool and
// effort.
func TestPlanningEntryPointsPassRawPlanningModel(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	for _, kind := range planEntryKinds() {
		t.Run(kind.name, func(t *testing.T) {
			fx := newPlanEntryFixture(t, kind)
			fx.feature.Pipeline = feature.PipelineMedium
			fx.feature.Models.Planning = ""
			fx.feature.Effort.Planning = "low"
			fx.save(t)
			planScript := testutil.WriteScript(t, fx.scriptsDir, "plan.sh",
				testutil.JSONLInit+"\n"+fx.writeArtifactSnippet(kind)+testutil.JSONLSuccess+"\n")
			buildSession, captured := capturingBuildSession(planScript, "")
			pr := newPlanEntryRunner(fx.sm, fx.store, fx.stateDir, buildSession)
			reg := llm.NewRegistry()
			reg.Register(&planModelProvider{effortCatalogProvider{models: []llm.ModelInfo{{
				ID:                 "catalog-planner",
				EffortCapabilities: []llm.EffortLevel{llm.EffortLow, llm.EffortMedium, llm.EffortHigh},
			}}}})
			pr.Registry = reg

			result := kind.runEntry(t, pr, fx.feature)

			if result.FinalStatus != "approved" {
				t.Fatalf("result = %+v, want approved", *result)
			}
			planner := plannerBuildOpts(*captured)
			if len(planner) != 1 {
				t.Fatalf("planner sessions = %d, want 1", len(planner))
			}
			opts := planner[0]
			if opts.Model != "" {
				t.Errorf("session Model = %q, want the raw (empty) planning model", opts.Model)
			}
			if opts.EffortLevel != llm.EffortLow {
				t.Errorf("session EffortLevel = %q, want low resolved against the catalog-planner capabilities", opts.EffortLevel)
			}
			wantSystem := buildRoleSystemPrompt(roleSystemPromptInput{
				Spec:           kind.creatorSpec,
				IterationDir:   filepath.Join(fx.artifactDir, "attempt-01"),
				AskingClause:   "CATALOG-ASKING-CLAUSE",
				CompletionTool: "catalog_complete_tool",
			})
			if opts.SystemPrompt != wantSystem {
				t.Errorf("system prompt was not built from the resolved role model's asking clause and completion tool:\n%s", opts.SystemPrompt)
			}
		})
	}
}
