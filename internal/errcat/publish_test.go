// Copyright 2026 DoorDash, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package errcat

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// allPublishFailureCodes lists the seven catalog codes a repository publish
// failure can carry, in catalog order.
var allPublishFailureCodes = []Code{
	PublishRebaseConflict,
	PublishRemoteDiverged,
	PublishRemoteChanged,
	PublishPullRequestClosed,
	PublishPullRequestFailed,
	PublishDescriptionFailed,
	PublishPushFailed,
}

// TestPublishFailureCodesAreNeedsActionPublishRetry pins the publish-failure
// contract: all seven codes are needs_action, reference the publish action,
// and declare exactly the repositories block.
func TestPublishFailureCodesAreNeedsActionPublishRetry(t *testing.T) {
	for _, code := range allPublishFailureCodes {
		entry, ok := Lookup(code)
		if !ok {
			t.Fatalf("%s: missing from catalog", code)
		}
		if entry.Class != ClassNeedsAction {
			t.Errorf("%s: class is %q; want needs_action", code, entry.Class)
		}
		if len(entry.Actions) != 1 || entry.Actions[0] != "publish" {
			t.Errorf("%s: actions = %#v; want [publish]", code, entry.Actions)
		}
		if len(entry.Blocks) != 1 || entry.Blocks[0] != BlockRepositories {
			t.Errorf("%s: blocks = %#v; want exactly the repositories block", code, entry.Blocks)
		}
		if strings.TrimSpace(entry.Summary) == "" {
			t.Errorf("%s: empty static summary", code)
		}
	}
}

// TestIsPublishFailureReturnsTrueForExactlyThePublishCodes pins the closed
// set: the helper is true for the seven publish codes and nothing else.
func TestIsPublishFailureReturnsTrueForExactlyThePublishCodes(t *testing.T) {
	want := map[Code]bool{}
	for _, code := range allPublishFailureCodes {
		want[code] = true
	}
	for _, code := range Codes() {
		if got := IsPublishFailure(code); got != want[code] {
			t.Errorf("IsPublishFailure(%s) = %v; want %v", code, got, want[code])
		}
	}
}

// TestRenderRecordPublishRebaseConflictNamesRepositoryBranchAndTarget pins
// the conflict projection: a stored record whose repositories block carries a
// name, branch, and rebase target renders a summary naming all three,
// without leaking diagnostics.
func TestRenderRecordPublishRebaseConflictNamesRepositoryBranchAndTarget(t *testing.T) {
	rendered := RenderRecord(FailureRecord{
		Code: PublishRebaseConflict,
		Context: &RecordContext{
			Repositories: []CodeRepository{{
				Name:         "publish-web",
				Branch:       "agentico/my-feature",
				RebaseTarget: "main",
			}},
		},
		Diagnostics: "CONFLICT (content): Merge conflict in go.mod",
	})
	for _, want := range []string{`"publish-web"`, `"agentico/my-feature"`, `"main"`} {
		if !strings.Contains(rendered.Summary, want) {
			t.Fatalf("summary does not name %s: %q", want, rendered.Summary)
		}
	}
	if strings.Contains(rendered.Summary, "CONFLICT") {
		t.Fatalf("summary leaks raw diagnostics: %q", rendered.Summary)
	}
	if rendered.Class != ClassNeedsAction {
		t.Fatalf("class = %q; want needs_action", rendered.Class)
	}
	if rendered.Remediation == nil || len(rendered.Remediation.Actions) != 1 || rendered.Remediation.Actions[0] != "publish" {
		t.Fatalf("publish_rebase_conflict must reference the publish action: %#v", rendered.Remediation)
	}
	if rendered.Context == nil || len(rendered.Context.Repositories) != 1 || rendered.Context.Repositories[0].RebaseTarget != "main" {
		t.Fatalf("repositories block not carried with the rebase target: %#v", rendered.Context)
	}
}

