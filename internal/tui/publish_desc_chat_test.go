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

package tui

import (
	"strings"
	"testing"
)

func TestBuildDescriptionChatSystemPrompt(t *testing.T) {
	ctx := DescriptionChatContext{
		FeatureID:    "feat-123",
		RepoName:     "test-repo",
		CurrentTitle: "Add user authentication",
		CurrentBody:  "This PR adds OAuth2 support.",
		DiffSummary:  "10 files changed, +200 -50",
	}

	prompt := buildDescriptionChatSystemPrompt(ctx)

	if !strings.Contains(prompt, ctx.FeatureID) {
		t.Errorf("prompt should contain feature ID %q", ctx.FeatureID)
	}
	if !strings.Contains(prompt, ctx.RepoName) {
		t.Errorf("prompt should contain repo name %q", ctx.RepoName)
	}
	if !strings.Contains(prompt, ctx.CurrentTitle) {
		t.Errorf("prompt should contain current title %q", ctx.CurrentTitle)
	}
	if !strings.Contains(prompt, ctx.CurrentBody) {
		t.Errorf("prompt should contain current body %q", ctx.CurrentBody)
	}
	if !strings.Contains(prompt, ctx.DiffSummary) {
		t.Errorf("prompt should contain diff summary %q", ctx.DiffSummary)
	}
	if !strings.Contains(prompt, "UpdatePRDescription") {
		t.Error("prompt should mention UpdatePRDescription tool")
	}
}
