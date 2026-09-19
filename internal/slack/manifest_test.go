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

package slack

import (
	"encoding/json"
	"testing"
)

func TestManifestPinsRequiredScopesAndInactiveFeatures(t *testing.T) {
	var manifest struct {
		DisplayInformation struct {
			Name string `json:"name"`
		} `json:"display_information"`
		Features struct {
			BotUser struct {
				DisplayName string `json:"display_name"`
			} `json:"bot_user"`
		} `json:"features"`
		OAuthConfig struct {
			Scopes struct {
				Bot  []string `json:"bot"`
				User []string `json:"user"`
			} `json:"scopes"`
		} `json:"oauth_config"`
		Settings struct {
			Interactivity struct {
				Enabled bool `json:"is_enabled"`
			} `json:"interactivity"`
			EventSubscriptions json.RawMessage `json:"event_subscriptions"`
			SocketMode         bool            `json:"socket_mode_enabled"`
		} `json:"settings"`
	}
	if err := json.Unmarshal([]byte(Manifest()), &manifest); err != nil {
		t.Fatalf("json.Unmarshal(Manifest()) error = %v", err)
	}
	if manifest.DisplayInformation.Name != "Agentico" ||
		manifest.Features.BotUser.DisplayName != "Agentico" {
		t.Fatalf("manifest names = %q/%q; want Agentico", manifest.DisplayInformation.Name, manifest.Features.BotUser.DisplayName)
	}
	if len(manifest.OAuthConfig.Scopes.Bot) != 13 {
		t.Fatalf("manifest bot scopes = %d; want 13", len(manifest.OAuthConfig.Scopes.Bot))
	}
	if !sameStrings(manifest.OAuthConfig.Scopes.Bot, manifest.OAuthConfig.Scopes.User) {
		t.Fatalf("manifest bot/user scopes differ: %#v / %#v", manifest.OAuthConfig.Scopes.Bot, manifest.OAuthConfig.Scopes.User)
	}
	if !sameStrings(manifest.OAuthConfig.Scopes.Bot, RequiredScopes()) {
		t.Fatalf("RequiredScopes() = %#v; want manifest scopes %#v", RequiredScopes(), manifest.OAuthConfig.Scopes.Bot)
	}
	if manifest.Settings.Interactivity.Enabled || manifest.Settings.SocketMode ||
		len(manifest.Settings.EventSubscriptions) != 0 {
		t.Fatalf("manifest enables interactive/event delivery: %#v", manifest.Settings)
	}
}

func TestTokenMetadata(t *testing.T) {
	cases := []struct {
		token    string
		wantType string
		wantHint string
	}{
		{"xoxb-secret-1234", "bot", "1234"},
		{"xoxp-secret-abcd", "user", "abcd"},
		{"xoxa-secret-zzzz", "unsupported", "zzzz"},
		{"", "unsupported", ""},
		{"abc", "unsupported", "abc"},
	}
	for _, tc := range cases {
		t.Run(tc.token, func(t *testing.T) {
			if got := string(TokenTypeOf(tc.token)); got != tc.wantType {
				t.Errorf("TokenTypeOf(%q) = %q; want %q", tc.token, got, tc.wantType)
			}
			if got := TokenHint(tc.token); got != tc.wantHint {
				t.Errorf("TokenHint(%q) = %q; want %q", tc.token, got, tc.wantHint)
			}
		})
	}
}