// TestRenderRecordPublishRemoteDivergedReproducesCountSummary pins the
// diverged projection: a stored record with a remote-only commit count
// renders the same count-bearing summary the mutation rejection renders.
func TestRenderRecordPublishRemoteDivergedReproducesCountSummary(t *testing.T) {
	rendered := RenderRecord(FailureRecord{
		Code: PublishRemoteDiverged,
		Context: &RecordContext{
			Repositories: []CodeRepository{{
				Name:              "publish-api",
				Branch:            "agentico/my-feature",
				RemoteOnlyCommits: 3,
			}},
		},
		Diagnostics: "remote branch moved: 3 new commits",
	})
	want := `The pull-request branch for "publish-api" contains 3 remote commits that are not in this workspace.`
	if rendered.Summary != want {
		t.Fatalf("summary = %q; want %q", rendered.Summary, want)
	}
	if strings.Contains(rendered.Summary, rendered.Diagnostics) {
		t.Fatalf("summary leaks raw diagnostics: %q", rendered.Summary)
	}
	if rendered.Class != ClassNeedsAction {
		t.Fatalf("class = %q; want needs_action", rendered.Class)
	}
}

// TestRenderRecordPublishCodesFallBackToStaticSummaries pins the static
// fallback: a publish record with no context renders the entry's authored
// static summary.
func TestRenderRecordPublishCodesFallBackToStaticSummaries(t *testing.T) {
	for _, code := range allPublishFailureCodes {
		entry, ok := Lookup(code)
		if !ok {
			t.Fatalf("%s: missing from catalog", code)
		}
		rendered := RenderRecord(FailureRecord{Code: code, Diagnostics: "raw detail"})
		if rendered.Summary != entry.Summary {
			t.Errorf("%s: no-context summary = %q; want static %q", code, rendered.Summary, entry.Summary)
		}
		if strings.Contains(rendered.Summary, "raw detail") {
			t.Errorf("%s: static summary leaks diagnostics: %q", code, rendered.Summary)
		}
	}
}

// TestPublishRecordRoundTripsYAMLAndJSON pins the stored shape of a
// repository publish-failure record: the rebase_target and
// remote_only_commits block fields survive both marshal cycles unchanged.
func TestPublishRecordRoundTripsYAMLAndJSON(t *testing.T) {
	record := FailureRecord{
		Code: PublishRebaseConflict,
		Context: &RecordContext{
			Repositories: []CodeRepository{{
				Name:              "publish-web",
				Branch:            "agentico/my-feature",
				RebaseTarget:      "main",
				RemoteOnlyCommits: 2,
			}},
		},
		Diagnostics: "git rebase: conflict in go.mod",
	}

	yamlBytes, err := yaml.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var fromYAML FailureRecord
	if err := yaml.Unmarshal(yamlBytes, &fromYAML); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fromYAML, record) {
		t.Fatalf("YAML round-trip mismatch:\n got %#v\nwant %#v\nyaml:\n%s", fromYAML, record, yamlBytes)
	}
	if !strings.Contains(string(yamlBytes), "rebase_target: main") {
		t.Fatalf("YAML does not carry the rebase_target key:\n%s", yamlBytes)
	}
	if !strings.Contains(string(yamlBytes), "remote_only_commits: 2") {
		t.Fatalf("YAML does not carry the remote_only_commits key:\n%s", yamlBytes)
	}

	jsonBytes, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var fromJSON FailureRecord
	if err := json.Unmarshal(jsonBytes, &fromJSON); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fromJSON, record) {
		t.Fatalf("JSON round-trip mismatch:\n got %#v\nwant %#v", fromJSON, record)
	}
}

// TestFprintRendersPublishRepositoryFields pins the CLI shape: the rebase
// target and remote-only commit count render as key-value lines under the
// repository line when present.
func TestFprintRendersPublishRepositoryFields(t *testing.T) {
	rendered := New(
		PublishRebaseConflict,
		WithRepositories(CodeRepository{
			Name:              "publish-web",
			Branch:            "agentico/my-feature",
			RebaseTarget:      "main",
			RemoteOnlyCommits: 2,
		}),
	)
	var out strings.Builder
	if err := Fprint(&out, rendered); err != nil {
		t.Fatal(err)
	}
	lines := out.String()
	if !strings.Contains(lines, "  repository: publish-web, branch agentico/my-feature") {
		t.Fatalf("repository line missing:\n%s", lines)
	}
	if !strings.Contains(lines, "    rebase_target: main") {
		t.Fatalf("rebase_target line missing:\n%s", lines)
	}
	if !strings.Contains(lines, "    remote_only_commits: 2") {
		t.Fatalf("remote_only_commits line missing:\n%s", lines)
	}
}
