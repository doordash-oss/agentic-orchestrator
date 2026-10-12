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

package clirun

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestParseVersionOutput(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"bare semver", "1.0.20", "1.0.20", false},
		{"claude prefix", "claude 2.1.112", "2.1.112", false},
		{"codex prefix with v", "OpenAI Codex v0.120.0", "0.120.0", false},
		{"leading/trailing whitespace", "  1.2.3  \n", "1.2.3", false},
		{"no match", "not a version", "", true},
		{"empty", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseVersionOutput([]byte(tt.input))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("got = %q, want = %q", got, tt.want)
			}
		})
	}
}

func TestVersionProbe(t *testing.T) {
	t.Run("returns trimmed raw output", func(t *testing.T) {
		var gotName string
		var gotArgs []string
		runner := func(ctx context.Context, name string, args []string, env []string) ([]byte, error) {
			gotName = name
			gotArgs = args
			if env != nil {
				t.Fatalf("env = %v, want nil", env)
			}
			return []byte("claude 2.1.81 (Claude Code)\n"), nil
		}
		got, err := VersionProbe(runner, "claude")
		if err != nil {
			t.Fatalf("VersionProbe() error: %v", err)
		}
		if got != "claude 2.1.81 (Claude Code)" {
			t.Fatalf("VersionProbe() = %q, want trimmed raw output", got)
		}
		if gotName != "claude" {
			t.Errorf("VersionProbe probed %q, want claude", gotName)
		}
		if !slices.Equal(gotArgs, []string{"--version"}) {
			t.Errorf("VersionProbe args = %v, want [--version]", gotArgs)
		}
	})
	t.Run("returns the raw runner error", func(t *testing.T) {
		wantErr := errors.New("boom")
		runner := func(ctx context.Context, name string, args []string, env []string) ([]byte, error) {
			return nil, wantErr
		}
		got, err := VersionProbe(runner, "claude")
		if err == nil {
			t.Fatalf("VersionProbe() = %q, want error", got)
		}
		if !errors.Is(err, wantErr) {
			t.Fatalf("VersionProbe() error = %v, want the raw runner error", err)
		}
	})
}
