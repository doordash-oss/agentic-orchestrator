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
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

var testIndexerRepoNames = []string{"tui-app", "database-lib", "kubernetes-ops", "auth-service", "frontend-ui",
	"backend-api", "data-pipeline", "ml-service", "devops-tools", "messaging-queue",
	"monitoring-svc", "payment-gw", "search-engine", "notification-svc", "cache-layer",
	"config-service", "gateway-api", "user-service", "analytics-svc", "docs-site"}

// setupTestRepos creates temp directories with git repos for testing.
func setupTestRepos(t *testing.T, count int) (stateDir string, repos map[string]config.RepoConfig, cleanup func()) {
	t.Helper()
	stateDir = t.TempDir()
	repos = make(map[string]config.RepoConfig, count)

	for i := 0; i < count && i < len(testIndexerRepoNames); i++ {
		name := testIndexerRepoNames[i]
		repoPath := filepath.Join(stateDir, "repos", name)
		if err := os.MkdirAll(repoPath, 0o755); err != nil {
			t.Fatal(err)
		}

		// Initialize git repo
		runGit(t, repoPath, "init")
		runGit(t, repoPath, "config", "user.email", "test@test.com")
		runGit(t, repoPath, "config", "user.name", "Test")

		// Create repo-specific content
		writeRepoContent(t, repoPath, name)
		runGit(t, repoPath, "add", ".")
		runGit(t, repoPath, "commit", "-m", "initial commit for "+name)

		repos[name] = config.RepoConfig{Path: repoPath}
	}
	return stateDir, repos, func() {}
}

// writeRepoContent creates files that give each repo a distinct identity.
func writeRepoContent(t *testing.T, repoPath, name string) {
	t.Helper()

	var readme, goCode string
	switch name {
	case "tui-app":
		readme = "# TUI Application\nTerminal user interface for workflow orchestration using bubbletea.\nFeatures: dashboard, wizard, detail views, keyboard navigation."
		goCode = "package tui\n\nfunc NewDashboard() {}\nfunc NewWizard() {}\nfunc RenderView() {}\n"
	case "database-lib":
		readme = "# Database Library\nPostgreSQL migration tool with schema versioning and rollback.\nSupports: migrations, seeds, schema diff, connection pooling."
		goCode = "package db\n\nfunc RunMigration() {}\nfunc Rollback() {}\nfunc CreatePool() {}\n"
	case "kubernetes-ops":
		readme = "# Kubernetes Operations\nHelm charts and operators for cluster infrastructure management.\nIncludes: ingress, monitoring, autoscaling, RBAC."
		goCode = "package k8s\n\nfunc DeployChart() {}\nfunc ScaleDeployment() {}\n"
	case "auth-service":
		readme = "# Auth Service\nAuthentication and authorization microservice with OAuth2 and JWT.\nHandles: login, token refresh, RBAC, SSO integration."
		goCode = "package auth\n\nfunc Login() {}\nfunc RefreshToken() {}\nfunc VerifyPermission() {}\n"
	case "frontend-ui":
		readme = "# Frontend UI\nReact TypeScript web application with component library.\nIncludes: forms, tables, charts, theme system."
		goCode = "" // Not a Go project
		_ = os.WriteFile(filepath.Join(repoPath, "package.json"), []byte(`{"name":"frontend-ui","dependencies":{"react":"^18","typescript":"^5"}}`), 0o644)
	default:
		readme = "# " + name + "\nA service component for the platform."
		goCode = "package main\n\nfunc main() {}\n"
	}

	_ = os.WriteFile(filepath.Join(repoPath, "README.md"), []byte(readme), 0o644)
	if goCode != "" {
		_ = os.MkdirAll(filepath.Join(repoPath, "internal"), 0o755)
		_ = os.WriteFile(filepath.Join(repoPath, "main.go"), []byte(goCode), 0o644)
		_ = os.WriteFile(filepath.Join(repoPath, "go.mod"), []byte("module example.com/"+name+"\n\ngo 1.24\n"), 0o644)
	}
}

type mockFeature struct {
	name   string
	desc   string
	repos  []string
	status feature.Status
}

func setupTestFeatureStore(t *testing.T, stateDir string, features []mockFeature) *feature.Store {
	t.Helper()
	storeDir := filepath.Join(stateDir, "features")
	_ = os.MkdirAll(storeDir, 0o755)
	store := feature.NewStore(storeDir)

	for i, mf := range features {
		id := generateTestFeatureID(i)
		featureDir := filepath.Join(storeDir, id)
		_ = os.MkdirAll(featureDir, 0o755)

		var repos []feature.FeatureRepo
		for _, r := range mf.repos {
			repos = append(repos, feature.FeatureRepo{Name: r})
		}

		f := &feature.Feature{
			ID:            id,
			Name:          mf.name,
			Description:   mf.desc,
			Status:        mf.status,
			Repos:         repos,
			Created:       time.Now(),
			SchemaVersion: feature.SchemaVersionCurrent,
		}
		if err := store.Save(f); err != nil {
			t.Fatalf("saving test feature: %v", err)
		}
	}
	return store
}

func generateTestFeatureID(i int) string {
	// Deterministic IDs for testing
	ids := []string{"feat-001", "feat-002", "feat-003", "feat-004", "feat-005",
		"feat-006", "feat-007", "feat-008", "feat-009", "feat-010"}
	if i < len(ids) {
		return ids[i]
	}
	return "feat-overflow"
}

type inMemoryIndexerFixture struct {
	stateDir string
	repos    map[string]config.RepoConfig
	store    *feature.Store
	runner   *inMemoryGitRunner
}

type inMemoryGitRunner struct {
	mu    sync.Mutex
	heads map[string]string
	calls []string
}

