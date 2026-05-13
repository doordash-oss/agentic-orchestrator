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
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

const (
	classifierDir         = "classifier"
	modelFile             = "model.json"
	indexStateFile        = "index-state.json"
	firstStartupBudget    = 50 * time.Second
	reindexBudget         = 10 * time.Second
	maxHistoricalWeight   = 20.0
	historicalWeightMul   = 2.0
	contentWeight         = 1.0
	predictLiftThreshold  = 1.1
	predictMaxSelections  = 3
	classifierTemperature = 0.05
)

// IndexerOption configures LoadOrBuildClassifier behavior.
type IndexerOption func(*indexerOptions)

type indexerOptions struct {
	onProgress    func(done, total int)
	commandRunner ports.CommandRunner
}

// WithProgressCallback sets a callback invoked after each repo is indexed.
func WithProgressCallback(fn func(done, total int)) IndexerOption {
	return func(o *indexerOptions) { o.onProgress = fn }
}

// WithCommandRunner sets the command runner used by the indexer for git operations.
func WithCommandRunner(r ports.CommandRunner) IndexerOption {
	return func(o *indexerOptions) { o.commandRunner = r }
}

// RepoIndexEntry tracks the indexing state of a single repo.
type RepoIndexEntry struct {
	HeadCommit  string    `json:"head_commit"`
	LastIndexed time.Time `json:"last_indexed"`
	ContentHash string    `json:"content_hash"`
}

// FeatureIndexState tracks which historical features have been processed.
type FeatureIndexState struct {
	ProcessedIDs  map[string]bool `json:"processed_ids"`
	DistinctRepos map[string]bool `json:"distinct_repos"`
}

// ClassifierIndex is the top-level orchestrator holding the trained model and index state.
type ClassifierIndex struct {
	mu            sync.Mutex
	Classifier    *CNBClassifier            `json:"-"`
	RepoIndex     map[string]RepoIndexEntry `json:"repo_index"`
	FeatureIndex  FeatureIndexState         `json:"feature_index"`
	RepoTokens    map[string][]string       `json:"-"` // cached per-repo token lists for IDF
	StateDir      string                    `json:"-"`
	IDF           map[string]float64        `json:"-"`
	CommandRunner ports.CommandRunner       `json:"-"`
}

// LoadOrBuildClassifier loads a persisted classifier or builds one from scratch.
func LoadOrBuildClassifier(stateDir string, repos map[string]config.RepoConfig, featureStore ports.FeatureStore, opts ...IndexerOption) (*ClassifierIndex, error) {
	var o indexerOptions
	for _, opt := range opts {
		opt(&o)
	}

	dir := filepath.Join(stateDir, classifierDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating classifier dir: %w", err)
	}

	classifier, modelErr := loadModel(stateDir)
	repoIndex, featureIndex, repoTokens, stateErr := loadIndexState(stateDir)

	if stateErr != nil {
		repoIndex = make(map[string]RepoIndexEntry)
		featureIndex = FeatureIndexState{
			ProcessedIDs:  make(map[string]bool),
			DistinctRepos: make(map[string]bool),
		}
		repoTokens = make(map[string][]string)
	}

	cmdRunner := o.commandRunner
	if cmdRunner == nil {
		cmdRunner = &execCommandRunner{}
	}

	idx := &ClassifierIndex{
		Classifier:    classifier,
		RepoIndex:     repoIndex,
		FeatureIndex:  featureIndex,
		RepoTokens:    repoTokens,
		StateDir:      stateDir,
		CommandRunner: cmdRunner,
	}

	if modelErr != nil {
		// First startup — build from scratch
		if err := idx.fullBuild(repos, featureStore, o.onProgress); err != nil {
			return nil, fmt.Errorf("first startup build: %w", err)
		}
		return idx, nil
	}

	// Override temperature from disk with current constant
	idx.Classifier.Temperature = classifierTemperature

	// Determine staleness
	staleRepos := idx.findStaleRepos(repos)
	newRepos := idx.findNewRepos(repos)

	if len(staleRepos) > 0 || len(newRepos) > 0 {
		if err := idx.incrementalUpdate(repos, featureStore, staleRepos, newRepos, o.onProgress); err != nil {
			return nil, fmt.Errorf("incremental update: %w", err)
		}
		return idx, nil
	}

	// Check for new historical features
	newFeatures := idx.findNewHistoricalFeatures(featureStore)
	if len(newFeatures) > 0 {
		idx.rebuildIDF(repos)
		idx.processHistoricalFeatures(newFeatures)
		idx.Classifier.Finalize()
		_ = persistModel(stateDir, idx.Classifier)
		_ = persistIndexState(stateDir, idx.RepoIndex, idx.FeatureIndex, idx.RepoTokens)
		return idx, nil
	}

	// Nothing stale — just finalize and return
	idx.rebuildIDF(repos)
	idx.Classifier.Finalize()
	return idx, nil
}

