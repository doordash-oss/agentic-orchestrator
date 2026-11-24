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

package server

import (
	"testing"
	"unicode/utf8"
)

func TestSafeDisplayTextKeepsRunesWhole(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure function, table-driven.
	tests := []struct {
		name  string
		in    string
		limit int
		want  string
	}{
		{"ascii", "abcdef", 3, "abc..."},
		{"cut inside em dash", "ab—cd", 3, "ab..."},
		{"cut after em dash", "ab—cd", 5, "ab—..."},
		{"within limit", "ab—", 5, "ab—"},
		{"no limit", "ab—cd", 0, "ab—cd"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := SafeDisplayText(tt.in, tt.limit)
			if got != tt.want || !utf8.ValidString(got) {
				t.Fatalf("SafeDisplayText(%q, %d) = %q, want %q", tt.in, tt.limit, got, tt.want)
			}
		})
	}
}