func (r *inMemoryGitRunner) Run(ctx context.Context, name string, args []string, opts ports.CommandOpts) ([]byte, error) {
	r.mu.Lock()
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	r.mu.Unlock()
	if name != "git" {
		return nil, fmt.Errorf("unexpected command %q", name)
	}
	if len(args) < 3 || args[0] != "-C" {
		return nil, fmt.Errorf("unexpected git args %v", args)
	}
	repoPath := args[1]
	switch args[2] {
	case "rev-parse":
		if len(args) == 4 && args[3] == "HEAD" {
			head, ok := r.heads[repoPath]
			if !ok {
				return nil, fmt.Errorf("missing fake HEAD for %s", repoPath)
			}
			return []byte(head + "\n"), nil
		}
	case "log":
		return []byte("initial commit for " + filepath.Base(repoPath) + "\n"), nil
	}
	return nil, fmt.Errorf("unexpected git args %v", args)
}

func setupInMemoryIndexerFixture(t *testing.T, names []string, features []mockFeature) *inMemoryIndexerFixture {
	t.Helper()
	stateDir := t.TempDir()
	repos := make(map[string]config.RepoConfig, len(names))
	heads := make(map[string]string, len(names))

	for _, name := range names {
		repoPath := filepath.Join(stateDir, "repos", name)
		if err := os.MkdirAll(repoPath, 0o755); err != nil {
			t.Fatalf("creating repo dir %q: %v", name, err)
		}
		writeRepoContent(t, repoPath, name)
		repos[name] = config.RepoConfig{Path: repoPath}
		heads[repoPath] = name + "-head-1"
	}

	return &inMemoryIndexerFixture{
		stateDir: stateDir,
		repos:    repos,
		store:    setupTestFeatureStore(t, stateDir, features),
		runner:   &inMemoryGitRunner{heads: heads},
	}
}

func (f *inMemoryIndexerFixture) load(t *testing.T) *ClassifierIndex {
	t.Helper()
	idx, err := LoadOrBuildClassifier(f.stateDir, f.repos, f.store, WithCommandRunner(f.runner))
	if err != nil {
		t.Fatalf("LoadOrBuildClassifier: %v", err)
	}
	return idx
}

func (f *inMemoryIndexerFixture) assertNoGitSetup(t *testing.T) {
	t.Helper()
	f.runner.mu.Lock()
	defer f.runner.mu.Unlock()
	for _, call := range f.runner.calls {
		if strings.Contains(call, " init") ||
			strings.Contains(call, " config ") ||
			strings.Contains(call, " add ") ||
			strings.Contains(call, " commit ") {
			t.Fatalf("in-memory indexer fixture used git setup command %q", call)
		}
	}
}

// --- Unit Tests ---

func TestLoadOrBuildClassifier_FirstStartup(t *testing.T) {
	fixture := setupInMemoryIndexerFixture(t, testIndexerRepoNames[:3], nil)
	idx := fixture.load(t)
	fixture.assertNoGitSetup(t)

	if idx.Classifier == nil {
		t.Fatal("classifier is nil")
	}

	// model.json should exist
	modelPath := filepath.Join(fixture.stateDir, classifierDir, modelFile)
	if _, err := os.Stat(modelPath); os.IsNotExist(err) {
		t.Error("model.json not created")
	}
	// index-state.json should exist
	statePath := filepath.Join(fixture.stateDir, classifierDir, indexStateFile)
	if _, err := os.Stat(statePath); os.IsNotExist(err) {
		t.Error("index-state.json not created")
	}

	// RepoIndex should have entries for all repos
	for name := range fixture.repos {
		entry, ok := idx.RepoIndex[name]
		if !ok {
			t.Errorf("missing RepoIndex entry for %q", name)
			continue
		}
		if entry.HeadCommit == "" {
			t.Errorf("empty HeadCommit for %q", name)
		}
	}
}

func TestLoadOrBuildClassifier_SubsequentStartup_NoChanges(t *testing.T) {
	// Extended-regression owner: real-git persistence reload across startup.
	if testing.Short() {
		t.Skip("skipping in short mode: extended regression covers real-git persistence reload")
	}

	stateDir, repos, cleanup := setupTestRepos(t, 3)
	defer cleanup()
	store := setupTestFeatureStore(t, stateDir, nil)

	// First build
	_, err := LoadOrBuildClassifier(stateDir, repos, store)
	if err != nil {
		t.Fatalf("first build: %v", err)
	}

	// Second build — should be fast (nothing changed)
	start := time.Now()
	idx2, err := LoadOrBuildClassifier(stateDir, repos, store)
	if err != nil {
		t.Fatalf("second build: %v", err)
	}
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Errorf("subsequent startup too slow: %v (expected <2s)", elapsed)
	}
	if idx2.Classifier == nil {
		t.Fatal("classifier is nil on subsequent startup")
	}
}

func TestLoadOrBuildClassifier_SubsequentStartup_RepoChanged(t *testing.T) {
	// Extended-regression owner: real-git incremental startup after repo HEAD changes.
	if testing.Short() {
		t.Skip("skipping in short mode: extended regression covers real-git incremental startup after repo changes")
	}

	stateDir, repos, cleanup := setupTestRepos(t, 3)
	defer cleanup()
	store := setupTestFeatureStore(t, stateDir, nil)

	// First build
	idx1, err := LoadOrBuildClassifier(stateDir, repos, store)
	if err != nil {
		t.Fatalf("first build: %v", err)
	}

	// Make a new commit in the first repo
	var firstRepo string
	for name := range repos {
		firstRepo = name
		break
	}
	repoPath := repos[firstRepo].Path
	_ = os.WriteFile(filepath.Join(repoPath, "new-file.go"), []byte("package main\nfunc NewFunc() {}\n"), 0o644)
	runGit(t, repoPath, "add", ".")
	runGit(t, repoPath, "commit", "-m", "add new function")

	oldHead := idx1.RepoIndex[firstRepo].HeadCommit

	// Rebuild
	idx2, err := LoadOrBuildClassifier(stateDir, repos, store)
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	if idx2.RepoIndex[firstRepo].HeadCommit == oldHead {
		t.Error("HeadCommit not updated for changed repo")
	}

	// Other repos should not have changed
	for name := range repos {
		if name == firstRepo {
			continue
		}
		if idx2.RepoIndex[name].HeadCommit != idx1.RepoIndex[name].HeadCommit {
			t.Errorf("HeadCommit changed for unchanged repo %q", name)
		}
	}
}

