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
	"reflect"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

func TestPhaseScope_SingleRepoUntagged(t *testing.T) {
	feat := &feature.Feature{
		Repos: []feature.FeatureRepo{{Name: "solo"}},
	}
	plan := `## Tasks

### Task 1: do work

Some details.
`
	got := PhaseScopeFromText(feat, plan)
	if !got.ScopeOK() {
		t.Fatalf("expected ScopeOK, got issues: %s", got.IssueSummary())
	}
	if !reflect.DeepEqual(got.Repos, []string{"solo"}) {
		t.Fatalf("repos = %v, want [solo]", got.Repos)
	}
}

func TestPhaseScope_MultiRepoTagged(t *testing.T) {
	feat := &feature.Feature{
		Repos: []feature.FeatureRepo{{Name: testRepoNameAPI}, {Name: testRepoNameWeb}, {Name: testRepoNameInfra}},
	}
	plan := `## Tasks

### Task 1: api work
**Repo:** ` + "`api`" + `

Body for api.

### Task 2: web work
**Repo:** ` + "`web`" + `

Body for web.
`
	got := PhaseScopeFromText(feat, plan)
	if !got.ScopeOK() {
		t.Fatalf("expected ScopeOK, got issues: %s", got.IssueSummary())
	}
	if !reflect.DeepEqual(got.Repos, []string{testRepoNameAPI, testRepoNameWeb}) {
		t.Fatalf("repos = %v, want [api web]", got.Repos)
	}
}

func TestPhaseScope_MultiRepoUntaggedTaskRejected(t *testing.T) {
	feat := &feature.Feature{
		Repos: []feature.FeatureRepo{{Name: testRepoNameAPI}, {Name: testRepoNameWeb}},
	}
	plan := `## Tasks

### Task 1: tagged
**Repo:** ` + "`api`" + `

Body.

### Task 2: untagged

Body.
`
	got := PhaseScopeFromText(feat, plan)
	if got.ScopeOK() {
		t.Fatalf("expected validation failure, got OK with repos %v", got.Repos)
	}
	found := false
	for _, iss := range got.Issues {
		if iss.Code == "untagged-task" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected untagged-task issue, got %+v", got.Issues)
	}
}

func TestPhaseScope_RepoNotInFeature(t *testing.T) {
	feat := &feature.Feature{
		Repos: []feature.FeatureRepo{{Name: testRepoNameAPI}, {Name: testRepoNameWeb}},
	}
	plan := `## Tasks

### Task 1: foreign
**Repo:** ` + "`infra`" + `

Body.
`
	got := PhaseScopeFromText(feat, plan)
	if got.ScopeOK() {
		t.Fatalf("expected validation failure, got OK")
	}
	found := false
	for _, iss := range got.Issues {
		if iss.Code == "repo-not-in-feature" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected repo-not-in-feature issue, got %+v", got.Issues)
	}
}

func TestPhaseScope_EmptyPlan(t *testing.T) {
	feat := &feature.Feature{
		Repos: []feature.FeatureRepo{{Name: testRepoNameAPI}},
	}
	got := PhaseScopeFromText(feat, "# Plan\n\nNo tasks here.\n")
	if got.ScopeOK() {
		t.Fatalf("expected validation failure for empty plan")
	}
	if got.Issues[0].Code != "no-tasks" {
		t.Fatalf("expected no-tasks issue, got %+v", got.Issues)
	}
}

func TestPhaseScope_PartialSubset(t *testing.T) {
	feat := &feature.Feature{
		Repos: []feature.FeatureRepo{{Name: testRepoNameAPI}, {Name: testRepoNameWeb}, {Name: testRepoNameInfra}},
	}
	plan := `## Tasks

### Task 1: api only
**Repo:** ` + "`api`" + `

Body.
`
	got := PhaseScopeFromText(feat, plan)
	if !got.ScopeOK() {
		t.Fatalf("expected OK, got %s", got.IssueSummary())
	}
	if !reflect.DeepEqual(got.Repos, []string{testRepoNameAPI}) {
		t.Fatalf("expected [api] (partial subset), got %v", got.Repos)
	}
}

func TestPhaseScope_FromFile(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.md")
	plan := `## Tasks

### Task 1: do
**Repo:** ` + "`api`" + `

Body.
`
	if err := os.WriteFile(planPath, []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}
	feat := &feature.Feature{Repos: []feature.FeatureRepo{{Name: testRepoNameAPI}, {Name: testRepoNameWeb}}}
	res, err := PhaseScope(feat, planPath)
	if err != nil {
		t.Fatalf("PhaseScope: %v", err)
	}
	if !res.ScopeOK() || !reflect.DeepEqual(res.Repos, []string{testRepoNameAPI}) {
		t.Fatalf("PhaseScope file path: repos=%v ok=%v", res.Repos, res.ScopeOK())
	}
}

func TestPhaseScope_NilFeature(t *testing.T) {
	if _, err := PhaseScope(nil, "/nonexistent"); err == nil {
		t.Fatalf("expected error for nil feature")
	}
}
