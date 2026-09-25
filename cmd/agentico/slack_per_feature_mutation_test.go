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

package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	serverruntime "github.com/doordash-oss/agentic-orchestrator/internal/server"
)

func TestSlackPerFeatureCreationAndPartialConfigMutation(t *testing.T) {
	dir := t.TempDir()
	cfg := config.NewDefault() // Slack disabled and token absent.
	repoPath := filepath.Join(dir, testRepoAName)
	initMutationGitRepo(t, repoPath)
	cfg.Repos[testRepoAName] = config.RepoConfig{Path: repoPath}
	configPath := filepath.Join(dir, "config.yaml")
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	store := feature.NewStore(filepath.Join(dir, "features"))
	target := newRESTCreateFeatureTarget(store, feature.NewManager(store, cfg), cfg, configPath)
	muted, off := "muted", "off"
	recipients := []serverruntime.SlackRecipient{
		{TypedText: "#eng", Kind: "channel", ID: "C1", DisplayName: "Engineering"},
	}
	created, err := target.CreateFeature(serverruntime.CreateFeatureRequest{
		Name: "with notifications", Repos: []string{testRepoAName},
		SlackNotifications: &serverruntime.SlackNotificationsPatch{
			Mode: &muted, Progress: &off, Recipients: &recipients,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.Load(created.FeatureID)
	if err != nil {
		t.Fatal(err)
	}
	if current.SlackNotifications == nil || current.SlackNotifications.Mode != feature.SlackMuted ||
		current.SlackNotifications.Progress != feature.SlackOff ||
		len(current.SlackNotifications.Recipients) != 1 {
		t.Fatalf("created section = %+v", current.SlackNotifications)
	}
	base := serverruntime.FeatureConfigMutationRequest{
		Models: current.Models, Effort: current.Effort, Inquireness: string(current.Inquireness),
		Checkpoints: current.Checkpoints, Pipeline: current.Pipeline,
	}
	update := func(req serverruntime.FeatureConfigMutationRequest) *feature.Feature {
		t.Helper()
		if _, err := target.UpdateFeatureConfig(created.FeatureID, req); err != nil {
			t.Fatal(err)
		}
		f, err := store.Load(created.FeatureID)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	if got := update(base).SlackNotifications; got == nil || got.Mode != feature.SlackMuted {
		t.Fatalf("omitted section changed: %+v", got)
	}
	base.SlackNotifications = &serverruntime.SlackNotificationsPatch{Problems: &off}
	if got := update(base).SlackNotifications; got.Mode != feature.SlackMuted ||
		got.Progress != feature.SlackOff || got.Problems != feature.SlackOff ||
		len(got.Recipients) != 1 {
		t.Fatalf("partial update changed another field: %+v", got)
	}
	inherit := "inherit"
	empty := []serverruntime.SlackRecipient{}
	base.SlackNotifications = &serverruntime.SlackNotificationsPatch{
		Mode: &inherit, Progress: &inherit, Recipients: &empty,
	}
	if got := update(base).SlackNotifications; got.Mode != feature.SlackInherit ||
		got.Progress != feature.SlackDefault || got.Problems != feature.SlackOff ||
		len(got.Recipients) != 0 {
		t.Fatalf("explicit clear = %+v", got)
	}
	other, err := target.CreateFeature(serverruntime.CreateFeatureRequest{
		Name: "without notifications", Repos: []string{testRepoAName},
	})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := store.Load(other.FeatureID)
	if err != nil {
		t.Fatal(err)
	}
	if plain.SlackNotifications != nil {
		t.Fatalf("omitted creation section = %+v", plain.SlackNotifications)
	}
}

func TestSlackPerFeatureRedactionPersistedAndProjected(t *testing.T) {
	const token = "xoxb-per-feature-token-sentinel-123456789"
	const credential = "https://alice:secret-value@example.com/path"
	dir := t.TempDir()
	cfg := config.NewDefault()
	cfg.Slack = &config.SlackConfig{Enabled: true, Token: token,
		DefaultRecipients: []config.SlackRecipient{{TypedText: "@ops", Kind: "user", ID: "U1", DisplayName: "Ops"}}}
	repoPath := filepath.Join(dir, testRepoAName)
	initMutationGitRepo(t, repoPath)
	cfg.Repos[testRepoAName] = config.RepoConfig{Path: repoPath}
	configPath := filepath.Join(dir, "config.yaml")
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	store := feature.NewStore(filepath.Join(dir, "features"))
	target := newRESTCreateFeatureTarget(store, feature.NewManager(store, cfg), cfg, configPath)
	recipients := []serverruntime.SlackRecipient{{
		TypedText: "#eng " + token + " " + credential,
		Kind:      "channel", ID: "C1",
		DisplayName: "Engineering " + token + " " + credential,
	}}
	created, err := target.CreateFeature(serverruntime.CreateFeatureRequest{
		Name: "redaction test", Repos: []string{testRepoAName},
		SlackNotifications: &serverruntime.SlackNotificationsPatch{Recipients: &recipients},
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(filepath.Join(store.BaseDir, created.FeatureID, "feature.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), token) || strings.Contains(string(stored), credential) ||
		!strings.Contains(string(stored), "Engineering") {
		t.Fatalf("persisted recipient text was not redacted: %s", stored)
	}
	handler := serverruntime.NewHandler(serverruntime.HandlerOptions{
		Features: store, Config: cfg, DisableHostValidation: true,
	})
	for _, path := range []string{
		"/api/v1/features/" + created.FeatureID,
		"/api/v1/features/" + created.FeatureID + "/config",
		"/api/v1/config/runtime",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK || strings.Contains(response.Body.String(), token) ||
			strings.Contains(response.Body.String(), credential) {
			t.Fatalf("%s: status=%d response=%s", path, response.Code, response.Body.String())
		}
		if path != "/api/v1/config/runtime" && !strings.Contains(response.Body.String(), "Engineering") {
			t.Fatalf("%s omitted harmless display text: %s", path, response.Body.String())
		}
	}
}

func TestSlackPerFeatureChildConfigEditUpdatesOwnerAndChild(t *testing.T) {
	dir := t.TempDir()
	cfg := config.NewDefault()
	configPath := filepath.Join(dir, "config.yaml")
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	store := feature.NewStore(filepath.Join(dir, "features"))
	manager := feature.NewManager(store, cfg)
	parent, err := manager.Create("parent notifications", "", nil, cfg.Defaults.Models, "", "", nil,
		feature.CreateOptions{
			SlackNotifications: &feature.SlackNotifications{
				Mode:       feature.SlackMuted,
				Recipients: []feature.SlackRecipient{{TypedText: "@ops", Kind: "user", ID: "U1", DisplayName: "Ops"}},
			},
		})
	if err != nil {
		t.Fatal(err)
	}
	child := &feature.Feature{
		ID: parent.ID + "-child", Name: "Child", Slug: parent.Slug + "-child",
		Status: feature.StatusCreated, Pipeline: feature.PipelineMedium,
		ActiveRun: 1, RunCount: 1, SchemaVersion: feature.SchemaVersionCurrent,
		Parent: &feature.ChildRelationship{ParentID: parent.ID, Kind: feature.ChildKindRefactor},
	}
	child.SetRun(&feature.Run{RunNumber: 1})
	if err := store.Save(child); err != nil {
		t.Fatal(err)
	}
	target := newRESTCreateFeatureTarget(store, manager, cfg, configPath)
	off := "off"
	_, err = target.UpdateFeatureConfig(child.ID, serverruntime.FeatureConfigMutationRequest{
		Models: child.Models, Effort: child.Effort, Pipeline: child.Pipeline,
		Inquireness: string(child.Inquireness), Checkpoints: child.Checkpoints,
		SlackNotifications: &serverruntime.SlackNotificationsPatch{Progress: &off},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{parent.ID, child.ID} {
		got, err := store.Load(id)
		if err != nil {
			t.Fatal(err)
		}
		section := got.SlackNotifications
		if section == nil || section.Mode != feature.SlackMuted || section.Progress != feature.SlackOff ||
			len(section.Recipients) != 1 || section.Recipients[0].ID != "U1" {
			t.Fatalf("%s section = %+v", id, section)
		}
	}
}
