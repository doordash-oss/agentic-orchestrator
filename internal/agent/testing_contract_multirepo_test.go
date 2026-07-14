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
	"strings"
	"testing"
)

func countItems(items []TestingContractItem, source, repo string) int {
	n := 0
	for _, it := range items {
		if it.Source == source && it.Repo == repo {
			n++
		}
	}
	return n
}

func TestCompileTestingContractMultiRepo_PerRepoBaseline(t *testing.T) {
	plan := strings.Join([]string{
		"## Tasks",
		"### Task 1: api work",
		"**Repo:** `api`",
		"#### Automated Verification:",
		"- [ ] api tests: `go test ./api/... -count=1`",
		"### Task 2: web work",
		"**Repo:** `web`",
		"#### Automated Verification:",
		"- [ ] web tests: `npm test`",
	}, "\n")
	in := MultiRepoContractInput{
		Repos:    []string{testRepoNameAPI, testRepoNameWeb},
		PlanText: plan,
		PlanPath: "/state/runs/run-001/phase-01/plan/approved.md",
	}
	c := CompileTestingContractMultiRepo(in)

	baselineCount := len(DefaultBaselineVerificationSteps())
	if got := countItems(c.Items, testingContractBaselineSource, testRepoNameAPI); got != baselineCount {
		t.Errorf("api baseline rows = %d, want %d", got, baselineCount)
	}
	if got := countItems(c.Items, testingContractBaselineSource, testRepoNameWeb); got != baselineCount {
		t.Errorf("web baseline rows = %d, want %d", got, baselineCount)
	}

	if got := countItems(c.Items, testingContractPlanSource, testRepoNameAPI); got != 1 {
		t.Errorf("api plan rows = %d, want 1", got)
	}
	if got := countItems(c.Items, testingContractPlanSource, testRepoNameWeb); got != 1 {
		t.Errorf("web plan rows = %d, want 1", got)
	}
}

func TestCompileTestingContractMultiRepo_CrossRepoSteps(t *testing.T) {
	in := MultiRepoContractInput{
		Repos:    []string{testRepoNameAPI, testRepoNameWeb},
		PlanText: "## Tasks\n### Task 1\n**Repo:** `api`\n",
		CrossRepoSteps: []VerificationStep{
			{Description: "End-to-end smoke", Command: "scripts/e2e.sh"},
		},
	}
	c := CompileTestingContractMultiRepo(in)
	if got := countItems(c.Items, testingContractCrossRepoSource, TestingContractCrossRepoTag); got != 1 {
		t.Errorf("cross-repo rows = %d, want 1", got)
	}
}

func TestCompileTestingContractMultiRepo_PlanLessNoPlanItems(t *testing.T) {
	plan := strings.Join([]string{
		"## Tasks",
		"### Task 1: api work",
		"**Repo:** `api`",
		"#### Automated Verification:",
		"- [ ] api tests: `go test ./api/... -count=1`",
	}, "\n")
	in := MultiRepoContractInput{
		Repos:    []string{testRepoNameAPI},
		PlanText: plan,
		PlanLess: true,
	}
	c := CompileTestingContractMultiRepo(in)
	for _, it := range c.Items {
		if it.Source == testingContractPlanSource {
			t.Errorf("plan-less mode emitted plan-source item: %+v", it)
		}
	}
	// Baseline should still be emitted.
	baselineCount := len(DefaultBaselineVerificationSteps())
	if got := countItems(c.Items, testingContractBaselineSource, testRepoNameAPI); got != baselineCount {
		t.Errorf("baseline rows = %d, want %d", got, baselineCount)
	}
}

func TestCompileTestingContractMultiRepo_SingleRepoDegenerate(t *testing.T) {
	plan := strings.Join([]string{
		"#### Automated Verification:",
		"- [ ] core test: `go test ./...`",
	}, "\n")
	in := MultiRepoContractInput{
		Repos:    []string{"solo"},
		PlanText: plan,
	}
	c := CompileTestingContractMultiRepo(in)
	baselineCount := len(DefaultBaselineVerificationSteps())
	if got := countItems(c.Items, testingContractBaselineSource, "solo"); got != baselineCount {
		t.Errorf("solo baseline = %d, want %d", got, baselineCount)
	}
	for _, it := range c.Items {
		if it.Source == testingContractCrossRepoSource {
			t.Errorf("single-repo phase should have no cross-repo items, got %+v", it)
		}
	}
}

func TestCompileTestingContractMultiRepo_EveryItemTagged(t *testing.T) {
	plan := strings.Join([]string{
		"## Tasks",
		"### Task 1: api",
		"**Repo:** `api`",
		"#### Automated Verification:",
		"- [ ] api: `go test ./api/...`",
	}, "\n")
	in := MultiRepoContractInput{
		Repos:    []string{testRepoNameAPI, testRepoNameWeb},
		PlanText: plan,
		CrossRepoSteps: []VerificationStep{
			{Description: "smoke", Command: "scripts/e2e.sh"},
		},
	}
	c := CompileTestingContractMultiRepo(in)
	for _, it := range c.Items {
		if it.Repo == "" {
			t.Errorf("item has empty repo tag: %+v", it)
		}
	}
}

func TestCompileTestingContractMultiRepo_DistinctIDsPerRepo(t *testing.T) {
	in := MultiRepoContractInput{
		Repos: []string{testRepoNameAPI, testRepoNameWeb},
	}
	c := CompileTestingContractMultiRepo(in)
	ids := make(map[string]bool)
	for _, it := range c.Items {
		if ids[it.ID] {
			t.Errorf("duplicate item ID %s", it.ID)
		}
		ids[it.ID] = true
	}
}