// Predict returns repo names matching the given feature name+description.
func (idx *ClassifierIndex) Predict(name, description string, repos map[string]config.RepoConfig) []string {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	tokens := TokenizeAndProcess(name + " " + description)
	tfidfVec := ComputeTFIDF(tokens, idx.IDF)
	if len(tfidfVec) == 0 {
		return nil
	}

	scores, err := idx.Classifier.Predict(tfidfVec)
	if err != nil {
		return nil
	}
	selected := SelectByThreshold(scores, predictLiftThreshold, predictMaxSelections)

	// Filter to valid repo names
	var valid []string
	for _, s := range selected {
		if _, ok := repos[s]; ok {
			valid = append(valid, s)
		}
	}
	return valid
}

// CheckAndReindex checks for repo changes and new historical features, re-indexing if needed.
func (idx *ClassifierIndex) CheckAndReindex(repos map[string]config.RepoConfig, featureStore ports.FeatureStore) (bool, error) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	staleRepos := idx.findStaleRepos(repos)
	newRepos := idx.findNewRepos(repos)
	newFeatures := idx.findNewHistoricalFeatures(featureStore)

	if len(staleRepos) == 0 && len(newRepos) == 0 && len(newFeatures) == 0 {
		return false, nil
	}

	if len(staleRepos) > 0 || len(newRepos) > 0 {
		if err := idx.incrementalUpdate(repos, featureStore, staleRepos, newRepos, nil); err != nil {
			return false, fmt.Errorf("reindex: %w", err)
		}
		return true, nil
	}

	// Only new historical features
	idx.rebuildIDF(repos)
	idx.processHistoricalFeatures(newFeatures)
	idx.Classifier.Finalize()
	if err := persistModel(idx.StateDir, idx.Classifier); err != nil {
		log.Printf("classifier: persist model after reindex: %v", err)
	}
	if err := persistIndexState(idx.StateDir, idx.RepoIndex, idx.FeatureIndex, idx.RepoTokens); err != nil {
		log.Printf("classifier: persist index state after reindex: %v", err)
	}
	return true, nil
}

// LearnFromFeature incrementally trains the classifier from a completed feature.
func (idx *ClassifierIndex) LearnFromFeature(f *feature.Feature) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if idx.FeatureIndex.ProcessedIDs[f.ID] {
		return // already processed
	}

	idx.FeatureIndex.ProcessedIDs[f.ID] = true
	for _, repo := range f.Repos {
		idx.FeatureIndex.DistinctRepos[repo.Name] = true
	}

	weight := math.Min(maxHistoricalWeight, historicalWeightMul*float64(len(idx.FeatureIndex.DistinctRepos)))

	tokens := TokenizeAndProcess(f.Name + " " + f.Description)
	tfidfVec := ComputeTFIDF(tokens, idx.IDF)
	if len(tfidfVec) == 0 {
		return
	}

	for _, repo := range f.Repos {
		idx.Classifier.AddTrainingData(repo.Name, tfidfVec, weight)
	}
	idx.Classifier.Finalize()

	// Best-effort persistence
	if err := persistModel(idx.StateDir, idx.Classifier); err != nil {
		log.Printf("classifier: persist model after learn: %v", err)
	}
	if err := persistIndexState(idx.StateDir, idx.RepoIndex, idx.FeatureIndex, idx.RepoTokens); err != nil {
		log.Printf("classifier: persist index state after learn: %v", err)
	}
}