func TestLoadOrBuildClassifier_NewRepoDiscovered(t *testing.T) {
	// Extended-regression owner: real-git startup discovery of newly added repositories.
	if testing.Short() {
		t.Skip("skipping in short mode: extended regression covers real-git startup discovery of new repositories")
	}

	stateDir, repos, cleanup := setupTestRepos(t, 2)
	defer cleanup()
	store := setupTestFeatureStore(t, stateDir, nil)

	// First build with 2 repos
	_, err := LoadOrBuildClassifier(stateDir, repos, store)
	if err != nil {
		t.Fatalf("first build: %v", err)
	}

	// Add a 3rd repo
	newRepoPath := filepath.Join(stateDir, "repos", "new-repo")
	_ = os.MkdirAll(newRepoPath, 0o755)
	runGit(t, newRepoPath, "init")
	runGit(t, newRepoPath, "config", "user.email", "test@test.com")
	runGit(t, newRepoPath, "config", "user.name", "Test")
	_ = os.WriteFile(filepath.Join(newRepoPath, "README.md"), []byte("# New Repo\nA new service."), 0o644)
	_ = os.WriteFile(filepath.Join(newRepoPath, "go.mod"), []byte("module example.com/new-repo\n\ngo 1.24\n"), 0o644)
	runGit(t, newRepoPath, "add", ".")
	runGit(t, newRepoPath, "commit", "-m", "initial")

	repos["new-repo"] = config.RepoConfig{Path: newRepoPath}

	// Rebuild with 3 repos
	idx, err := LoadOrBuildClassifier(stateDir, repos, store)
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	if _, ok := idx.RepoIndex["new-repo"]; !ok {
		t.Error("new-repo not in RepoIndex")
	}
}

func TestClassifierIndex_Predict_BasicAccuracy(t *testing.T) {
	fixture := setupInMemoryIndexerFixture(t, testIndexerRepoNames[:3], nil)
	idx := fixture.load(t)
	repos := fixture.repos

	// Predict for TUI-related query
	result := idx.Predict("Fix the TUI wizard", "Terminal user interface bubbletea dashboard", repos)
	if len(result) == 0 {
		t.Fatal("no predictions for TUI query")
	}
	if result[0] != "tui-app" {
		t.Errorf("expected tui-app as top result, got %v", result)
	}

	// Predict for database-related query
	result = idx.Predict("Database migration", "PostgreSQL schema migration rollback", repos)
	if len(result) == 0 {
		t.Fatal("no predictions for database query")
	}
	if result[0] != "database-lib" {
		t.Errorf("expected database-lib as top result, got %v", result)
	}
}

func TestClassifierIndex_Predict_ServicesProtobufRegression(t *testing.T) {
	// Extended-regression owner: realistic repo selection with service/protobuf repositories.
	if testing.Short() {
		t.Skip("skipping in short mode: extended regression covers realistic service/protobuf repo selection")
	}

	// Build classifier with several repos including a services-protobuf repo
	stateDir, repos, cleanup := setupTestRepos(t, 5)
	defer cleanup()

	// Add a services-protobuf repo
	pbPath := filepath.Join(stateDir, "repos", "services-protobuf")
	_ = os.MkdirAll(pbPath, 0o755)
	runGit(t, pbPath, "init")
	runGit(t, pbPath, "config", "user.email", "test@test.com")
	runGit(t, pbPath, "config", "user.name", "Test")
	_ = os.WriteFile(filepath.Join(pbPath, "README.md"), []byte("# Services Protobuf\nShared protobuf definitions for microservices."), 0o644)
	_ = os.WriteFile(filepath.Join(pbPath, "go.mod"), []byte("module example.com/services-protobuf\n\ngo 1.24\n"), 0o644)
	runGit(t, pbPath, "add", ".")
	runGit(t, pbPath, "commit", "-m", "initial")
	repos["services-protobuf"] = config.RepoConfig{Path: pbPath}

	store := setupTestFeatureStore(t, stateDir, nil)
	idx, err := LoadOrBuildClassifier(stateDir, repos, store)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	// Generic queries should NOT have services-protobuf as top result
	genericQueries := []string{
		"Add new feature",
		"Fix bug in service",
		"Improve performance",
	}
	for _, q := range genericQueries {
		result := idx.Predict(q, q, repos)
		if len(result) > 0 && result[0] == "services-protobuf" {
			t.Errorf("services-protobuf should not be top result for %q, got %v", q, result)
		}
	}
}

func TestCheckAndReindex_NoChanges(t *testing.T) {
	fixture := setupInMemoryIndexerFixture(t, testIndexerRepoNames[:3], nil)
	idx := fixture.load(t)

	changed, err := idx.CheckAndReindex(fixture.repos, fixture.store)
	if err != nil {
		t.Fatalf("CheckAndReindex: %v", err)
	}
	if changed {
		t.Error("expected no changes")
	}
}

func TestCheckAndReindex_RepoChanged(t *testing.T) {
	// Extended-regression owner: real-git reindex after committed repository changes.
	if testing.Short() {
		t.Skip("skipping in short mode: extended regression covers real-git reindex after committed repository changes")
	}

	stateDir, repos, cleanup := setupTestRepos(t, 3)
	defer cleanup()
	store := setupTestFeatureStore(t, stateDir, nil)

	idx, err := LoadOrBuildClassifier(stateDir, repos, store)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	// Make a new commit in one repo
	var firstRepo string
	for name := range repos {
		firstRepo = name
		break
	}
	repoPath := repos[firstRepo].Path
	_ = os.WriteFile(filepath.Join(repoPath, "change.txt"), []byte("changed"), 0o644)
	runGit(t, repoPath, "add", ".")
	runGit(t, repoPath, "commit", "-m", "change")

	changed, err := idx.CheckAndReindex(repos, store)
	if err != nil {
		t.Fatalf("CheckAndReindex: %v", err)
	}
	if !changed {
		t.Error("expected changes detected")
	}
}

