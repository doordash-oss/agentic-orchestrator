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

package prompts

import (
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

func requireContains(t testing.TB, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Errorf("rendered prompt missing %q\n--- GOT ---\n%s", want, got)
	}
}

func requireNotContains(t testing.TB, got, unwanted string) {
	t.Helper()
	if strings.Contains(got, unwanted) {
		t.Errorf("rendered prompt contains %q\n--- GOT ---\n%s", unwanted, got)
	}
}

func requireOrder(t testing.TB, got string, markers ...string) {
	t.Helper()
	last := -1
	for _, marker := range markers {
		idx := strings.Index(got, marker)
		if idx < 0 {
			t.Errorf("rendered prompt missing ordered marker %q\n--- GOT ---\n%s", marker, got)
			continue
		}
		if idx <= last {
			t.Errorf("marker %q index = %d, want after %d\n--- GOT ---\n%s", marker, idx, last, got)
		}
		last = idx
	}
}

func TestPreflightBranchBehavior(t *testing.T) {
	render := func(in PreflightInput) string {
		return RoleSystemPrompt(RoleSystemInput{
			OutputRoots: []OutputRootView{{Name: "phase_dir", Path: "/phase"}},
			MarkerPath:  "/phase/phase_complete",
			SkillPath:   "/skills/role/SKILL.md",
			Preflight:   in,
		})
	}

	tests := []struct {
		name         string
		input        PreflightInput
		wantContains []string
		wantOmit     []string
	}{
		{
			name: "kb_section_emitted_when_has_kb",
			input: PreflightInput{
				HasKB: true,
				KBInfos: []KBView{
					{Name: "web", IndexPath: "/kb/web/index.md", RootDir: "/kb/web"},
				},
			},
			wantContains: []string{"## Knowledge Base", "/kb/web/index.md"},
			wantOmit:     []string{"## Guidelines", "## Additional Skills"},
		},
		{
			name: "guidelines_section_emitted_when_has_guidelines",
			input: PreflightInput{
				HasGuidelines: true,
				Guidelines:    []GuidelineView{{Language: "Go", IndexPath: "/guidelines/go/index.md"}},
			},
			wantContains: []string{"## Guidelines", "/guidelines/go/index.md"},
			wantOmit:     []string{"## Knowledge Base", "## Additional Skills"},
		},
		{
			name: "skills_section_emitted_when_has_skills",
			input: PreflightInput{
				HasSkills: true,
				Skills:    []SkillView{{Name: "skill", Description: "desc", Topics: "topic", Path: "/skills/skill/SKILL.md"}},
			},
			wantContains: []string{"## Additional Skills", "/skills/skill/SKILL.md"},
			wantOmit:     []string{"## Knowledge Base", "## Guidelines"},
		},
		{
			name: "skills_section_omitted_when_has_skills_false",
			input: PreflightInput{
				HasSkills: false,
				Skills:    []SkillView{{Name: "skill", Description: "desc", Topics: "topic", Path: "/skills/skill/SKILL.md"}},
			},
			wantOmit: []string{"## Additional Skills", "/skills/skill/SKILL.md"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := render(tt.input)
			for _, want := range tt.wantContains {
				requireContains(t, got, want)
			}
			for _, unwanted := range tt.wantOmit {
				requireNotContains(t, got, unwanted)
			}
		})
	}
}

func TestMultiRepoPromptBranches(t *testing.T) {
	repos := []RepoView{{Name: "web", Path: "/repos/web"}, {Name: "api", Path: "/repos/api"}}
	tests := []struct {
		name   string
		render func() string
		want   bool
	}{
		{
			name: "brainstorm_target_repositories_block_emitted_when_multi_repo_with_multiple_repos",
			render: func() string {
				return BrainstormUserPrompt(BrainstormUserInput{Name: "Name", Description: "Desc", MultiRepo: true, Repos: repos})
			},
			want: true,
		},
		{
			name: "brainstorm_target_repositories_block_omitted_when_multi_repo_flag_false",
			render: func() string {
				return BrainstormUserPrompt(BrainstormUserInput{Name: "Name", Description: "Desc", MultiRepo: false, Repos: repos})
			},
		},
		{
			name: "brainstorm_target_repositories_block_omitted_when_only_one_repo",
			render: func() string {
				return BrainstormUserPrompt(BrainstormUserInput{Name: "Name", Description: "Desc", MultiRepo: true, Repos: repos[:1]})
			},
		},
		{
			name: "roadmap_target_repositories_block_emitted_when_multi_repo_with_multiple_repos",
			render: func() string {
				return RoadmapUserPrompt(RoadmapUserInput{Name: "Name", Description: "Desc", MultiRepo: true, Repos: repos})
			},
			want: true,
		},
		{
			name: "roadmap_target_repositories_block_omitted_when_multi_repo_flag_false",
			render: func() string {
				return RoadmapUserPrompt(RoadmapUserInput{Name: "Name", Description: "Desc", MultiRepo: false, Repos: repos})
			},
		},
		{
			name: "roadmap_target_repositories_block_omitted_when_only_one_repo",
			render: func() string {
				return RoadmapUserPrompt(RoadmapUserInput{Name: "Name", Description: "Desc", MultiRepo: true, Repos: repos[:1]})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.render()
			if tt.want {
				requireContains(t, got, "## Target Repositories")
				requireContains(t, got, "**api**")
				return
			}
			requireNotContains(t, got, "## Target Repositories")
		})
	}
}

