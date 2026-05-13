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
	"testing"
)

func TestLogsViewHidesPublishHintForUnpublished(t *testing.T) {
	t.Parallel()
	m := NewLogsModel("Test", "content", 80, 40)
	m.featureID = "f1"
	m.autoPublish = false
	m.isPublishable = false
	view := m.View()
	if strings.Contains(view, "[p]") {
		t.Error("expected [p] hint to be hidden for unpublished feature")
	}
}

func TestLogsViewShowsPublishHintForPublished(t *testing.T) {
	t.Parallel()
	m := NewLogsModel("Test", "content", 80, 40)
	m.featureID = "f1"
	m.autoPublish = false
	m.isPublishable = true
	view := m.View()
	if !strings.Contains(view, "[p]") {
		t.Error("expected [p] hint to be visible for published feature")
	}
}