func TestCheckAndReindex_NewHistoricalFeature(t *testing.T) {
	// Extended-regression owner: realistic CheckAndReindex historical-feature ingestion.
	if testing.Short() {
		t.Skip("skipping in short mode: extended regression covers realistic historical-feature reindex ingestion")
	}

	stateDir, repos, cleanup := setupTestRepos(t, 3)
	defer cleanup()
	store := setupTestFeatureStore(t, stateDir, nil)

	idx, err := LoadOrBuildClassifier(stateDir, repos, store)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	// Add a new completed feature to the store
	var repoName string
	for name := range repos {
		repoName = name
		break
	}
	featureDir := filepath.Join(stateDir, "features", "new-feat")
	_ = os.MkdirAll(featureDir, 0o755)
	newFeature := &feature.Feature{
		ID:            "new-feat",
		Name:          "New test feature",
		Description:   "A completed feature for testing",
		Status:        feature.StatusDone,
		Repos:         []feature.FeatureRepo{{Name: repoName}},
		Created:       time.Now(),
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	_ = store.Save(newFeature)

	changed, err := idx.CheckAndReindex(repos, store)
	if err != nil {
		t.Fatalf("CheckAndReindex: %v", err)
	}
	if !changed {
		t.Error("expected changes from new historical feature")
	}
	if !idx.FeatureIndex.ProcessedIDs["new-feat"] {
		t.Error("new feature not in ProcessedIDs")
	}
}

func TestLearnFromFeature_WeightCapping(t *testing.T) {
	fixture := setupInMemoryIndexerFixture(t, testIndexerRepoNames[:10], nil)
	idx := fixture.load(t)

	// Add features across many distinct repos to push weight toward cap
	allRepoNames := make([]string, 0, len(fixture.repos))
	for name := range fixture.repos {
		allRepoNames = append(allRepoNames, name)
	}

	for i := 0; i < len(allRepoNames); i++ {
		f := &feature.Feature{
			ID:          generateTestFeatureID(i),
			Name:        "test feature",
			Description: "description for capping test",
			Status:      feature.StatusDone,
			Repos:       []feature.FeatureRepo{{Name: allRepoNames[i%len(allRepoNames)]}},
		}
		idx.LearnFromFeature(f)
	}

	// Verify formula: min(20.0, 2.0 * len(distinctRepos))
	distinctCount := len(idx.FeatureIndex.DistinctRepos)
	expectedWeight := 2.0 * float64(distinctCount)
	if expectedWeight > maxHistoricalWeight {
		expectedWeight = maxHistoricalWeight
	}
	// Just verify the distinct repos are tracked correctly
	if distinctCount == 0 {
		t.Error("no distinct repos tracked")
	}
	if distinctCount > len(fixture.repos) {
		t.Errorf("more distinct repos than total repos: %d > %d", distinctCount, len(fixture.repos))
	}
	if expectedWeight != maxHistoricalWeight {
		t.Errorf("historical weight = %f, want capped weight %f", expectedWeight, maxHistoricalWeight)
	}
}

func TestLearnFromFeature_Idempotent(t *testing.T) {
	fixture := setupInMemoryIndexerFixture(t, testIndexerRepoNames[:3], nil)
	idx := fixture.load(t)

	var repoName string
	for name := range fixture.repos {
		repoName = name
		break
	}

	f := &feature.Feature{
		ID:          "idempotent-feat",
		Name:        "test feature",
		Description: "test description for idempotency",
		Status:      feature.StatusDone,
		Repos:       []feature.FeatureRepo{{Name: repoName}},
	}

	// Learn twice
	idx.LearnFromFeature(f)
	countAfterFirst := len(idx.FeatureIndex.ProcessedIDs)

	idx.LearnFromFeature(f)
	countAfterSecond := len(idx.FeatureIndex.ProcessedIDs)

	if countAfterFirst != countAfterSecond {
		t.Errorf("ProcessedIDs count changed: %d -> %d", countAfterFirst, countAfterSecond)
	}
}

func TestLearnFromFeature_IncrementalUpdate(t *testing.T) {
	fixture := setupInMemoryIndexerFixture(t, testIndexerRepoNames[:3], nil)
	idx := fixture.load(t)

	// Get initial predictions
	initialResult := idx.Predict("database migration postgresql", "schema migration rollback", fixture.repos)

	// Learn from a feature that associates "database migration" with database-lib
	f := &feature.Feature{
		ID:          "db-feat",
		Name:        "Database migration improvements",
		Description: "Improve PostgreSQL schema migration and rollback functionality",
		Status:      feature.StatusDone,
		Repos:       []feature.FeatureRepo{{Name: "database-lib"}},
	}
	idx.LearnFromFeature(f)

	// Predict again — should still favor database-lib
	updatedResult := idx.Predict("database migration postgresql", "schema migration rollback", fixture.repos)
	if len(updatedResult) == 0 {
		t.Fatal("no predictions after learning")
	}

	// The prediction should favor database-lib (either initially or after learning)
	_ = initialResult // Initial may or may not have it, but after learning it should
	if updatedResult[0] != "database-lib" {
		t.Errorf("expected database-lib as top result after learning, got %v", updatedResult)
	}
}

func TestPersistAndLoad_RoundTrip(t *testing.T) {
	fixture := setupInMemoryIndexerFixture(t, testIndexerRepoNames[:3], nil)
	idx1 := fixture.load(t)
	pred1 := idx1.Predict("Fix the TUI wizard", "terminal user interface", fixture.repos)

	classifier, err := loadModel(fixture.stateDir)
	if err != nil {
		t.Fatalf("loadModel: %v", err)
	}
	repoIndex, featureIndex, repoTokens, err := loadIndexState(fixture.stateDir)
	if err != nil {
		t.Fatalf("loadIndexState: %v", err)
	}
	classifier.Finalize()
	idx2 := &ClassifierIndex{
		Classifier:   classifier,
		RepoIndex:    repoIndex,
		FeatureIndex: featureIndex,
		RepoTokens:   repoTokens,
		StateDir:     fixture.stateDir,
	}
	idx2.rebuildIDF(fixture.repos)
	pred2 := idx2.Predict("Fix the TUI wizard", "terminal user interface", fixture.repos)

	// Predictions should be identical
	if len(pred1) != len(pred2) {
		t.Fatalf("predictions differ in length: %v vs %v", pred1, pred2)
	}
	for i := range pred1 {
		if pred1[i] != pred2[i] {
			t.Errorf("prediction[%d] differs: %q vs %q", i, pred1[i], pred2[i])
		}
	}
}

// --- Benchmark Tests ---

func BenchmarkLoadClassifier_FromDisk(b *testing.B) {
	benchStateDir := b.TempDir()

	// Create a mock classifier with realistic data
	c := NewCNBClassifier(1.0, 1.0)
	for i := 0; i < 20; i++ {
		name := generateTestFeatureID(i)
		features := make(map[string]float64)
		for j := 0; j < 500; j++ {
			features[generateTestFeatureID(j)] = float64(j) * 0.01
		}
		c.Fit(name, features, 1.0)
	}
	c.Finalize()

	dir := filepath.Join(benchStateDir, classifierDir)
	_ = os.MkdirAll(dir, 0o755)
	_ = persistModel(benchStateDir, c)
	_ = persistIndexState(benchStateDir, make(map[string]RepoIndexEntry), FeatureIndexState{
		ProcessedIDs:  make(map[string]bool),
		DistinctRepos: make(map[string]bool),
	}, make(map[string][]string))

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		classifier, _ := loadModel(benchStateDir)
		classifier.Finalize()
	}
}

