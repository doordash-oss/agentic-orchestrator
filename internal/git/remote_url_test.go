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

package git

import "testing"

func TestParseRemoteURL(t *testing.T) {
	cases := []struct {
		remote            string
		host, owner, repo string
		wantErr           bool
	}{
		{remote: "https://github.com/acme/widgets.git", host: "github.com", owner: "acme", repo: "widgets"},
		{remote: "https://github.com/acme/widgets", host: "github.com", owner: "acme", repo: "widgets"},
		{remote: "git@github.com:acme/widgets.git", host: "github.com", owner: "acme", repo: "widgets"},
		{remote: "ssh://git@ghe.corp.example/acme/widgets.git", host: "ghe.corp.example", owner: "acme", repo: "widgets"},
		{remote: "/local/path/repo", wantErr: true},
		{remote: "https://github.com/only-owner", wantErr: true},
	}
	for _, tc := range cases {
		host, owner, repo, err := ParseRemoteURL(tc.remote)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseRemoteURL(%q) = %s/%s/%s; want error", tc.remote, host, owner, repo)
			}
			continue
		}
		if err != nil || host != tc.host || owner != tc.owner || repo != tc.repo {
			t.Errorf("ParseRemoteURL(%q) = %s/%s/%s, %v; want %s/%s/%s", tc.remote, host, owner, repo, err, tc.host, tc.owner, tc.repo)
		}
	}
}