func TestInquirenessBranches(t *testing.T) {
	grillMeNone := GrillMeInquireness("none")
	grillMeMedium := GrillMeInquireness("medium")
	grillMeHigh := GrillMeInquireness("high")
	if grillMeNone != grillMeMedium || grillMeMedium != grillMeHigh {
		t.Fatalf("grill-me directive should be invariant by inquireness level:\n--- none ---\n%s\n--- medium ---\n%s\n--- high ---\n%s", grillMeNone, grillMeMedium, grillMeHigh)
	}

	tests := []struct {
		name         string
		got          string
		wantContains []string
		wantOmit     []string
	}{
		{
			name:         "grillme_none_uses_policy_free_directive",
			got:          grillMeNone,
			wantContains: []string{"Ambiguity Resolution [grill-me]", "Ask the questions one at a time"},
			wantOmit:     []string{"strictly greater than", "auto-pick", "auto-resolve", "silent", "qa-answers.md", "threshold"},
		},
		{
			name:         "grillme_medium_uses_policy_free_directive",
			got:          grillMeMedium,
			wantContains: []string{"Ambiguity Resolution [grill-me]", "Ask the questions one at a time"},
			wantOmit:     []string{"strictly greater than", "auto-pick", "auto-resolve", "silent", "qa-answers.md", "threshold"},
		},
		{
			name:         "grillme_high_uses_policy_free_directive",
			got:          grillMeHigh,
			wantContains: []string{"Ambiguity Resolution [grill-me]", "Ask the questions one at a time"},
			wantOmit:     []string{"strictly greater than", "auto-pick", "auto-resolve", "silent", "qa-answers.md", "threshold"},
		},
		{
			name:         "autonomous_inquireness_forbids_questions",
			got:          AutonomousInquireness(),
			wantContains: []string{"Ambiguity Resolution [none]", "Do NOT ask the user any questions"},
			wantOmit:     []string{"Ambiguity Resolution [grill-me]"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, want := range tt.wantContains {
				requireContains(t, tt.got, want)
			}
			for _, unwanted := range tt.wantOmit {
				requireNotContains(t, tt.got, unwanted)
			}
		})
	}
}

func TestKBBuildModeBranches(t *testing.T) {
	base := KBBuildUserInput{
		RepoName:    "repo",
		RepoPath:    "/repos/repo",
		KBRootDir:   "/kb/repo",
		KBIndexPath: "/kb/repo/index.md",
	}
	tests := []struct {
		name         string
		input        KBBuildUserInput
		wantContains []string
		wantOmit     []string
	}{
		{
			name:         "kb_build_full_mode_when_existing_kb_and_last_commit_empty",
			input:        base,
			wantContains: []string{"## Mode: FULL BUILD"},
			wantOmit:     []string{"## Mode: INCREMENTAL UPDATE", "Last built at commit"},
		},
		{
			name: "kb_build_incremental_mode_when_existing_kb_and_last_commit_set",
			input: KBBuildUserInput{
				RepoName:       base.RepoName,
				RepoPath:       base.RepoPath,
				KBRootDir:      base.KBRootDir,
				KBIndexPath:    base.KBIndexPath,
				ExistingKBPath: "/kb/repo/index.md",
				LastCommit:     "abc1234",
			},
			wantContains: []string{"## Mode: INCREMENTAL UPDATE", "Existing KB index: /kb/repo/index.md", "Last built at commit: `abc1234`"},
			wantOmit:     []string{"## Mode: FULL BUILD"},
		},
		{
			name: "kb_build_full_mode_when_only_existing_kb_set",
			input: KBBuildUserInput{
				RepoName:       base.RepoName,
				RepoPath:       base.RepoPath,
				KBRootDir:      base.KBRootDir,
				KBIndexPath:    base.KBIndexPath,
				ExistingKBPath: "/kb/repo/index.md",
			},
			wantContains: []string{"## Mode: FULL BUILD"},
			wantOmit:     []string{"## Mode: INCREMENTAL UPDATE"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := KBBuildUserPrompt(tt.input)
			for _, want := range tt.wantContains {
				requireContains(t, got, want)
			}
			for _, unwanted := range tt.wantOmit {
				requireNotContains(t, got, unwanted)
			}
		})
	}
}

func TestPromptBranchFixturesUseRegisteredImplementSkills(t *testing.T) {
	got := RoleSystemPrompt(RoleSystemInput{
		OutputRoots: []OutputRootView{{Name: "phase_dir", Path: "/phase"}},
		MarkerPath:  "/phase/phase_complete",
		SkillPath:   "/skills/implement/SKILL.md",
		Preflight: PreflightInput{
			HasSkills: true,
			Skills:    testSkills(feature.PhaseImplement),
		},
	})
	if count := strings.Count(got, "| "); count < 4 {
		t.Errorf("Preflight(implement skills) table cell markers = %d, want at least 4\n--- GOT ---\n%s", count, got)
	}
	requireContains(t, got, "frontend-design")
	requireContains(t, got, "knowledge-reader")
	requireContains(t, got, "guideline-reader")
}
