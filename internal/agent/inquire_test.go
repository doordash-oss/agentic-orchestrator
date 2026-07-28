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

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

func TestBuildInquirePrompt(t *testing.T) {
	f := &feature.Feature{
		Name:        "Test Feature",
		Description: "Implement user authentication",
		Repos: []feature.FeatureRepo{
			{Name: "myrepo", Path: "/tmp/myrepo"},
		},
		Inquireness: feature.InquirenessHigh,
	}

	prompt := BuildInquirePrompt(f, "")

	checks := []string{
		"# Feature Context",
		"## Feature Request",
		"**Name**: Test Feature",
		"> Implement user authentication",
		"myrepo",
		"Ambiguity Resolution",
	}

	for _, c := range checks {
		if !strings.Contains(prompt, c) {
			t.Errorf("expected prompt to contain %q", c)
		}
	}
}

func TestBuildInquirePromptWithImages(t *testing.T) {
	f := &feature.Feature{
		Name:        "Image Feature",
		Description: "A feature with images",
		Images:      []string{"/tmp/images/image-1.png"},
		Repos: []feature.FeatureRepo{
			{Name: "myrepo", Path: "/tmp/myrepo"},
		},
		Inquireness: feature.InquirenessMedium,
	}
	prompt := BuildInquirePrompt(f, "")
	if !strings.Contains(prompt, "Attached Images:") {
		t.Error("expected 'Attached Images' section")
	}
}

func TestBuildInquirePromptUsesDescription(t *testing.T) {
	f := &feature.Feature{
		Name:        "Inquire Feature",
		Description: "original desc",
		Repos: []feature.FeatureRepo{
			{Name: "repo", Path: "/tmp/test"},
		},
	}
	prompt := BuildInquirePrompt(f, "")
	if !strings.Contains(prompt, "original desc") {
		t.Error("expected original description in inquire output")
	}
}

func TestBuildInquirePromptLeavesKBResourcesToSystemPrompt(t *testing.T) {
	f := &feature.Feature{
		Name:        "KB Feature",
		Description: "Test",
		Inquireness: feature.InquirenessMedium,
	}
	kb := KBInfo{Name: "myrepo", IndexPath: "/tmp/kb/index.md", RootDir: "/tmp/kb"}
	prompt := BuildInquirePrompt(f, "", kb)
	for _, forbidden := range []string{"# Useful Resources", "## Knowledge Base", "/tmp/kb/index.md", "/tmp/kb"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("BuildInquirePrompt() contains RoleSpec-owned resource %q:\n%s", forbidden, prompt)
		}
	}

	systemPrompt := BuildRoleSystemPrompt(BuildRoleSystemPromptInput{
		Spec:         InquirerRoleSpec(),
		IterationDir: "/tmp/feature/runs/run-001/inquire",
		SkillsDir:    "/tmp/skills",
		KBInfos:      []KBInfo{kb},
	})
	for _, want := range []string{"# Useful Resources", "## Knowledge Base", "**myrepo**", "/tmp/kb/index.md", "/tmp/kb"} {
		if !strings.Contains(systemPrompt, want) {
			t.Fatalf("BuildRoleSystemPrompt() missing %q:\n%s", want, systemPrompt)
		}
	}
}