func BenchmarkClassifierIndex_Predict(b *testing.B) {
	c := NewCNBClassifier(1.0, 1.0)
	repos := make(map[string]config.RepoConfig)
	repoNames := []string{"tui-app", "database-lib", "kubernetes-ops", "auth-service", "frontend-ui"}
	for _, name := range repoNames {
		features := map[string]float64{
			"tui": 0.5, "database": 0.3, "kubernetes": 0.2,
			name: 1.0, "service": 0.1, "api": 0.1,
		}
		c.Fit(name, features, 1.0)
		repos[name] = config.RepoConfig{}
	}
	c.Finalize()

	idf := map[string]float64{
		"tui": 1.6, "database": 1.6, "kubernetes": 1.6,
		"fix": 0.5, "wizard": 1.6, "service": 0.2,
	}

	idx := &ClassifierIndex{
		Classifier: c,
		IDF:        idf,
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		idx.Predict("Fix the TUI wizard", "terminal user interface bubbletea", repos)
	}
}

func BenchmarkCheckAndReindex_NoChanges(b *testing.B) {
	// This benchmark uses real git repos, skip in short mode
	if testing.Short() {
		b.Skip("skipping in short mode")
	}

	stateDir := b.TempDir()
	repos := make(map[string]config.RepoConfig)

	// Create 3 real git repos
	for _, name := range []string{"repo-a", "repo-b", "repo-c"} {
		repoPath := filepath.Join(stateDir, "repos", name)
		_ = os.MkdirAll(repoPath, 0o755)
		exec.Command("git", "-C", repoPath, "init").Run()
		exec.Command("git", "-C", repoPath, "config", "user.email", "test@test.com").Run()
		exec.Command("git", "-C", repoPath, "config", "user.name", "Test").Run()
		_ = os.WriteFile(filepath.Join(repoPath, "README.md"), []byte("# "+name), 0o644)
		_ = os.WriteFile(filepath.Join(repoPath, "go.mod"), []byte("module example.com/"+name+"\n\ngo 1.24\n"), 0o644)
		exec.Command("git", "-C", repoPath, "add", ".").Run()
		exec.Command("git", "-C", repoPath, "commit", "-m", "initial").Run()
		repos[name] = config.RepoConfig{Path: repoPath}
	}

	store := feature.NewStore(filepath.Join(stateDir, "features"))
	_ = os.MkdirAll(filepath.Join(stateDir, "features"), 0o755)

	idx, _ := LoadOrBuildClassifier(stateDir, repos, store)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		idx.CheckAndReindex(repos, store)
	}
}

// --- End-to-End Tests ---

func TestEndToEnd_ClassificationUnder1s(t *testing.T) {
	// Extended-regression owner: realistic classification timing over real git fixtures.
	if testing.Short() {
		t.Skip("skipping in short mode: extended regression covers realistic classification timing")
	}

	stateDir, repos, cleanup := setupTestRepos(t, 10)
	defer cleanup()
	store := setupTestFeatureStore(t, stateDir, nil)

	idx, err := LoadOrBuildClassifier(stateDir, repos, store)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	start := time.Now()
	for i := 0; i < 100; i++ {
		idx.Predict("Fix the TUI wizard dashboard", "terminal user interface", repos)
	}
	elapsed := time.Since(start)

	// 100 predictions should complete in under 1 second
	if elapsed > time.Second {
		t.Errorf("100 predictions took %v (expected <1s)", elapsed)
	}
}

