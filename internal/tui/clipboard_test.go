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
	"testing"
)

func TestCanPasteClipboardImage(t *testing.T) {
	// Just verify it returns a bool without panicking.
	// The result depends on the host system.
	_ = canPasteClipboardImage()
}

func TestSaveClipboardImageNoImage(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping host clipboard probe in short mode")
	}
	if !canPasteClipboardImage() {
		t.Skip("clipboard pasting not supported on this platform")
	}
	// On CI or a machine with no image on clipboard, this should fail gracefully.
	_, err := saveClipboardImage(t.TempDir(), 1)
	// We can't assert err != nil because there might be an image on the clipboard.
	// Just ensure it doesn't panic.
	_ = err
}

func TestSaveClipboardFilesNoFile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping host clipboard probe in short mode")
	}
	if !canPasteClipboardImage() {
		t.Skip("clipboard pasting not supported on this platform")
	}
	paths, names, err := saveClipboardFiles(t.TempDir())
	// Can't assert specific result — depends on clipboard state
	// Just verify no panic
	_ = paths
	_ = names
	_ = err
}
