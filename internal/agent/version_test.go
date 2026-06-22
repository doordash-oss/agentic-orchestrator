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

package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// mockVersionProvider implements llm.LLMProvider for version tests.
type mockVersionProvider struct {
	name        string
	version     string
	versionErr  error
	installHint string
	minVer      [3]int
}

func (m *mockVersionProvider) Name() string                 { return m.name }
func (m *mockVersionProvider) VersionInfo() (string, error) { return m.version, m.versionErr }
func (m *mockVersionProvider) InstallHint() string          { return m.installHint }
func (m *mockVersionProvider) MatchesModel(_ string) bool   { return false }
func (m *mockVersionProvider) DetectCLI() bool              { return true }
func (m *mockVersionProvider) AvailableModels() []string    { return nil }
func (m *mockVersionProvider) BuildCommand(_ llm.CommandBuildOpts) ([]string, []string, error) {
	return nil, nil, nil
}
func (m *mockVersionProvider) NewProtocol(_ llm.ProtocolOpts) llm.Protocol { return nil }
func (m *mockVersionProvider) MinVersion() [3]int                          { return m.minVer }
func (m *mockVersionProvider) EnvVarsToExclude() []string                  { return nil }

// Ensure mockVersionProvider satisfies llm.LLMProvider at compile time.
var _ llm.LLMProvider = (*mockVersionProvider)(nil)

func TestCheckProviderVersions_MultipleProviders(t *testing.T) {
	providers := []llm.LLMProvider{
		&mockVersionProvider{name: "claude", version: "1.2.3", minVer: [3]int{1, 0, 0}, installHint: "npm install -g @anthropic-ai/claude-code"},
		&mockVersionProvider{name: "codex", version: "0.5.0", minVer: [3]int{0, 10, 0}, installHint: "npm install -g codex"},
	}

	results := CheckProviderVersions(providers)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// claude: meets minimum, no warning
	if results[0].Provider != "claude" {
		t.Errorf("expected provider 'claude', got %q", results[0].Provider)
	}
	if results[0].Version != "1.2.3" {
		t.Errorf("expected version '1.2.3', got %q", results[0].Version)
	}
	if results[0].Err != nil {
		t.Errorf("expected no error for claude, got %v", results[0].Err)
	}
	if results[0].Warning != "" {
		t.Errorf("expected no warning for claude, got %q", results[0].Warning)
	}

	// codex: below minimum, should have warning
	if results[1].Provider != "codex" {
		t.Errorf("expected provider 'codex', got %q", results[1].Provider)
	}
	if results[1].Version != "0.5.0" {
		t.Errorf("expected version '0.5.0', got %q", results[1].Version)
	}
	if results[1].Err != nil {
		t.Errorf("expected no error for codex, got %v", results[1].Err)
	}
	if results[1].Warning == "" {
		t.Error("expected warning for codex below minimum version")
	}
	if !strings.Contains(results[1].Warning, "below minimum") {
		t.Errorf("warning should mention 'below minimum', got %q", results[1].Warning)
	}
}

func TestCheckProviderVersions_OneFailure(t *testing.T) {
	providers := []llm.LLMProvider{
		&mockVersionProvider{name: "claude", version: "1.2.3", minVer: [3]int{1, 0, 0}},
		&mockVersionProvider{name: "codex", versionErr: fmt.Errorf("CLI not found")},
	}

	results := CheckProviderVersions(providers)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// claude: success
	if results[0].Version != "1.2.3" {
		t.Errorf("expected version '1.2.3', got %q", results[0].Version)
	}
	if results[0].Err != nil {
		t.Errorf("expected no error for claude, got %v", results[0].Err)
	}

	// codex: error
	if results[1].Err == nil {
		t.Error("expected error for codex, got nil")
	}
	if results[1].Version != "" {
		t.Errorf("expected empty version for codex error case, got %q", results[1].Version)
	}
}

func TestCheckProviderVersions_Empty(t *testing.T) {
	results := CheckProviderVersions([]llm.LLMProvider{})
	if len(results) != 0 {
		t.Fatalf("expected 0 results for empty input, got %d", len(results))
	}
}

func TestCheckProviderVersions_BelowMinimum(t *testing.T) {
	providers := []llm.LLMProvider{
		&mockVersionProvider{name: "claude", version: "0.0.1", minVer: [3]int{1, 0, 0}, installHint: "npm install -g @anthropic-ai/claude-code"},
	}

	results := CheckProviderVersions(providers)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	r := results[0]
	if r.Err != nil {
		t.Errorf("expected no error, got %v", r.Err)
	}
	if r.Warning == "" {
		t.Error("expected warning for version below minimum")
	}
	if !strings.Contains(r.Warning, "below minimum") {
		t.Errorf("warning should mention 'below minimum', got %q", r.Warning)
	}
	if !strings.Contains(r.Warning, "npm install -g @anthropic-ai/claude-code") {
		t.Errorf("warning should contain install hint, got %q", r.Warning)
	}
}