// fullBuild extracts features from all repos and builds the classifier from scratch.
func (idx *ClassifierIndex) fullBuild(repos map[string]config.RepoConfig, featureStore ports.FeatureStore, onProgress func(done, total int)) error {
	idx.Classifier = NewCNBClassifier(1.0, classifierTemperature)

	// Extract features from all repos
	var progressArgs []func(done, total int)
	if onProgress != nil {
		progressArgs = append(progressArgs, onProgress)
	}
	allFeatures := ExtractAllRepoFeatures(context.Background(), idx.CommandRunner, repos, firstStartupBudget, progressArgs...)

	// Build global IDF and cache per-repo tokens
	idx.RepoTokens = make(map[string][]string, len(allFeatures))
	for name, rf := range allFeatures {
		tokens := TokenizeAndProcess(ToText(rf))
		idx.RepoTokens[name] = tokens
	}
	idx.IDF = BuildIDF(idx.RepoTokens)

	// Fit each repo
	for name, tokens := range idx.RepoTokens {
		tfidfVec := ComputeTFIDF(tokens, idx.IDF)
		if len(tfidfVec) > 0 {
			idx.Classifier.Fit(name, tfidfVec, contentWeight)
		}
	}

	// Update repo index entries
	for name, rc := range repos {
		head, _ := getGitHead(context.Background(), idx.CommandRunner, rc.Path)
		entry := RepoIndexEntry{
			HeadCommit:  head,
			LastIndexed: time.Now(),
		}
		if rf, ok := allFeatures[name]; ok {
			entry.ContentHash = computeContentHash(rf)
		}
		idx.RepoIndex[name] = entry
	}

	// Process historical features
	idx.processAllHistoricalFeatures(featureStore)

	idx.Classifier.Finalize()

	if err := persistModel(idx.StateDir, idx.Classifier); err != nil {
		return fmt.Errorf("persisting model: %w", err)
	}
	if err := persistIndexState(idx.StateDir, idx.RepoIndex, idx.FeatureIndex, idx.RepoTokens); err != nil {
		return fmt.Errorf("persisting index state: %w", err)
	}
	return nil
}

// incrementalUpdate re-processes only stale/new repos and updates the classifier.
// To maintain consistent TF-IDF space, all class vectors are rebuilt when IDF changes.
func (idx *ClassifierIndex) incrementalUpdate(repos map[string]config.RepoConfig, featureStore ports.FeatureStore, staleRepos, newRepos []string, onProgress func(done, total int)) error {
	// Build subset of repos to extract
	toExtract := make(map[string]config.RepoConfig)
	for _, name := range staleRepos {
		if rc, ok := repos[name]; ok {
			toExtract[name] = rc
		}
	}
	for _, name := range newRepos {
		if rc, ok := repos[name]; ok {
			toExtract[name] = rc
		}
	}

	// Extract features for changed repos only
	var progressArgs []func(done, total int)
	if onProgress != nil {
		progressArgs = append(progressArgs, onProgress)
	}
	extracted := ExtractAllRepoFeatures(context.Background(), idx.CommandRunner, toExtract, reindexBudget, progressArgs...)

	// Update cached token lists for changed/new repos
	for name, rf := range extracted {
		idx.RepoTokens[name] = TokenizeAndProcess(ToText(rf))
	}

	// Rebuild IDF from ALL cached repo tokens (ensures consistent weighting)
	idx.rebuildIDF(repos)

	// Rebuild entire classifier so all classes use the same IDF space.
	// Without this, unchanged classes would have vectors from old IDF
	// while changed classes use new IDF — mathematically inconsistent.
	idx.Classifier = NewCNBClassifier(1.0, classifierTemperature)
	for name, tokens := range idx.RepoTokens {
		tfidfVec := ComputeTFIDF(tokens, idx.IDF)
		if len(tfidfVec) > 0 {
			idx.Classifier.Fit(name, tfidfVec, contentWeight)
		}
	}

	// Re-apply ALL previously-processed historical features.
	// Since we rebuilt the classifier from scratch, historical signal
	// would be lost without this replay.
	idx.reapplyProcessedHistoricalFeatures(featureStore)

	// Process new historical features (updates ProcessedIDs)
	newFeatures := idx.findNewHistoricalFeatures(featureStore)
	idx.processHistoricalFeatures(newFeatures)

	// Update repo index entries for changed repos
	for name, rf := range extracted {
		head, _ := getGitHead(context.Background(), idx.CommandRunner, repos[name].Path)
		idx.RepoIndex[name] = RepoIndexEntry{
			HeadCommit:  head,
			LastIndexed: time.Now(),
			ContentHash: computeContentHash(rf),
		}
	}

	idx.Classifier.Finalize()
	if err := persistModel(idx.StateDir, idx.Classifier); err != nil {
		log.Printf("classifier: persist model after incremental update: %v", err)
	}
	if err := persistIndexState(idx.StateDir, idx.RepoIndex, idx.FeatureIndex, idx.RepoTokens); err != nil {
		log.Printf("classifier: persist index state after incremental update: %v", err)
	}
	return nil
}

