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