func TestCheckProviderVersions_UnparseableVersion(t *testing.T) {
	providers := []llm.LLMProvider{
		&mockVersionProvider{name: "claude", version: "some-weird-string"},
	}

	results := CheckProviderVersions(providers)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	r := results[0]
	if r.Version != "some-weird-string" {
		t.Errorf("expected version 'some-weird-string', got %q", r.Version)
	}
	if r.Err != nil {
		t.Errorf("expected no error, got %v", r.Err)
	}
	if r.Warning == "" {
		t.Error("expected warning for unparseable version")
	}
	if !strings.Contains(r.Warning, "could not parse") {
		t.Errorf("warning should mention 'could not parse', got %q", r.Warning)
	}
}

func TestCheckProviderVersions_PerProviderMinVersions(t *testing.T) {
	providers := []llm.LLMProvider{
		&mockVersionProvider{name: "claude", version: "2.1.81", minVer: [3]int{2, 1, 81}},
		&mockVersionProvider{name: "codex", version: "0.116.0", minVer: [3]int{0, 116, 0}},
	}

	results := CheckProviderVersions(providers)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	// Both meet their own minimums — no warnings
	if results[0].Warning != "" {
		t.Errorf("expected no warning for claude at exact min, got %q", results[0].Warning)
	}
	if results[1].Warning != "" {
		t.Errorf("expected no warning for codex at exact min, got %q", results[1].Warning)
	}
}

func TestBelowMinVersion(t *testing.T) {
	tests := []struct {
		name       string
		version    string
		versionErr error
		minVer     [3]int
		wantBelow  bool
	}{
		{"below minimum", "1.17.8", nil, [3]int{1, 17, 9}, true},
		{"exact minimum", "1.17.9", nil, [3]int{1, 17, 9}, false},
		{"above minimum", "1.18.0", nil, [3]int{1, 17, 9}, false},
		// VersionInfo errors and unparseable output are warn-only, not hard
		// failures: BelowMinVersion must not report them as below-minimum.
		{"version error is not below", "", fmt.Errorf("not installed"), [3]int{1, 17, 9}, false},
		{"unparseable is not below", "some-weird-string", nil, [3]int{1, 17, 9}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &mockVersionProvider{name: "opencode", version: tt.version, versionErr: tt.versionErr, minVer: tt.minVer}
			below, version, minVer := BelowMinVersion(p)
			if below != tt.wantBelow {
				t.Fatalf("BelowMinVersion below = %v, want %v", below, tt.wantBelow)
			}
			if minVer != tt.minVer {
				t.Fatalf("BelowMinVersion minVer = %v, want %v", minVer, tt.minVer)
			}
			if tt.versionErr == nil && version != tt.version {
				t.Fatalf("BelowMinVersion version = %q, want %q", version, tt.version)
			}
			if tt.versionErr != nil && version != "" {
				t.Fatalf("BelowMinVersion version = %q, want empty on VersionInfo error", version)
			}
		})
	}
}

func TestParseCLIVersion(t *testing.T) {
	tests := []struct {
		input               string
		major, minor, patch int
		wantErr             bool
	}{
		{"1.0.0", 1, 0, 0, false},
		{"1.2.3", 1, 2, 3, false},
		{"claude 1.0.17", 1, 0, 17, false},
		{"Claude Code v2.3.4-beta", 2, 3, 4, false},
		{"10.20.30", 10, 20, 30, false},
		{"no version here", 0, 0, 0, true},
		{"", 0, 0, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			major, minor, patch, err := parseCLIVersion(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if major != tt.major || minor != tt.minor || patch != tt.patch {
				t.Errorf("got %d.%d.%d, want %d.%d.%d", major, minor, patch, tt.major, tt.minor, tt.patch)
			}
		})
	}
}

func TestMeetsMinVersion(t *testing.T) {
	minVer := [3]int{1, 5, 0}

	tests := []struct {
		name                string
		major, minor, patch int
		want                bool
	}{
		{"exact match", 1, 5, 0, true},
		{"higher patch", 1, 5, 1, true},
		{"higher minor", 1, 6, 0, true},
		{"higher major", 2, 0, 0, true},
		{"lower patch - at boundary", 1, 4, 99, false},
		{"lower minor", 1, 4, 0, false},
		{"lower major", 0, 99, 99, false},
		{"zero version", 0, 0, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := meetsMinVersion(tt.major, tt.minor, tt.patch, minVer)
			if got != tt.want {
				t.Errorf("meetsMinVersion(%d, %d, %d) = %v, want %v",
					tt.major, tt.minor, tt.patch, got, tt.want)
			}
		})
	}
}
