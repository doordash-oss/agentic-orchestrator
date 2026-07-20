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

package main

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestDesktopLaunchCommandUsesRegisteredApplication(t *testing.T) {
	tests := []struct {
		name     string
		goos     string
		wantName string
		wantArgs []string
		wantErr  bool
	}{
		{name: "macOS bundle", goos: "darwin", wantName: "open", wantArgs: []string{"-b", "com.doordash.agentico"}},
		{name: "Linux protocol", goos: "linux", wantName: "xdg-open", wantArgs: []string{"agentico://"}},
		{name: "unsupported platform", goos: "windows", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, args, err := desktopLaunchCommand(tt.goos)
			if (err != nil) != tt.wantErr {
				t.Fatalf("desktopLaunchCommand(%q) error = %v, wantErr %v", tt.goos, err, tt.wantErr)
			}
			if name != tt.wantName {
				t.Errorf("desktopLaunchCommand(%q) name = %q, want %q", tt.goos, name, tt.wantName)
			}
			if !slices.Equal(args, tt.wantArgs) {
				t.Errorf("desktopLaunchCommand(%q) args = %v, want %v", tt.goos, args, tt.wantArgs)
			}
		})
	}
}

func TestRunDesktopLaunchCommandIncludesStderr(t *testing.T) {
	err := runDesktopLaunchCommand(context.Background(), "sh", []string{"-c", "printf 'application not registered' >&2; exit 7"})
	if err == nil {
		t.Fatal("runDesktopLaunchCommand() error = nil; want launch failure")
	}
	if !strings.Contains(err.Error(), "application not registered") {
		t.Errorf("runDesktopLaunchCommand() error = %q; want launcher stderr", err)
	}
}