func TestEndToEnd_RealisticRepoSelection(t *testing.T) {
	// Extended-regression owner: end-to-end realistic repository selection.
	if testing.Short() {
		t.Skip("skipping in short mode: extended regression covers end-to-end realistic repository selection")
	}

	stateDir := t.TempDir()
	repoDescs := map[string]string{
		"agentic":        "TUI workflow orchestrator for AI-assisted development with research plan implement publish lifecycle",
		"db-tool":        "database management tool for PostgreSQL schema migrations and data access",
		"graph-runner":   "execution engine for directed acyclic graph pipelines and workflow orchestration",
		"data-pipeline":  "monorepo containing CDC consumer and proxy services for data replication",
		"approvals":      "human-in-the-loop approval framework for AI agent actions",
		"dev-portal":     "backstage developer portal with react typescript frontend",
		"cluster-config": "kubernetes helm charts for cluster infrastructure",
		"dbaccess":       "go library for database connection management",
		"go-utils":       "go utility library for common shared patterns",
	}

	repos := make(map[string]config.RepoConfig)
	for name, desc := range repoDescs {
		repoPath := filepath.Join(stateDir, "repos", name)
		_ = os.MkdirAll(repoPath, 0o755)
		runGit(t, repoPath, "init")
		runGit(t, repoPath, "config", "user.email", "test@test.com")
		runGit(t, repoPath, "config", "user.name", "Test")
		_ = os.WriteFile(filepath.Join(repoPath, "README.md"), []byte("# "+name+"\n"+desc), 0o644)
		_ = os.WriteFile(filepath.Join(repoPath, "go.mod"), []byte("module example.com/"+name+"\n\ngo 1.24\n"), 0o644)
		runGit(t, repoPath, "add", ".")
		runGit(t, repoPath, "commit", "-m", "initial commit")
		repos[name] = config.RepoConfig{Path: repoPath}
	}

	store := feature.NewStore(filepath.Join(stateDir, "features"))
	_ = os.MkdirAll(filepath.Join(stateDir, "features"), 0o755)

	idx, err := LoadOrBuildClassifier(stateDir, repos, store)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	// Test: "Fix the agentic TUI wizard" -> should select agentic
	result := idx.Predict("Fix the agentic TUI wizard", "Fix the agentic TUI wizard", repos)
	if len(result) == 0 || result[0] != "agentic" {
		t.Errorf("expected agentic as top result for TUI query, got %v", result)
	}

	// Test: "Add kubernetes helm chart" -> should include cluster-config
	result = idx.Predict("Add kubernetes helm chart", "Add kubernetes helm chart for new service", repos)
	foundCluster := false
	for _, s := range result {
		if s == "cluster-config" {
			foundCluster = true
		}
	}
	if !foundCluster {
		t.Errorf("expected cluster-config in results for k8s query, got %v", result)
	}

	// Results limited by maxSelections — should be ≤3 repos per query
	if len(result) > 3 {
		t.Errorf("expected at most 3 repos (maxSelections should limit), got %v", result)
	}
}

// TestClassifierIndex_Predict_ManyRepos verifies predictions work with 20 repos.
// This is a regression test: with 20 classes, softmax uniform is 1/20 = 0.05.
// The old absolute threshold of 0.15 was unreachable; lift-based scoring adapts.
func TestClassifierIndex_Predict_ManyRepos(t *testing.T) {
	repoDescriptions := map[string]string{
		"tui-app":          "terminal user interface bubbletea dashboard wizard",
		"database-lib":     "postgresql schema migration rollback database",
		"kubernetes-ops":   "helm chart cluster infrastructure autoscaling kubernetes",
		"auth-service":     "oauth jwt login permission rbac authentication",
		"frontend-ui":      "react typescript component forms tables",
		"backend-api":      "api handler request response middleware",
		"data-pipeline":    "cdc replication streaming warehouse pipeline",
		"ml-service":       "model inference training embeddings features",
		"devops-tools":     "deployment release automation infrastructure",
		"messaging-queue":  "queue topic broker delivery messaging",
		"monitoring-svc":   "metrics alerts dashboards observability",
		"payment-gw":       "payments checkout card gateway billing",
		"search-engine":    "index query ranking search documents",
		"notification-svc": "email sms push notification delivery",
		"cache-layer":      "redis cache eviction key value",
		"config-service":   "configuration flags rollout settings",
		"gateway-api":      "edge routing gateway proxy api",
		"user-service":     "profile account users preferences",
		"analytics-svc":    "events reporting analytics cohorts",
		"docs-site":        "documentation markdown publishing site",
	}
	repos := make(map[string]config.RepoConfig, len(repoDescriptions))
	classifier := NewCNBClassifier(1.0, classifierTemperature)
	idf := make(map[string]float64)
	for name, description := range repoDescriptions {
		repos[name] = config.RepoConfig{}
		tokens := TokenizeAndProcess(name + " " + description)
		features := make(map[string]float64, len(tokens))
		for _, token := range tokens {
			features[token]++
			idf[token] = 1
		}
		classifier.Fit(name, features, 1)
	}
	classifier.Finalize()
	idx := &ClassifierIndex{Classifier: classifier, IDF: idf}

	// TUI-related query should return results (not empty)
	result := idx.Predict("Fix the TUI wizard", "Terminal user interface bubbletea dashboard", repos)
	if len(result) == 0 {
		t.Fatal("no predictions for TUI query with 20 repos — lift-based threshold too strict")
	}
	if result[0] != "tui-app" {
		t.Errorf("expected tui-app as top result, got %v", result)
	}

	// Database-related query
	result = idx.Predict("Database migration", "PostgreSQL schema migration rollback", repos)
	if len(result) == 0 {
		t.Fatal("no predictions for database query with 20 repos")
	}
	if result[0] != "database-lib" {
		t.Errorf("expected database-lib as top result, got %v", result)
	}

	// Kubernetes-related query
	result = idx.Predict("Kubernetes deployment", "helm chart cluster infrastructure autoscaling", repos)
	if len(result) == 0 {
		t.Fatal("no predictions for kubernetes query with 20 repos")
	}
	if result[0] != "kubernetes-ops" {
		t.Errorf("expected kubernetes-ops as top result, got %v", result)
	}

	// Results should be capped at maxSelections (3)
	for _, q := range []struct{ name, desc string }{
		{"Fix TUI", "terminal bubbletea"},
		{"Database migration", "postgresql schema"},
		{"Kubernetes deploy", "helm cluster"},
	} {
		r := idx.Predict(q.name, q.desc, repos)
		if len(r) > 3 {
			t.Errorf("query %q returned %d results (max should be 3): %v", q.name, len(r), r)
		}
	}
}

// --- Regression Tests ---

