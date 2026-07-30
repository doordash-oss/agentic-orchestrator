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

package e2e

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/internal/tui"
	"github.com/doordash-oss/agentic-orchestrator/internal/tui/tuitest"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// tuiJourneyFixtureOptions parameterizes the shared TUI journey rig: the
// parent record identity/branch, its repositories and worktree policy, the
// Review axes seeded on it, and an optional worktree-operator wrapper (used
// to inject deterministic cleanup failures).
type tuiJourneyFixtureOptions struct {
	// ParentID doubles as the parent slug; the feature branch is
	// "feature/<ParentID>". ParentName defaults to ParentID.
	ParentID   string
	ParentName string
	// RepoNames lists the parent repositories (default: ["repoA"]).
	RepoNames []string
	// ParentSelfWorktree records WorktreePath = main checkout on the parent
	// repos — the parent was published out of its own checkout and has no
	// disposable feature worktree yet. The cascade-delete journey leaves
	// this false so the parent's cascade cleanup sees no recorded worktree.
	ParentSelfWorktree bool
	// ResetCheckoutToBase returns every repo checkout to the base branch
	// after the push: the cascade cleanup removes the recorded feature
	// branch, which a checked-out branch would resist deleting.
	ResetCheckoutToBase bool
	// Review axes seeded on the parent (zero values keep the defaults).
	Models       config.ModelConfig
	Effort       config.EffortConfig
	Checkpoints  feature.Checkpoints
	Inquireness  feature.Inquireness
	RiskLevel    feature.RiskLevel
	ExitCriteria string
	// WrapWorktrees optionally wraps the worktree operator handed to the
	// orchestrator (e.g. a deterministic cleanup failure injector).
	WrapWorktrees func(wm *git.WorktreeManager) ports.WorktreeOperator
	// WrapOrchestratorStore optionally wraps the feature store handed to
	// the orchestrator (e.g. a deterministic record-mutation failure
	// injector); the REST server keeps the unwrapped store.
	WrapOrchestratorStore func(s *feature.Store) ports.FeatureStore
}

// tuiJourneyFixture is the fully wired rig: real git repositories, feature
// store/manager, session manager, scripted phase runner (with the blocked
// phase-plan lock file playbook), orchestrator, live HTTP server, REST
// client, and event forwarding — everything a TUI journey needs before it
// drives bubbletea keys through the real API-driven model.
type tuiJourneyFixture struct {
	Tmp       string
	StateDir  string
	RepoDirs  map[string]string
	Parent    *feature.Feature
	Store     *feature.Store
	Worktrees *git.WorktreeManager
	Server    *httptest.Server
	Client    *server.Client
	// BlockFile holds phase-plan sessions open while it exists, giving the
	// journey a deterministic running-phase window.
	BlockFile string
}