func TestCompileTestingContractMultiRepo_TopLevelVerificationFanOut(t *testing.T) {
	plan := strings.Join([]string{
		"#### Automated Verification:",
		"- [ ] cross check: `make verify`",
	}, "\n")
	in := MultiRepoContractInput{
		Repos:    []string{testRepoNameAPI, testRepoNameWeb},
		PlanText: plan,
	}
	c := CompileTestingContractMultiRepo(in)
	if got := countItems(c.Items, testingContractPlanSource, testRepoNameAPI); got != 1 {
		t.Errorf("api plan rows = %d, want 1 (top-level fanned to api)", got)
	}
	if got := countItems(c.Items, testingContractPlanSource, testRepoNameWeb); got != 1 {
		t.Errorf("web plan rows = %d, want 1 (top-level fanned to web)", got)
	}
}

func TestCompileTestingContractMultiRepo_SuccessCriteriaVerificationFanOut(t *testing.T) {
	plan := strings.Join([]string{
		"## Overview",
		"Do the slice.",
		"## Tasks",
		"### Task 1: api work",
		"**Repo:** `api`",
		"#### What to build",
		"Do api work.",
		"## Success Criteria",
		"### Automated Verification",
		"- [ ] Full suite passes: `make verify`",
	}, "\n")
	c := CompileTestingContractMultiRepo(MultiRepoContractInput{
		Repos:    []string{testRepoNameAPI, testRepoNameWeb},
		PlanText: plan,
	})
	if got := countItems(c.Items, testingContractPlanSource, testRepoNameAPI); got != 1 {
		t.Errorf("api plan rows = %d, want 1 (success criteria fanned to api)", got)
	}
	if got := countItems(c.Items, testingContractPlanSource, testRepoNameWeb); got != 1 {
		t.Errorf("web plan rows = %d, want 1 (success criteria fanned to web)", got)
	}
}

func TestCompileTestingContractMultiRepo_ManualVerificationIsCrossRepo(t *testing.T) {
	plan := strings.Join([]string{
		"## Tasks",
		"### Task 1: api work",
		"**Repo:** `api`",
		"#### What to build",
		"Do api work.",
		"### Task 2: web work",
		"**Repo:** `web`",
		"#### What to build",
		"Do web work.",
		"## Success Criteria",
		"### Manual Verification",
		"- [ ] Complete the end-to-end sign-in flow in a browser.",
	}, "\n")
	c := CompileTestingContractMultiRepo(MultiRepoContractInput{
		Repos:    []string{testRepoNameAPI, testRepoNameWeb},
		PlanText: plan,
	})

	var got *TestingContractItem
	for i := range c.Items {
		if c.Items[i].Source == testingContractManualSource {
			got = &c.Items[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("missing manual item: %+v", c.Items)
	}
	if got.Repo != TestingContractCrossRepoTag {
		t.Fatalf("manual repo = %q, want %q", got.Repo, TestingContractCrossRepoTag)
	}
	if got.ExpectedEvidence.Kind != testingContractManualKind {
		t.Fatalf("manual evidence kind = %q", got.ExpectedEvidence.Kind)
	}
}

func TestCompileTestingContractMultiRepo_EvidenceRowsSingleRepo(t *testing.T) {
	plan := strings.Join([]string{
		"## Success Criteria",
		"### Visual Evidence",
		"- [ ] Capture the settings panel after saving.",
		"### Behavioral Evidence",
		"- [ ] Attach the successful save transcript.",
	}, "\n")

	c := CompileTestingContractMultiRepo(MultiRepoContractInput{
		Repos:    []string{"app"},
		PlanText: plan,
	})

	if got := countItems(c.Items, testingContractVisualSource, "app"); got != 1 {
		t.Fatalf("single-repo visual rows = %d, want 1", got)
	}
	if got := countItems(c.Items, testingContractBehavioralSource, "app"); got != 1 {
		t.Fatalf("single-repo behavioral rows = %d, want 1", got)
	}
}

func TestCompileTestingContractMultiRepo_EvidenceRowsMultiRepoAreCrossRepo(t *testing.T) {
	plan := strings.Join([]string{
		"## Success Criteria",
		"### Visual Evidence",
		"- [ ] Capture the web and API status surfaces together.",
		"### Behavioral Evidence",
		"- [ ] Attach the end-to-end workflow recording.",
	}, "\n")

	c := CompileTestingContractMultiRepo(MultiRepoContractInput{
		Repos:    []string{testRepoNameAPI, testRepoNameWeb},
		PlanText: plan,
	})

	if got := countItems(c.Items, testingContractVisualSource, TestingContractCrossRepoTag); got != 1 {
		t.Fatalf("multi-repo visual rows = %d, want 1", got)
	}
	if got := countItems(c.Items, testingContractBehavioralSource, TestingContractCrossRepoTag); got != 1 {
		t.Fatalf("multi-repo behavioral rows = %d, want 1", got)
	}
}

func TestCompileTestingContractMultiRepo_PlanLessNoEvidenceRows(t *testing.T) {
	plan := strings.Join([]string{
		"## Success Criteria",
		"### Visual Evidence",
		"- [ ] Capture the screen.",
		"### Behavioral Evidence",
		"- [ ] Attach the transcript.",
	}, "\n")

	c := CompileTestingContractMultiRepo(MultiRepoContractInput{
		Repos:    []string{testRepoNameAPI, testRepoNameWeb},
		PlanText: plan,
		PlanLess: true,
	})

	for _, it := range c.Items {
		if it.Source == testingContractVisualSource || it.Source == testingContractBehavioralSource {
			t.Fatalf("plan-less contract emitted evidence row: %+v", it)
		}
	}
}
