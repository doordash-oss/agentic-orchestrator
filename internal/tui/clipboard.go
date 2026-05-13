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
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// canPasteClipboardImage checks if clipboard image pasting is supported.
// Currently only macOS is supported (using osascript).
func canPasteClipboardImage() bool {
	return runtime.GOOS == "darwin"
}

// pasteClipboardImage saves the current clipboard image to destPath as PNG.
// Uses native macOS osascript — no third-party tools required.
// Returns an error if no image is on the clipboard.
func pasteClipboardImage(destPath string) error {
	_ = os.Remove(destPath) // ensure clean file for AppleScript open-for-access
	fileRef := fmt.Sprintf(`set theFile to open for access POSIX file %q with write permission`, destPath)
	cmd := exec.Command("osascript",
		"-e", `set theImage to the clipboard as «class PNGf»`,
		"-e", fileRef,
		"-e", `set eof theFile to 0`,
		"-e", `write theImage to theFile`,
		"-e", `close access theFile`,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("clipboard paste failed: %s: %w", string(out), err)
	}
	return nil
}

// saveClipboardImage saves a clipboard image to a numbered file in dir.
// Returns the file path on success, or error if no image is available.
func saveClipboardImage(dir string, index int) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating image dir: %w", err)
	}
	path := filepath.Join(dir, fmt.Sprintf("image-%d.png", index))
	if err := pasteClipboardImage(path); err != nil {
		return "", err
	}
	return path, nil
}

// getClipboardText returns the current clipboard text content using pbpaste (macOS).
func getClipboardText() (string, error) {
	out, err := exec.Command("pbpaste").Output()
	if err != nil {
		return "", fmt.Errorf("pbpaste failed: %w", err)
	}
	return string(out), nil
}

// saveClipboardFiles saves clipboard file(s) to destDir using their original filenames.
// Uses macOS osascript with «class furl» to detect files copied in Finder.
// Returns (destPaths, originalNames, error). Returns nil, nil, nil if no files on clipboard.
func saveClipboardFiles(destDir string) ([]string, []string, error) {
	cmd := exec.Command("osascript",
		"-e", `try`,
		"-e", `set theFile to the clipboard as «class furl»`,
		"-e", `return POSIX path of theFile`,
		"-e", `end try`,
		"-e", `return ""`,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, nil, nil // no files on clipboard
	}

	srcPath := strings.TrimSpace(string(out))
	if srcPath == "" {
		return nil, nil, nil
	}
	// Remove trailing slash (directories)
	srcPath = strings.TrimRight(srcPath, "/")

	// Validate source is a regular file we can read
	info, err := os.Stat(srcPath)
	if err != nil || info.IsDir() {
		return nil, nil, nil
	}

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return nil, nil, fmt.Errorf("creating attachment dir: %w", err)
	}

	name := filepath.Base(srcPath)
	destPath := filepath.Join(destDir, name)

	// Handle filename collision within temp dir
	if _, err := os.Stat(destPath); err == nil {
		ext := filepath.Ext(name)
		base := strings.TrimSuffix(name, ext)
		for i := 2; i <= 1000; i++ {
			destPath = filepath.Join(destDir, fmt.Sprintf("%s-%d%s", base, i, ext))
			_, statErr := os.Stat(destPath)
			if os.IsNotExist(statErr) {
				break
			}
			if statErr != nil {
				return nil, nil, fmt.Errorf("checking dest path: %w", statErr)
			}
			if i == 1000 {
				return nil, nil, fmt.Errorf("too many filename collisions for %s", name)
			}
		}
	}

	data, err := os.ReadFile(srcPath)
	if err != nil {
		return nil, nil, fmt.Errorf("reading source file %s: %w", srcPath, err)
	}
	if err := os.WriteFile(destPath, data, 0o644); err != nil {
		return nil, nil, fmt.Errorf("writing temp file: %w", err)
	}

	return []string{destPath}, []string{name}, nil
}