// newTUIJourneyFixture builds the rig shared by every refactor TUI journey.
func newTUIJourneyFixture(t *testing.T, opts tuiJourneyFixtureOptions) *tuiJourneyFixture {
	t.Helper()
	if opts.ParentID == "" {
		t.Fatal("tuiJourneyFixtureOptions.ParentID is required")
	}
	if len(opts.RepoNames) == 0 {
		opts.RepoNames = []string{"repoA"}
	}
	if opts.ParentName == "" {
		opts.ParentName = opts.ParentID
	}

	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}
	wtBaseDir := filepath.Join(tmp, "worktrees")

	branch := "feature/" + opts.ParentID
	repoDirs := map[string]string{}
	for _, repo := range opts.RepoNames {
		dir := testutil.InitGitRepo(t)
		testutil.InitBareRemote(t, dir)
		journeyGit(t, dir, "checkout", "-b", branch)
		writeJourneyFile(t, dir, "base.txt", "v1\n")
		journeyGit(t, dir, "add", "base.txt")
		journeyGit(t, dir, "commit", "-m", repo+" base commit")
		journeyGit(t, dir, "push", "-u", "origin", branch)
		repoDirs[repo] = dir
	}
	if opts.ResetCheckoutToBase {
		for _, dir := range repoDirs {
			journeyGit(t, dir, "checkout", "main")
		}
	}

	store := feature.NewStore(stateDir)
	publishable := true
	repos := make([]feature.FeatureRepo, 0, len(opts.RepoNames))
	repoStates := map[string]*feature.RepoState{}
	for _, name := range opts.RepoNames {
		repo := feature.FeatureRepo{
			Name:        name,
			Path:        repoDirs[name],
			Branch:      branch,
			BaseBranch:  "main",
			Publishable: &publishable,
		}
		if opts.ParentSelfWorktree {
			repo.WorktreePath = repoDirs[name]
		}
		repos = append(repos, repo)
		repoStates[name] = &feature.RepoState{Touched: true}
	}
	parent := &feature.Feature{
		ID:            opts.ParentID,
		Name:          opts.ParentName,
		Slug:          opts.ParentID,
		Status:        feature.StatusPublished,
		CurrentPhase:  feature.PhasePublish,
		Created:       time.Now().UTC().Truncate(time.Second),
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Models:        opts.Models,
		Effort:        opts.Effort,
		Inquireness:   opts.Inquireness,
		RiskLevel:     opts.RiskLevel,
		ExitCriteria:  opts.ExitCriteria,
		Checkpoints:   opts.Checkpoints,
		Repos:         repos,
		RepoStates:    repoStates,
	}
	if err := store.Save(parent); err != nil {
		t.Fatalf("Save(parent) error = %v", err)
	}

	wm := git.NewWorktreeManager(wtBaseDir)
	mgr := feature.NewManager(store, config.NewDefault())
	mgr.Worktrees = wm
	mgr.Cleanliness = journeyCleanliness(wm)

	serverEvents := make(chan interface{}, 512)
	stopForwarding := make(chan struct{})
	t.Cleanup(func() { close(stopForwarding) })

	sm := session.NewManager(serverEvents)
	t.Cleanup(sm.Shutdown)
	pr := journeyChildPhaseRunner(t, sm, store, stateDir)

	// Hold phase-plan sessions open while the lock file exists so journeys
	// get a deterministic running-phase window instead of racing the instant
	// scripted completion.
	blockFile := filepath.Join(tmp, "block-phase-plan")
	scriptsDir := t.TempDir()
	baseBuild := pr.BuildSessionFn
	pr.BuildSessionFn = func(opts agent.BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
		args, env, sessOpts, err := baseBuild(opts)
		if err != nil || opts.MarkerPath == "" || strings.Contains(opts.MarkerPath, "roadmap") {
			return args, env, sessOpts, err
		}
		wrapper := testutil.WriteScript(t, scriptsDir, filepath.Base(args[1])+"-blocked.sh",
			fmt.Sprintf("while [ -f %q ]; do sleep 0.1; done\nexec bash %q\n", blockFile, args[1]))
		return []string{"bash", wrapper}, env, sessOpts, err
	}

	var wtOps ports.WorktreeOperator = wm
	if opts.WrapWorktrees != nil {
		wtOps = opts.WrapWorktrees(wm)
	}
	var orchStore ports.FeatureStore = store
	if opts.WrapOrchestratorStore != nil {
		orchStore = opts.WrapOrchestratorStore(store)
	}

	orch := orchestrator.New(orchestrator.Deps{
		Lifecycle:   mgr,
		Store:       orchStore,
		Sessions:    sm,
		Recovery:    session.NewRecoveryAdapter(stateDir, mgr),
		Publisher:   &git.PublishAdapter{},
		Worktrees:   wtOps,
		PhaseRunner: pr,
		CmdRunner:   pr.CommandRunner,
		Cleanliness: journeyCleanliness(wm),
	}, orchestrator.Hooks{})
	t.Cleanup(func() {
		_ = orch.Shutdown()
		orch.WaitForCycles()
	})
	go func() {
		for {
			select {
			case ev := <-orch.Events():
				select {
				case serverEvents <- ev:
				default:
				}
			case <-stopForwarding:
				return
			}
		}
	}()

	// The runtime config served to the TUI carries non-empty workspace roots
	// so the model boots into the dashboard rather than the welcome screen,
	// and the git-backed freshness provider lets the server flag entry
	// conditions exactly like production.
	runtimeCfg := config.NewDefault()
	runtimeCfg.WorkspaceRoots = []string{tmp}

	srv := httptest.NewServer(server.NewHandler(server.HandlerOptions{
		Runtime:               server.RuntimeIdentity{RuntimeDir: tmp, StateDir: stateDir},
		Features:              store,
		FeatureStore:          store,
		Freshness:             journeyFreshnessProvider{},
		Config:                runtimeCfg,
		Sessions:              sm,
		Events:                serverEvents,
		Mutations:             &journeyMutationTarget{mgr: mgr, orch: orch},
		Cleanliness:           journeyCleanliness(wm),
		DisableHostValidation: true,
	}))
	t.Cleanup(srv.Close)
	client, err := server.NewClient(server.ClientOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	return &tuiJourneyFixture{
		Tmp:       tmp,
		StateDir:  stateDir,
		RepoDirs:  repoDirs,
		Parent:    parent,
		Store:     store,
		Worktrees: wm,
		Server:    srv,
		Client:    client,
		BlockFile: blockFile,
	}
}