// rebuildIDF reconstructs the IDF from cached per-repo token lists.
// Uses real tokenized repo content (not classifier feature keys) for correct IDF.
func (idx *ClassifierIndex) rebuildIDF(repos map[string]config.RepoConfig) {
	docTokens := make(map[string][]string, len(idx.RepoTokens))
	for name, tokens := range idx.RepoTokens {
		docTokens[name] = tokens
	}

	// Include repos that might not have cached tokens yet
	for name := range repos {
		if _, ok := docTokens[name]; !ok {
			docTokens[name] = nil
		}
	}

	if len(docTokens) > 0 {
		idx.IDF = BuildIDF(docTokens)
	} else {
		idx.IDF = make(map[string]float64)
	}
}

// processAllHistoricalFeatures loads all terminal features and trains from them.
func (idx *ClassifierIndex) processAllHistoricalFeatures(featureStore ports.FeatureStore) {
	if featureStore == nil {
		return
	}
	features, err := featureStore.List()
	if err != nil && !feature.IsPartialLoadError(err) {
		return
	}
	var terminal []*feature.Feature
	for _, f := range features {
		if isTerminalFeature(f) {
			terminal = append(terminal, f)
		}
	}
	idx.processHistoricalFeatures(terminal)
}

// processHistoricalFeatures trains the classifier from a list of features.
func (idx *ClassifierIndex) processHistoricalFeatures(features []*feature.Feature) {
	for _, f := range features {
		if idx.FeatureIndex.ProcessedIDs[f.ID] {
			continue
		}
		idx.FeatureIndex.ProcessedIDs[f.ID] = true
		for _, repo := range f.Repos {
			idx.FeatureIndex.DistinctRepos[repo.Name] = true
		}

		weight := math.Min(maxHistoricalWeight, historicalWeightMul*float64(len(idx.FeatureIndex.DistinctRepos)))

		tokens := TokenizeAndProcess(f.Name + " " + f.Description)
		tfidfVec := ComputeTFIDF(tokens, idx.IDF)
		if len(tfidfVec) == 0 {
			continue
		}
		for _, repo := range f.Repos {
			idx.Classifier.AddTrainingData(repo.Name, tfidfVec, weight)
		}
	}
}

// reapplyProcessedHistoricalFeatures re-applies training data from all previously
// processed historical features. Used after classifier rebuild to restore historical
// signal that would otherwise be lost when class vectors are recreated.
// Does NOT modify ProcessedIDs or DistinctRepos (those are already tracked).
func (idx *ClassifierIndex) reapplyProcessedHistoricalFeatures(featureStore ports.FeatureStore) {
	if featureStore == nil {
		return
	}
	features, err := featureStore.List()
	if err != nil && !feature.IsPartialLoadError(err) {
		return
	}
	for _, f := range features {
		if !isTerminalFeature(f) || !idx.FeatureIndex.ProcessedIDs[f.ID] {
			continue // only replay previously processed features
		}
		weight := math.Min(maxHistoricalWeight, historicalWeightMul*float64(len(idx.FeatureIndex.DistinctRepos)))
		tokens := TokenizeAndProcess(f.Name + " " + f.Description)
		tfidfVec := ComputeTFIDF(tokens, idx.IDF)
		if len(tfidfVec) == 0 {
			continue
		}
		for _, repo := range f.Repos {
			idx.Classifier.AddTrainingData(repo.Name, tfidfVec, weight)
		}
	}
}

