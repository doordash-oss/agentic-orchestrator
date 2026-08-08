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

package llm

import "testing"

func TestMatchAskUserOptionLabel(t *testing.T) {
	labels := []string{"Option A (Recommended)", "Option B", "Other approach"}
	tests := []struct {
		name   string
		answer string
		want   string
		ok     bool
	}{
		{"exact", "Option B", "Option B", true},
		{"exact with recommended suffix", "Option A (Recommended)", "Option A (Recommended)", true},
		{"missing recommended suffix", "Option A", "Option A (Recommended)", true},
		{"surrounding whitespace", "  Option B  ", "Option B", true},
		{"ascii ellipsis truncation", "Option A (Recomm...", "Option A (Recommended)", true},
		{"unicode ellipsis truncation", "Other app…", "Other approach", true},
		{"ambiguous truncation", "Option...", "", false},
		{"free text", "do something else entirely", "", false},
		{"empty", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := MatchAskUserOptionLabel(labels, tt.answer)
			if got != tt.want || ok != tt.ok {
				t.Errorf("MatchAskUserOptionLabel(%q) = (%q, %v), want (%q, %v)", tt.answer, got, ok, tt.want, tt.ok)
			}
		})
	}
}
