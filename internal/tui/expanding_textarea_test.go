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

package tui

import "testing"

func TestGrowTextareaHeight(t *testing.T) {
	t.Parallel()
	if got := growTextareaHeight(1); got != 2 {
		t.Errorf("growTextareaHeight(1) = %d, want 2", got)
	}
	if got := growTextareaHeight(6); got != 6 {
		t.Errorf("growTextareaHeight(6) = %d, want 6 (capped)", got)
	}
}

func TestSyncTextareaHeight(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		value    string
		minLines int
		maxLines int
		want     int
	}{
		{"empty collapses to min", "", 1, 6, 1},
		{"single line stays at min", "hello", 1, 6, 1},
		{"two lines grows to two", "hello\nworld", 1, 6, 2},
		{"caps at maxLines", "1\n2\n3\n4\n5\n6\n7\n8", 1, 6, 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := syncTextareaHeight(tc.value, tc.minLines, tc.maxLines); got != tc.want {
				t.Errorf("syncTextareaHeight(%q, %d, %d) = %d, want %d", tc.value, tc.minLines, tc.maxLines, got, tc.want)
			}
		})
	}
}
