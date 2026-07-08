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
	"reflect"
	"strings"
	"testing"
)

const samplePlanMultiRepo = `# Phase 1: Translate API — Implementation Plan

## Overview

Stuff.

## Tasks

### Task 1: Wire the helper

**Repo:** ` + "`api`" + `

**Files:**
- Create: ` + "`internal/foo/bar.go`" + `

- [ ] **RED**

` + "```go" + `
func TestX(t *testing.T) {}
` + "```" + `

### Task 2: Update the web client

**Repo:** ` + "`web`" + `

**Files:**
- Modify: ` + "`web/src/api.ts`" + `

- [ ] **GREEN**

### Task 3: Add a second api task

**Repo:** ` + "`api`" + `

**Files:**
- Modify: ` + "`internal/foo/quux.go`" + `

## Success Criteria

### Automated

- [ ] tests pass
`

const samplePlanSingleRepo = `# Phase 1 — Plan

## Tasks

### Task 1: First

**Files:**
- Create: ` + "`a.go`" + `

### Task 2: Second

**Files:**
- Modify: ` + "`b.go`" + `

## Success Criteria
`

func TestParsePlanTasks_MultiRepo(t *testing.T) {
	tasks := ParsePlanTasks(samplePlanMultiRepo)
	if len(tasks) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(tasks))
	}
	want := []struct {
		heading string
		repo    string
	}{
		{"### Task 1: Wire the helper", testRepoNameAPI},
		{"### Task 2: Update the web client", testRepoNameWeb},
		{"### Task 3: Add a second api task", testRepoNameAPI},
	}
	for i, w := range want {
		if tasks[i].Heading != w.heading {
			t.Errorf("task %d heading = %q, want %q", i, tasks[i].Heading, w.heading)
		}
		if tasks[i].Repo != w.repo {
			t.Errorf("task %d repo = %q, want %q", i, tasks[i].Repo, w.repo)
		}
	}
}

func TestParsePlanTasks_SingleRepoNoTags(t *testing.T) {
	tasks := ParsePlanTasks(samplePlanSingleRepo)
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(tasks))
	}
	for i, tt := range tasks {
		if tt.Repo != "" {
			t.Errorf("task %d repo = %q, want empty", i, tt.Repo)
		}
	}
}

func TestParsePlanTasks_NoTasksSection(t *testing.T) {
	tasks := ParsePlanTasks("# Plan\n\n## Overview\n\nstuff\n")
	if tasks != nil {
		t.Fatalf("expected nil, got %+v", tasks)
	}
}

func TestPlanTaskRepos(t *testing.T) {
	got := PlanTaskRepos(samplePlanMultiRepo)
	want := []string{testRepoNameAPI, testRepoNameWeb}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("repos = %v, want %v", got, want)
	}
}

func TestPlanTaskRepos_SingleRepoEmptyTags(t *testing.T) {
	got := PlanTaskRepos(samplePlanSingleRepo)
	if got != nil {
		t.Errorf("expected nil for untagged plan, got %v", got)
	}
}