// NewHarness cold-boots the real API-driven TUI model against the fixture's
// live server at the deterministic 140x42 journey size.
func (fx *tuiJourneyFixture) NewHarness(t *testing.T) *tuitest.AppHarness {
	t.Helper()
	h, err := tuitest.NewAppHarness(t.Context(), fx.Client, tui.APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAppHarness() error = %v", err)
	}
	t.Cleanup(h.Close)
	h.Resize(140, 42)
	return h
}

// journeyLaunchRefactor drives one refactor child through the TUI wizard and
// returns once setup parked it at Created.
func journeyLaunchRefactor(t *testing.T, fx *tuiJourneyFixture, h *tuitest.AppHarness, name, desc string) string {
	t.Helper()
	h.Refresh()
	h.PressKey(tea.KeyPressMsg{Code: 'F', Text: "F"})
	if !h.WizardActive() {
		t.Fatalf("Shift+F did not open the refactor wizard for %q; status = %q", name, h.StatusMessage())
	}
	h.Type(name)
	h.Press(tea.KeyEnter) // name → description focus
	h.Type(desc)
	h.Press(tea.KeyEnter) // advance: What → Pipeline (Where skipped in refactor mode)
	h.Press(tea.KeyUp)    // pipeline options [medium, large, moonshot]; default cursor large → medium
	h.Press(tea.KeyEnter) // advance: Pipeline → Review
	h.PressKey(tea.KeyPressMsg{Code: 'G', Text: "G"})
	childID := h.SelectedFeatureID()
	if childID == "" || childID == fx.Parent.ID {
		t.Fatalf("launch of %q did not auto-select the child; selected = %q, status = %q", name, childID, h.StatusMessage())
	}
	childBody := waitForJourneySetupComplete(t, fx.Server.URL, childID)
	if childBody["status"] != "Created" {
		t.Fatalf("child %s status = %v, want Created after setup", childID, childBody["status"])
	}
	return childID
}

// journeyRunChildToClosed starts the child through the TUI's contextual
// action key and waits for the relationship to durably close. Journeys
// driving this on a parent without review checkpoints get a straight-through
// scripted pipeline run.
func journeyRunChildToClosed(t *testing.T, fx *tuiJourneyFixture, h *tuitest.AppHarness, childID string) {
	t.Helper()
	h.Refresh()
	h.PressKey(tea.KeyPressMsg{Code: 'a', Text: "a"})
	waitForJourneyChildClosed(t, fx.Server.URL, fx.Store, childID)
}

// journeyExpandHistoryGroup navigates from the parent row onto the collapsed
// "Refactor History (N)" group row and expands it with Enter, returning once
// the closed-child token (a child slug) is visible in the dashboard.
func journeyExpandHistoryGroup(t *testing.T, h *tuitest.AppHarness, parentID, childSlug string) {
	t.Helper()
	expanded := false
	for i := 0; i < 4 && !expanded; i++ {
		h.Press(tea.KeyDown)
		h.Press(tea.KeyEnter)
		if got := h.SelectedFeatureID(); got != "" && got != parentID {
			// Landed on a closed child row: group was already expanded.
			expanded = true
			break
		}
		if strings.Contains(ansi.Strip(h.View()), childSlug) {
			expanded = true
			break
		}
	}
	if !expanded {
		t.Fatalf("Enter never expanded the Refactor History group; selected = %q\n%s", h.SelectedFeatureID(), ansi.Strip(h.View()))
	}
}

// journeyHintLine returns the stripped view line containing marker, failing
// when no line carries it. It isolates one rendered line (e.g. the footer
// hint line) so word-level assertions never hit unrelated detail content.
func journeyHintLine(t *testing.T, view, marker string) string {
	t.Helper()
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, marker) {
			return line
		}
	}
	t.Fatalf("view has no line containing %q:\n%s", marker, view)
	return ""
}
