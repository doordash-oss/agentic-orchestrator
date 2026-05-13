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

package clirun

import (
	"testing"
)

func TestParseVersionOutput(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"bare semver", "1.0.20", "1.0.20", false},
		{"claude prefix", "claude 2.1.112", "2.1.112", false},
		{"codex prefix with v", "OpenAI Codex v0.120.0", "0.120.0", false},
		{"leading/trailing whitespace", "  1.2.3  \n", "1.2.3", false},
		{"no match", "not a version", "", true},
		{"empty", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseVersionOutput([]byte(tt.input))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("got = %q, want = %q", got, tt.want)
			}
		})
	}
}