// findStaleRepos returns repo names whose HEAD has changed since last indexing.
func (idx *ClassifierIndex) findStaleRepos(repos map[string]config.RepoConfig) []string {
	var stale []string
	for name, rc := range repos {
		entry, exists := idx.RepoIndex[name]
		if !exists {
			continue // new repo, handled by findNewRepos
		}
		head, err := getGitHead(context.Background(), idx.CommandRunner, rc.Path)
		if err != nil {
			continue // skip repos where git HEAD can't be determined
		}
		if head != entry.HeadCommit {
			stale = append(stale, name)
		}
	}
	return stale
}

// findNewRepos returns repo names not in the current index.
func (idx *ClassifierIndex) findNewRepos(repos map[string]config.RepoConfig) []string {
	var newRepos []string
	for name := range repos {
		if _, exists := idx.RepoIndex[name]; !exists {
			newRepos = append(newRepos, name)
		}
	}
	return newRepos
}

// findNewHistoricalFeatures returns terminal features not yet processed.
func (idx *ClassifierIndex) findNewHistoricalFeatures(featureStore ports.FeatureStore) []*feature.Feature {
	if featureStore == nil {
		return nil
	}
	features, err := featureStore.List()
	if err != nil && !feature.IsPartialLoadError(err) {
		return nil
	}
	var newFeatures []*feature.Feature
	for _, f := range features {
		if isTerminalFeature(f) && !idx.FeatureIndex.ProcessedIDs[f.ID] {
			newFeatures = append(newFeatures, f)
		}
	}
	return newFeatures
}

// isTerminalFeature returns true if the feature is in a completed state.
func isTerminalFeature(f *feature.Feature) bool {
	return f.Status == feature.StatusDone ||
		f.Status == feature.StatusPublished
}

// Persistence helpers

func persistModel(stateDir string, classifier *CNBClassifier) error {
	data, err := json.MarshalIndent(classifier, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling model: %w", err)
	}
	path := filepath.Join(stateDir, classifierDir, modelFile)
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("writing temp model: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("renaming model: %w", err)
	}
	return nil
}

type indexState struct {
	RepoIndex    map[string]RepoIndexEntry `json:"repo_index"`
	FeatureIndex FeatureIndexState         `json:"feature_index"`
	RepoTokens   map[string][]string       `json:"repo_tokens"`
}

func persistIndexState(stateDir string, repoIndex map[string]RepoIndexEntry, featureIndex FeatureIndexState, repoTokens map[string][]string) error {
	state := indexState{
		RepoIndex:    repoIndex,
		FeatureIndex: featureIndex,
		RepoTokens:   repoTokens,
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling index state: %w", err)
	}
	path := filepath.Join(stateDir, classifierDir, indexStateFile)
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("writing temp index state: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("renaming index state: %w", err)
	}
	return nil
}

func loadModel(stateDir string) (*CNBClassifier, error) {
	path := filepath.Join(stateDir, classifierDir, modelFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c CNBClassifier
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parsing model: %w", err)
	}
	return &c, nil
}

func loadIndexState(stateDir string) (map[string]RepoIndexEntry, FeatureIndexState, map[string][]string, error) {
	path := filepath.Join(stateDir, classifierDir, indexStateFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, FeatureIndexState{}, nil, err
	}
	var state indexState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, FeatureIndexState{}, nil, fmt.Errorf("parsing index state: %w", err)
	}
	if state.RepoIndex == nil {
		state.RepoIndex = make(map[string]RepoIndexEntry)
	}
	if state.FeatureIndex.ProcessedIDs == nil {
		state.FeatureIndex.ProcessedIDs = make(map[string]bool)
	}
	if state.FeatureIndex.DistinctRepos == nil {
		state.FeatureIndex.DistinctRepos = make(map[string]bool)
	}
	if state.RepoTokens == nil {
		state.RepoTokens = make(map[string][]string)
	}
	return state.RepoIndex, state.FeatureIndex, state.RepoTokens, nil
}

func getGitHead(ctx context.Context, runner ports.CommandRunner, repoPath string) (string, error) {
	out, err := runner.Run(ctx, "git", []string{"-C", repoPath, "rev-parse", "HEAD"}, ports.CommandOpts{})
	if err != nil {
		return "", fmt.Errorf("git rev-parse HEAD: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func computeContentHash(features *RepoFeatures) string {
	hash := sha256.Sum256([]byte(ToText(features)))
	return fmt.Sprintf("%x", hash)
}
