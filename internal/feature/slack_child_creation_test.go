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

package feature_test

import (
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

func TestSlackPerFeatureChildCreationDoesNotCopySection(t *testing.T) {
	mgr := newChildTestManager(t, map[string]string{"/wt/repo": "aaaa"}, cleanEverywhere())
	parent := &feature.Feature{
		ID: "parent-slack", Slug: "parent-slack", Status: feature.StatusPublished,
		Pipeline: feature.PipelineMoonshot,
		Repos: []feature.FeatureRepo{{
			Name: "repo", Path: "/src/repo", WorktreePath: "/wt/repo", Branch: "main", BaseBranch: "main",
		}},
		SlackNotifications: &feature.SlackNotifications{
			Mode:       feature.SlackMuted,
			Recipients: []feature.SlackRecipient{{TypedText: "#eng", Kind: "channel", ID: "C1", DisplayName: "Engineering"}},
		},
	}
	saveChildTestParent(t, mgr, parent)
	child, err := mgr.CreateRefactorChild(parent.ID, feature.RefactorChildSpec{
		Name: "Child", Pipeline: feature.PipelineMedium,
	})
	if err != nil {
		t.Fatal(err)
	}
	persistedChild, err := mgr.Store.Load(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if child.SlackNotifications != nil || persistedChild.SlackNotifications != nil {
		t.Fatalf("child copied notifications: in memory %+v, persisted %+v", child.SlackNotifications, persistedChild.SlackNotifications)
	}
	persistedParent, err := mgr.Store.Load(parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persistedParent.SlackNotifications == nil || persistedParent.SlackNotifications.Mode != feature.SlackMuted ||
		len(persistedParent.SlackNotifications.Recipients) != 1 {
		t.Fatalf("parent notifications changed during child creation: %+v", persistedParent.SlackNotifications)
	}
}
