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
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed manifest.json
var manifestText string

type appManifest struct {
	OAuthConfig struct {
		Scopes struct {
			Bot  []string `json:"bot"`
			User []string `json:"user"`
		} `json:"scopes"`
	} `json:"oauth_config"`
}

var requiredScopes = parseRequiredScopes()

// Manifest returns the embedded Slack app manifest.
func Manifest() string {
	return manifestText
}

// RequiredScopes returns a copy of the manifest's required bot scopes.
func RequiredScopes() []string {
	return append([]string(nil), requiredScopes...)
}

func parseRequiredScopes() []string {
	var manifest appManifest
	if err := json.Unmarshal([]byte(manifestText), &manifest); err != nil {
		panic(fmt.Sprintf("parsing embedded Slack manifest: %v", err))
	}
	if len(manifest.OAuthConfig.Scopes.Bot) == 0 {
		panic("embedded Slack manifest has no bot scopes")
	}
	if !sameStrings(manifest.OAuthConfig.Scopes.Bot, manifest.OAuthConfig.Scopes.User) {
		panic("embedded Slack manifest bot and user scopes differ")
	}
	return append([]string(nil), manifest.OAuthConfig.Scopes.Bot...)
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