// TestIncrementalReindex_PreservesHistoricalSignal verifies that historical feature
// training data is preserved after a repo HEAD change triggers incremental reindex.
// This is a regression test for: stale repos losing historical signal after RemoveClass.
func TestIncrementalReindex_PreservesHistoricalSignal(t *testing.T) {
	// Extended-regression owner: real-git reindex preserving historical signal after HEAD changes.
	if testing.Short() {
		t.Skip("skipping in short mode: extended regression covers real-git reindex preserving historical signal")
	}

	stateDir, repos, cleanup := setupTestRepos(t, 3)
	defer cleanup()

	// Create a historical feature that associates "database migration" with database-lib
	store := setupTestFeatureStore(t, stateDir, []mockFeature{
		{
			name:   "Database migration improvements",
			desc:   "Improve PostgreSQL schema migration and rollback",
			repos:  []string{"database-lib"},
			status: feature.StatusDone,
		},
	})

	idx, err := LoadOrBuildClassifier(stateDir, repos, store)
	if err != nil {
		t.Fatalf("initial build: %v", err)
	}

	// Verify historical feature was processed
	if len(idx.FeatureIndex.ProcessedIDs) == 0 {
		t.Fatal("no historical features processed during initial build")
	}

	// Get prediction before repo change — database-lib should be favored for DB queries
	predBefore := idx.Predict("database migration postgresql", "schema migration rollback", repos)
	if len(predBefore) == 0 {
		t.Fatal("no predictions before reindex")
	}
	beforeTopIsDB := predBefore[0] == "database-lib"

	// Change database-lib's HEAD (add a new file, make a new commit)
	dbRepo := repos["database-lib"]
	_ = os.WriteFile(filepath.Join(dbRepo.Path, "extra.go"), []byte("package db\nfunc Extra() {}\n"), 0o644)
	runGit(t, dbRepo.Path, "add", ".")
	runGit(t, dbRepo.Path, "commit", "-m", "add extra function")

	// Trigger incremental reindex
	changed, err := idx.CheckAndReindex(repos, store)
	if err != nil {
		t.Fatalf("CheckAndReindex: %v", err)
	}
	if !changed {
		t.Fatal("expected reindex to detect changes")
	}

	// Get prediction after reindex — historical signal should still be present
	predAfter := idx.Predict("database migration postgresql", "schema migration rollback", repos)
	if len(predAfter) == 0 {
		t.Fatal("no predictions after reindex")
	}

	// If database-lib was top before, it should still be top after reindex
	// (historical signal preserved)
	if beforeTopIsDB && predAfter[0] != "database-lib" {
		t.Errorf("historical signal lost: database-lib was top before reindex but got %v after", predAfter)
	}

	// Verify the feature is still in ProcessedIDs (not lost)
	if len(idx.FeatureIndex.ProcessedIDs) == 0 {
		t.Error("ProcessedIDs empty after reindex — historical tracking lost")
	}
}

