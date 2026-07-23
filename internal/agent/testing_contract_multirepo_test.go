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

func TestCompileTestingContractMultiRepo_PerRepoPlanCommands(t *testing.T) {
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

	if got := countItems(c.Items, testingContractPlanSource, testRepoNameAPI); got != 1 {
		t.Errorf("api plan rows = %d, want 1", got)
	}
	if got := countItems(c.Items, testingContractPlanSource, testRepoNameWeb); got != 1 {
		t.Errorf("web plan rows = %d, want 1", got)
	}
}

func TestCompileTestingContractMultiRepo_ExplicitTopLevelRepoScopes(t *testing.T) {
	in := MultiRepoContractInput{
		Repos:    []string{testRepoNameAPI, testRepoNameWeb},
		PlanText: "### Automated Verification\n- [ ] [repo: api] API tests: `go test ./...`\n- [ ] [repo: web] Web tests: `npm test`\n",
	}
	c := CompileTestingContractMultiRepo(in)
	if got := countItems(c.Items, testingContractPlanSource, testRepoNameAPI); got != 1 {
		t.Errorf("api rows = %d, want 1", got)
	}
	if got := countItems(c.Items, testingContractPlanSource, testRepoNameWeb); got != 1 {
		t.Errorf("web rows = %d, want 1", got)
	}
}

func TestCompileTestingContractMultiRepo_CrossRepoStepsFromPlan(t *testing.T) {
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
		"## Cross-Repo Verification",
		"- e2e smoke: `scripts/e2e.sh`",
		"- [ ] contract test: `scripts/contract-test.sh`",
	}, "\n")
	c := CompileTestingContractMultiRepo(MultiRepoContractInput{
		Repos:    []string{testRepoNameAPI, testRepoNameWeb},
		PlanText: plan,
	})
	if got := countItems(c.Items, testingContractCrossRepoSource, TestingContractCrossRepoTag); got != 2 {
		t.Fatalf("cross-repo rows = %d, want 2", got)
	}
	wantCmds := map[string]bool{"scripts/e2e.sh": false, "scripts/contract-test.sh": false}
	for _, it := range c.Items {
		if it.Source != testingContractCrossRepoSource {
			continue
		}
		if _, ok := wantCmds[it.Command]; !ok {
			t.Errorf("unexpected cross-repo command %q", it.Command)
			continue
		}
		wantCmds[it.Command] = true
	}
	for cmd, seen := range wantCmds {
		if !seen {
			t.Errorf("missing cross-repo command %q", cmd)
		}
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
	if len(c.Items) != 0 {
		t.Errorf("plan-less mode emitted items: %+v", c.Items)
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
	for _, it := range c.Items {
		if it.Repo != "solo" {
			t.Errorf("single-repo command repo = %q, want solo", it.Repo)
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

func TestCompileTestingContractMultiRepo_TopLevelVerificationUsesExplicitScopes(t *testing.T) {
	plan := strings.Join([]string{
		"#### Automated Verification:",
		"- [ ] [repo: api] api check: `go test ./...`",
		"- [ ] [repo: web] web check: `npm test`",
	}, "\n")
	in := MultiRepoContractInput{
		Repos:    []string{testRepoNameAPI, testRepoNameWeb},
		PlanText: plan,
	}
	c := CompileTestingContractMultiRepo(in)
	if got := countItems(c.Items, testingContractPlanSource, testRepoNameAPI); got != 1 {
		t.Errorf("api plan rows = %d, want 1 explicitly scoped command", got)
	}
	if got := countItems(c.Items, testingContractPlanSource, testRepoNameWeb); got != 1 {
		t.Errorf("web plan rows = %d, want 1 explicitly scoped command", got)
	}
}

func TestCompileTestingContractMultiRepo_SuccessCriteriaVerificationDoesNotFanOut(t *testing.T) {
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
		"- [ ] [repo: api] API suite passes: `go test ./...`",
	}, "\n")
	c := CompileTestingContractMultiRepo(MultiRepoContractInput{
		Repos:    []string{testRepoNameAPI, testRepoNameWeb},
		PlanText: plan,
	})
	if got := countItems(c.Items, testingContractPlanSource, testRepoNameAPI); got != 1 {
		t.Errorf("api plan rows = %d, want 1 explicitly scoped command", got)
	}
	if got := countItems(c.Items, testingContractPlanSource, testRepoNameWeb); got != 0 {
		t.Errorf("web plan rows = %d, want no accidental fan-out", got)
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
