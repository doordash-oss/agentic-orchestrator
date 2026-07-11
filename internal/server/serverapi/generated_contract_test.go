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

package serverapi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGeneratedDTOResponsesExposeConcreteFields(t *testing.T) {
	health := HealthResponse{}
	health.Status = "ok"
	health.Runtime.RuntimeDir = "/tmp/runtime"
	health.Owner.PID = 123

	runtime := RuntimeConfigResponse{}
	runtime.Runtime.StateDir = "/tmp/state"
	runtime.FeatureDefaults.Pipeline = "research"
	runtime.Notifications.MuteFeatureInput = true

	featureConfig := FeatureConfigResponse{}
	featureConfig.Current.Inquireness = "low"
	featureConfig.Publish.Repos = map[string]bool{"repo": true}

	recovery := RecoverySnapshotResponse{}
	recovery.SnapshotID = "snapshot"
	recovery.Items = []RecoveryItem{{Key: "item"}}

	workspace := WorkspaceBrowseResponse{}
	workspace.Entries = []WorkspaceBrowseEntry{{Name: "repo"}}

	models := ModelCatalogResponse{}
	models.ProviderModels = map[string][]Model{"codex": {{ID: "gpt-5"}}}

	prompts := PromptSnapshotResponse{}
	prompts.HelpQueue = []HelpQueue{{Question: "question"}}

	permissions := PermissionSnapshotResponse{}
	permissions.Requests = []ControlRequest{{RequestID: "req"}}

	artifacts := ArtifactListResponse{}
	artifacts.Artifacts = []Artifact{{ID: "artifact"}}

	text := TextContentResponse{}
	text.Text = "content"
	text.Offset = 1

	live := LivePreviewResponse{}
	live.Activity = "running"
	live.Context.Percentage = 10

	transcript := TranscriptResponse{}
	transcript.Messages = []TranscriptMessage{{Index: 1}}
}

func TestGeneratedPermissionAnswerRequestPreservesEmptyRememberScope(t *testing.T) {
	scope := ""
	body, err := json.Marshal(PermissionAnswerRequest{
		RequestID:       "perm-1",
		Decision:        AllowRemember,
		RememberPattern: "Bash(go test *)",
		RememberScope:   &scope,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if !strings.Contains(string(body), `"remember_scope":""`) {
		t.Fatalf("body = %s, want explicit empty remember_scope", body)
	}
}