func TestParsePhasePlanFrontend(t *testing.T) {
	tests := []struct {
		name string
		plan string
		want bool
	}{
		{
			name: "true",
			plan: "# Phase 1\n\n" +
				"## Metadata\n\n" +
				"**Frontend:** true\n\n" +
				"## Overview\nBody.\n",
			want: true,
		},
		{
			name: "false",
			plan: "# Phase 1\n\n" +
				"## Metadata\n\n" +
				"**Frontend:** FALSE\n\n" +
				"## Overview\nBody.\n",
			want: false,
		},
		{
			name: "legacy no metadata",
			plan: "# Phase 1\n\n" +
				"## Overview\nBody.\n",
			want: false,
		},
		{
			name: "unrecognized value defaults false",
			plan: "# Phase 1\n\n" +
				"## Metadata\n\n" +
				"**Frontend:** maybe\n\n" +
				"## Overview\nBody.\n",
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParsePhasePlanFrontend(tt.plan)
			if got != tt.want {
				t.Errorf("ParsePhasePlanFrontend() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParsePlanTasks_FencedHeadingsDoNotTruncate(t *testing.T) {
	plan := "# Plan\n" +
		"\n" +
		"## Tasks\n" +
		"\n" +
		"### Task 1: Rename agentic README\n" +
		"\n" +
		"**Repo:** `agentic`\n" +
		"\n" +
		"### Task 2: Replace agentic README with translation\n" +
		"\n" +
		"**Repo:** `agentic`\n" +
		"\n" +
		"````markdown\n" +
		"# Agentic\n" +
		"\n" +
		"## Picchì Agentic?\n" +
		"\n" +
		"Some translated content.\n" +
		"\n" +
		"## Guida viloci\n" +
		"\n" +
		"More translated content.\n" +
		"````\n" +
		"\n" +
		"### Task 3: Rename payments README\n" +
		"\n" +
		"**Repo:** `payments`\n" +
		"\n" +
		"### Task 4: Replace payments README with translation\n" +
		"\n" +
		"**Repo:** `payments`\n" +
		"\n" +
		"````markdown\n" +
		"# Payments\n" +
		"\n" +
		"## Architettura\n" +
		"````\n" +
		"\n" +
		"## Success Criteria\n" +
		"\n" +
		"- [ ] PRs open\n"
	tasks := ParsePlanTasks(plan)
	if len(tasks) != 4 {
		t.Fatalf("expected 4 tasks (fenced headings should not truncate), got %d", len(tasks))
	}
	got := []string{tasks[0].Repo, tasks[1].Repo, tasks[2].Repo, tasks[3].Repo}
	want := []string{"agentic", "agentic", "payments", "payments"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("repos = %v, want %v", got, want)
	}
	repos := PlanTaskRepos(plan)
	if !reflect.DeepEqual(repos, []string{"agentic", "payments"}) {
		t.Errorf("PlanTaskRepos = %v, want [agentic payments]", repos)
	}
}

func TestParsePlanTasks_FencedTaskHeadingIgnored(t *testing.T) {
	// A `### Task N` line inside a fence is illustrative content, not a
	// real task heading.
	plan := "## Tasks\n" +
		"\n" +
		"### Task 1: Real task\n" +
		"\n" +
		"**Repo:** `api`\n" +
		"\n" +
		"```\n" +
		"### Task 99: Fake task in code block\n" +
		"**Repo:** `evil`\n" +
		"```\n" +
		"\n" +
		"### Task 2: Another real task\n" +
		"\n" +
		"**Repo:** `web`\n"
	tasks := ParsePlanTasks(plan)
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(tasks))
	}
	if tasks[0].Repo != testRepoNameAPI || tasks[1].Repo != testRepoNameWeb {
		t.Errorf("repos = [%q %q], want [api web]", tasks[0].Repo, tasks[1].Repo)
	}
}

func TestParsePlanTasks_TildeFenceTracked(t *testing.T) {
	plan := "## Tasks\n" +
		"\n" +
		"### Task 1: Real\n" +
		"\n" +
		"**Repo:** `api`\n" +
		"\n" +
		"~~~\n" +
		"## Not A Section Break\n" +
		"~~~\n" +
		"\n" +
		"### Task 2: Real\n" +
		"\n" +
		"**Repo:** `web`\n"
	tasks := ParsePlanTasks(plan)
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks (tilde fence), got %d", len(tasks))
	}
}

func TestParsePlanTasks_BoundsOnNextSection(t *testing.T) {
	plan := `# P
## Tasks

### Task 1: One

body line
` + "```go" + `
code block
` + "```" + `

## Success Criteria

### Automated
- [ ] tests
`
	tasks := ParsePlanTasks(plan)
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	body := strings.Join(tasks[0].Body, "\n")
	if strings.Contains(body, "Success Criteria") || strings.Contains(body, "Automated") {
		t.Errorf("task body leaked into next section: %q", body)
	}
	if !strings.Contains(body, "code block") {
		t.Errorf("expected fenced code block in body, got: %q", body)
	}
}
