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
	"strings"

	"charm.land/bubbles/v2/key"
)

// shiftEnterKey is the shared binding for "insert a literal newline" across
// every textarea in the app (chat input, attach input, AskUserQuestion
// freeform/notes editors) — plain Enter always sends/submits instead.
var shiftEnterKey = key.NewBinding(key.WithKeys("shift+enter"))

// growTextareaHeight returns the textarea height after growing by one line
// (capped at maxLines). Call this BEFORE inserting a newline so the
// textarea grows before the content does — otherwise the viewport scrolls
// prior lines out of view at the old (smaller) height for one frame.
func growTextareaHeight(current, maxLines int) int {
	return min(current+1, maxLines)
}

// syncTextareaHeight recalculates textarea height from content line count,
// clamped to [minLines, maxLines]. Call after any operation that changes
// textarea content (paste, delete, programmatic SetValue).
func syncTextareaHeight(value string, minLines, maxLines int) int {
	lineCount := strings.Count(value, "\n") + 1
	h := max(lineCount, minLines)
	return min(h, maxLines)
}
