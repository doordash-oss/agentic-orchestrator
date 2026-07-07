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
	"fmt"

	"charm.land/lipgloss/v2"
)

// autoPickedTag returns the label and style for a "who answered this"
// turn tag: the normal "[you]" tag in colorBrand, or the warning-accented
// "[auto-picked]"/"[auto-picked, confidence: X]" tag when the answer was
// decided out-of-band without the interactive picker. Shared by attach.go's
// transcript loop and chat.go's renderChatTurn so the two surfaces can't
// visually drift on this.
func autoPickedTag(autoPicked bool, confidence float64) (string, lipgloss.Style) {
	if !autoPicked {
		return "[you]", chatUserTagStyle
	}
	label := "[auto-picked]"
	if confidence > 0 {
		label = fmt.Sprintf("[auto-picked, confidence: %.2f]", confidence)
	}
	return label, lipgloss.NewStyle().Bold(true).Foreground(colorWarning)
}