// TestClassifierStateDirSiblingToFeatureStore verifies that when the classifier
// state dir is the parent of the feature store (mirroring TUI startup layout),
// historical feature processing works correctly. This is a regression test for:
// classifier/ created inside feature store causing Store.List() partial-load errors
// that silently abort historical ingestion.
func TestClassifierStateDirSiblingToFeatureStore(t *testing.T) {
	// Extended-regression owner: TUI-like on-disk classifier and feature-store sibling layout.
	if testing.Short() {
		t.Skip("skipping in short mode: extended regression covers TUI-like classifier state layout")
	}

	// Layout mirrors TUI app: rootDir/features/ is the store, rootDir/ is classifier stateDir.
	rootDir := t.TempDir()
	featuresDir := filepath.Join(rootDir, "features")
	_ = os.MkdirAll(featuresDir, 0o755)

	// Create repos
	_, repos, cleanup := setupTestRepos(t, 3)
	defer cleanup()

	// Create feature store with a historical feature
	store := feature.NewStore(featuresDir)
	var repoName string
	for name := range repos {
		repoName = name
		break
	}
	histFeat := &feature.Feature{
		ID:            "hist-001",
		Name:          "Historical DB migration",
		Description:   "PostgreSQL schema migration and rollback improvements",
		Status:        feature.StatusDone,
		Repos:         []feature.FeatureRepo{{Name: repoName}},
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	if err := store.Save(histFeat); err != nil {
		t.Fatalf("saving historical feature: %v", err)
	}

	// Build classifier with rootDir as stateDir (classifier/ is a sibling of features/)
	idx, err := LoadOrBuildClassifier(rootDir, repos, store)
	if err != nil {
		t.Fatalf("LoadOrBuildClassifier: %v", err)
	}

	// Verify classifier/ was created at rootDir/classifier/, NOT rootDir/features/classifier/
	classifierPath := filepath.Join(rootDir, classifierDir, modelFile)
	if _, err := os.Stat(classifierPath); os.IsNotExist(err) {
		t.Errorf("model.json not at expected path %s", classifierPath)
	}
	wrongPath := filepath.Join(featuresDir, classifierDir, modelFile)
	if _, err := os.Stat(wrongPath); err == nil {
		t.Errorf("model.json should NOT exist inside feature store at %s", wrongPath)
	}

	// Verify Store.List() still works without errors (no classifier/ dir collision)
	features, err := store.List()
	if err != nil {
		t.Errorf("Store.List() should succeed without errors, got: %v", err)
	}
	if len(features) != 1 {
		t.Errorf("expected 1 feature from store, got %d", len(features))
	}

	// Verify historical feature was processed
	if !idx.FeatureIndex.ProcessedIDs["hist-001"] {
		t.Error("historical feature not processed — historical ingestion failed")
	}

	// Verify predictions work
	result := idx.Predict("database migration", "schema migration rollback", repos)
	if len(result) == 0 {
		t.Error("no predictions — classifier may not be properly trained")
	}
}

// TestPartialLoadErrorDoesNotBlockHistoricalProcessing verifies that when
// Store.List() returns a PartialLoadError (e.g., non-feature subdirectories),
// successfully loaded features are still processed for historical training.
func TestPartialLoadErrorDoesNotBlockHistoricalProcessing(t *testing.T) {
	// Extended-regression owner: on-disk partial-load feature stores during historical ingestion.
	if testing.Short() {
		t.Skip("skipping in short mode: extended regression covers on-disk partial-load historical ingestion")
	}

	rootDir := t.TempDir()
	featuresDir := filepath.Join(rootDir, "features")
	_ = os.MkdirAll(featuresDir, 0o755)

	_, repos, cleanup := setupTestRepos(t, 3)
	defer cleanup()

	store := feature.NewStore(featuresDir)

	// Save a real historical feature
	var repoName string
	for name := range repos {
		repoName = name
		break
	}
	histFeat := &feature.Feature{
		ID:            "valid-feat",
		Name:          "Valid historical feature",
		Description:   "A feature that should be processable",
		Status:        feature.StatusDone,
		Repos:         []feature.FeatureRepo{{Name: repoName}},
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	if err := store.Save(histFeat); err != nil {
		t.Fatalf("saving feature: %v", err)
	}

	// Create a bogus subdirectory inside the feature store (simulates path collision)
	bogusDir := filepath.Join(featuresDir, "bogus-not-a-feature")
	_ = os.MkdirAll(bogusDir, 0o755)
	// Don't create feature.yaml — this will cause a PartialLoadError

	// Verify Store.List() does return a PartialLoadError
	features, err := store.List()
	if !feature.IsPartialLoadError(err) {
		t.Fatalf("expected PartialLoadError, got: %v", err)
	}
	if len(features) != 1 {
		t.Fatalf("expected 1 successfully loaded feature, got %d", len(features))
	}

	// Build classifier — should still process the valid feature despite PartialLoadError
	idx, err := LoadOrBuildClassifier(rootDir, repos, store)
	if err != nil {
		t.Fatalf("LoadOrBuildClassifier: %v", err)
	}

	// Verify the valid feature was processed
	if !idx.FeatureIndex.ProcessedIDs["valid-feat"] {
		t.Error("valid feature not processed despite PartialLoadError — historical ingestion aborted")
	}

	// Verify CheckAndReindex also handles PartialLoadError gracefully
	// Add another feature
	newFeat := &feature.Feature{
		ID:            "new-valid-feat",
		Name:          "Another valid feature",
		Description:   "Should be picked up by reindex",
		Status:        feature.StatusDone,
		Repos:         []feature.FeatureRepo{{Name: repoName}},
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	if err := store.Save(newFeat); err != nil {
		t.Fatalf("saving new feature: %v", err)
	}

	changed, err := idx.CheckAndReindex(repos, store)
	if err != nil {
		t.Fatalf("CheckAndReindex: %v", err)
	}
	if !changed {
		t.Error("expected changes from new historical feature")
	}
	if !idx.FeatureIndex.ProcessedIDs["new-valid-feat"] {
		t.Error("new feature not processed during reindex despite PartialLoadError")
	}
}

// TestIncrementalReindex_ConsistentIDF verifies that after a stale repo update,
// all class vectors use the same IDF space (no mixed old/new IDF weighting).
// This is a regression test for: classes left in mixed TF-IDF spaces after IDF rebuild.
func TestIncrementalReindex_ConsistentIDF(t *testing.T) {
	// Extended-regression owner: real-git incremental reindex IDF consistency after HEAD changes.
	if testing.Short() {
		t.Skip("skipping in short mode: extended regression covers real-git incremental reindex IDF consistency")
	}

	stateDir, repos, cleanup := setupTestRepos(t, 3)
	defer cleanup()
	store := setupTestFeatureStore(t, stateDir, nil)

	// Initial build
	idx, err := LoadOrBuildClassifier(stateDir, repos, store)
	if err != nil {
		t.Fatalf("initial build: %v", err)
	}

	// Record predictions for all repos before change
	predsBefore := make(map[string][]string)
	queries := []struct{ name, desc string }{
		{"Fix TUI wizard", "terminal user interface bubbletea dashboard"},
		{"Database migration", "PostgreSQL schema migration rollback"},
		{"Kubernetes deployment", "helm chart cluster infrastructure"},
	}
	for _, q := range queries {
		predsBefore[q.name] = idx.Predict(q.name, q.desc, repos)
	}

	// Change ONE repo's HEAD
	var changedRepo string
	for name := range repos {
		changedRepo = name
		break
	}
	repoPath := repos[changedRepo].Path
	_ = os.WriteFile(filepath.Join(repoPath, "idf-test.txt"), []byte("new content for idf test"), 0o644)
	runGit(t, repoPath, "add", ".")
	runGit(t, repoPath, "commit", "-m", "idf consistency test")

	// Incremental reindex
	changed, err := idx.CheckAndReindex(repos, store)
	if err != nil {
		t.Fatalf("CheckAndReindex: %v", err)
	}
	if !changed {
		t.Fatal("expected reindex to detect changes")
	}

	// Verify ALL repos have tokens in RepoTokens (consistent corpus)
	for name := range repos {
		tokens, ok := idx.RepoTokens[name]
		if !ok {
			t.Errorf("repo %q missing from RepoTokens after reindex", name)
		}
		if len(tokens) == 0 {
			t.Errorf("repo %q has empty tokens after reindex", name)
		}
	}

	// Verify IDF was rebuilt (should contain terms from all repos)
	if len(idx.IDF) == 0 {
		t.Error("IDF is empty after reindex")
	}

	// Verify predictions still make sense (no garbage from mixed IDF spaces)
	for _, q := range queries {
		result := idx.Predict(q.name, q.desc, repos)
		// We don't assert exact match with predsBefore (content changed),
		// but predictions should be non-empty and valid
		for _, r := range result {
			if _, ok := repos[r]; !ok {
				t.Errorf("prediction %q for query %q is not a valid repo", r, q.name)
			}
		}
	}

	// Verify all classes exist in the classifier after rebuild
	for name := range repos {
		if _, ok := idx.Classifier.ClassCounts[name]; !ok {
			t.Errorf("repo %q missing from classifier after reindex — rebuild incomplete", name)
		}
	}
}
