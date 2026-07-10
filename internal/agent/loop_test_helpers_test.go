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
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

type loopTestFeatureOptions struct {
	Name           string
	Slug           string
	Description    string
	ExitCriteria   string
	RefactorPrompt string
}

func newLoopTestFeature(t *testing.T, stateDir, featureID string, repoNames []string, opts loopTestFeatureOptions) (*feature.Store, *feature.Feature, []string) {
	t.Helper()

	store := feature.NewStore(stateDir)
	repos := make([]feature.FeatureRepo, 0, len(repoNames))
	repoPaths := make([]string, 0, len(repoNames))
	repoStates := map[string]*feature.RepoState{}
	for _, name := range repoNames {
		repoDir := filepath.Join(t.TempDir(), name)
		if err := os.MkdirAll(repoDir, 0o755); err != nil {
			t.Fatalf("mkdir repo %q: %v", name, err)
		}
		repos = append(repos, feature.FeatureRepo{
			Name:       name,
			Path:       repoDir,
			BaseBranch: "main",
		})
		repoPaths = append(repoPaths, repoDir)
		repoStates[name] = &feature.RepoState{
			Touched: true,
			PRURL:   fmt.Sprintf("https://github.com/example/%s/pull/1", name),
		}
	}

	f := &feature.Feature{
		ID:             featureID,
		Name:           opts.Name,
		Slug:           opts.Slug,
		Description:    opts.Description,
		Status:         feature.StatusPublished,
		ActiveRun:      1,
		RunCount:       1,
		SchemaVersion:  feature.SchemaVersionCurrent,
		Repos:          repos,
		RepoStates:     repoStates,
		ExitCriteria:   opts.ExitCriteria,
		RefactorPrompt: opts.RefactorPrompt,
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}

	loaded, err := store.Load(featureID)
	if err != nil {
		t.Fatalf("reload feature: %v", err)
	}
	return store, loaded, repoPaths
}
