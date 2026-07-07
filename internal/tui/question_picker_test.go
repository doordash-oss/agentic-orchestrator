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

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseAskUserQuestions(t *testing.T) {
	t.Parallel()
	input := json.RawMessage(`{"questions":[{"question":"Pick one","multiSelect":false,"options":[{"label":"A"},{"label":"B"}]}]}`)
	got := parseAskUserQuestions(input)
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].Question != "Pick one" || len(got[0].Options) != 2 {
		t.Errorf("got[0] = %+v, want Question=%q with 2 options", got[0], "Pick one")
	}
}

func TestQuestionUsesDirectFreeform(t *testing.T) {
	t.Parallel()
	if !questionUsesDirectFreeform(askUserQuestion{}) {
		t.Error("question with no options should use direct freeform")
	}
	if questionUsesDirectFreeform(askUserQuestion{Options: []askUserOption{{Label: "A"}}}) {
		t.Error("question with options should not use direct freeform")
	}
}

func TestQuestionVisibleWindowPureNoScrollNeeded(t *testing.T) {
	t.Parallel()
	opts := []askUserOption{{Label: "A"}, {Label: "B"}}
	start, end, needAbove, needBelow := questionVisibleWindowPure(opts, 0, 0, 20, 40)
	if start != 0 || end != 2 || needAbove || needBelow {
		t.Errorf("got (%d, %d, %v, %v), want (0, 2, false, false)", start, end, needAbove, needBelow)
	}
}

func TestRenderQuestionOptionsBlockMarksSelectedRow(t *testing.T) {
	t.Parallel()
	opts := []askUserOption{{Label: "staging"}, {Label: "production"}}
	q := askUserQuestion{Question: "Which env?", Options: opts}
	out := renderQuestionOptionsBlock(q, 1, nil, 0, 2, false, false)
	if !strings.Contains(out, "production") || !strings.Contains(out, "staging") {
		t.Fatalf("rendered block missing option labels: %q", out)
	}
	// The selected row (index 1, "production") gets the "> " cursor; the
	// unselected row gets two spaces.
	lines := strings.Split(out, "\n")
	foundCursorOnProduction := false
	for _, line := range lines {
		if strings.Contains(line, "production") && strings.HasPrefix(strings.TrimLeft(line, "\x1b[0123456789;m"), "> ") {
			foundCursorOnProduction = true
		}
	}
	if !foundCursorOnProduction {
		t.Errorf("expected '> ' cursor on the selected 'production' row, got: %q", out)
	}
}
