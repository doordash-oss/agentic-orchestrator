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

package feature

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"gopkg.in/yaml.v3"
)

func TestSlackPerFeatureEffectiveMatrix(t *testing.T) {
	global := SlackGlobalSettings{
		Enabled: true, HasToken: true,
		Recipients: []SlackRecipient{
			{TypedText: "@first", Kind: "user", ID: "U123", DisplayName: "First"},
			{TypedText: "#ops", Kind: "channel", ID: "C123", DisplayName: "Ops"},
		},
		Progress: true, NeedsInput: true, Problems: false,
	}
	section := &SlackNotifications{
		Mode:       SlackMuted,
		Progress:   SlackOff,
		NeedsInput: SlackOn,
		Problems:   SlackOn,
		Recipients: []SlackRecipient{
			{TypedText: "first@example.com", Kind: "user", ID: "U123", DisplayName: "Duplicate"},
			{TypedText: "#extra", Kind: "channel", ID: "C456", DisplayName: "Extra"},
			{TypedText: "C456", Kind: "channel", ID: "C456", DisplayName: "Duplicate extra"},
		},
	}
	got := ResolveSlack(global, section)
	if !got.Configured || !got.Muted || got.ModeSource != SlackSourceFeature {
		t.Errorf("ResolveSlack mode = %+v; want configured muted feature", got)
	}
	if diff := cmp.Diff([]SlackEffectiveRecipient{
		{SlackRecipient: global.Recipients[0], Source: SlackSourceGlobal},
		{SlackRecipient: global.Recipients[1], Source: SlackSourceGlobal},
		{SlackRecipient: section.Recipients[1], Source: SlackSourceFeature},
	}, got.Recipients); diff != "" {
		t.Errorf("ResolveSlack recipients (-want +got):\n%s", diff)
	}
	if got.Progress != (SlackEffectiveCategory{Enabled: false, Source: SlackSourceFeature}) ||
		got.NeedsInput != (SlackEffectiveCategory{Enabled: true, Source: SlackSourceFeature}) ||
		got.Problems != (SlackEffectiveCategory{Enabled: true, Source: SlackSourceFeature}) {
		t.Errorf("ResolveSlack categories = %+v; want feature overrides", got)
	}
	inherit := ResolveSlack(global, nil)
	if inherit.Muted || inherit.ModeSource != SlackSourceGlobal ||
		inherit.Progress != (SlackEffectiveCategory{Enabled: true, Source: SlackSourceGlobal}) ||
		inherit.Problems != (SlackEffectiveCategory{Enabled: false, Source: SlackSourceGlobal}) {
		t.Errorf("ResolveSlack inherit = %+v", inherit)
	}
	for _, disabled := range []SlackGlobalSettings{
		{Enabled: false, HasToken: true, Recipients: global.Recipients},
		{Enabled: true, HasToken: false, Recipients: global.Recipients},
	} {
		if effective := ResolveSlack(disabled, section); effective.Configured || len(effective.Recipients) != 0 {
			t.Errorf("ResolveSlack(%+v) = %+v; want no effective recipients", disabled, effective)
		}
	}
	if diff := cmp.Diff(got, ResolveSlack(global, section)); diff != "" {
		t.Errorf("ResolveSlack is not deterministic (-first +second):\n%s", diff)
	}
}

func TestSlackPerFeaturePersistenceAndValidation(t *testing.T) {
	for _, invalid := range []string{"mode: unknown", "progress: maybe", "needs_input: maybe", "problems: maybe"} {
		var section SlackNotifications
		if err := yaml.Unmarshal([]byte(invalid), &section); err == nil {
			t.Errorf("Unmarshal(%q) succeeded; want error", invalid)
		}
	}
	section := SlackNotifications{Mode: SlackMuted, Progress: SlackOff, NeedsInput: SlackOn}
	data, err := yaml.Marshal(section)
	if err != nil {
		t.Fatal(err)
	}
	var loaded SlackNotifications
	if err := yaml.Unmarshal(data, &loaded); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(section, loaded); diff != "" {
		t.Errorf("SlackNotifications round-trip (-want +got):\n%s", diff)
	}
	for _, field := range []string{"mode: muted", "progress: \"off\"", "needs_input: \"on\""} {
		if !strings.Contains(string(data), field) {
			t.Errorf("YAML %q missing %q", data, field)
		}
	}
}

func TestSlackPerFeatureStoreRevisionOnlyTracksNotificationChanges(t *testing.T) {
	store := NewStore(t.TempDir())
	f := &Feature{ID: "F-1", Name: "Revision test", SchemaVersion: SchemaVersionCurrent}
	if err := store.Save(f); err != nil {
		t.Fatal(err)
	}
	initial := store.SlackNotificationsRevision()
	if err := store.Modify(f.ID, func(f *Feature) error {
		f.Name = "Renamed"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := store.SlackNotificationsRevision(); got != initial {
		t.Errorf("unrelated edit revision = %d; want %d", got, initial)
	}
	if err := store.Modify(f.ID, func(f *Feature) error {
		f.SlackNotifications = &SlackNotifications{Mode: SlackMuted}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := store.SlackNotificationsRevision(); got != initial+1 {
		t.Errorf("Slack edit revision = %d; want %d", got, initial+1)
	}
}
