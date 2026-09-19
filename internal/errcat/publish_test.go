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

// allPublishFailureCodes lists the eleven catalog codes a repository publish
// failure can carry, in catalog order.
var allPublishFailureCodes = []Code{
	PublishRemoteDiverged,
	PublishRemoteChanged,
	PublishStackPullRequestClosed,
	PublishStackMissing,
	PublishPullRequestFailed,
	PublishDescriptionFailed,
	PublishPushFailed,
	PublishStateWriteFailed,
	PublishReopenFailed,
	PublishHeadBranchMissing,
	PublishRecreateFailed,
}

// publishFailureActions pins the action list every publish-failure code
// references. The closed, reopen-failed, and head-branch-missing family
// resolves through reopen/recreate; the delivery and state-write failures
// retry publish.
var publishFailureActions = map[Code][]string{
	PublishRemoteDiverged:         {"publish"},
	PublishRemoteChanged:          {"publish"},
	PublishStackPullRequestClosed: {"reopen-pull-request", "recreate-pull-request"},
	PublishStackMissing:           {"publish"},
	PublishPullRequestFailed:      {"publish"},
	PublishDescriptionFailed:      {"publish"},
	PublishPushFailed:             {"publish"},
	PublishStateWriteFailed:       {"publish"},
	PublishReopenFailed:           {"reopen-pull-request", "recreate-pull-request"},
	PublishHeadBranchMissing:      {"recreate-pull-request"},
	PublishRecreateFailed:         {"recreate-pull-request"},
}

// TestPublishFailureCodesAreNeedsActionWithPinnedActions pins the
// publish-failure contract: all eleven codes are needs_action, reference
// exactly their pinned action list, and declare exactly the repositories
// block.
func TestPublishFailureCodesAreNeedsActionWithPinnedActions(t *testing.T) {
	for _, code := range allPublishFailureCodes {
		entry, ok := Lookup(code)
		if !ok {
			t.Fatalf("%s: missing from catalog", code)
		}
		if entry.Class != ClassNeedsAction {
			t.Errorf("%s: class is %q; want needs_action", code, entry.Class)
		}
		want := publishFailureActions[code]
		if len(entry.Actions) != len(want) {
			t.Errorf("%s: actions = %#v; want %#v", code, entry.Actions, want)
		} else {
			for i, action := range want {
				if entry.Actions[i] != action {
					t.Errorf("%s: actions = %#v; want %#v", code, entry.Actions, want)
					break
				}
			}
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
// set: the helper is true for the eleven publish codes and nothing else.
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

// TestRetiredPublishCodesAreMissingFromCatalog pins the retirement: the
// pull-rebase conflict and closed-PR publish codes no longer resolve in the
// catalog and no longer classify as publish failures.
func TestRetiredPublishCodesAreMissingFromCatalog(t *testing.T) {
	for _, code := range []Code{"publish_rebase_conflict", "publish_pull_request_closed"} {
		if _, ok := Lookup(code); ok {
			t.Errorf("%s: retired code still resolves in the catalog", code)
		}
		if IsPublishFailure(code) {
			t.Errorf("%s: retired code still classifies as a publish failure", code)
		}
	}
}

// TestRenderRecordPublishStackPullRequestClosedNamesRepositoryLayerAndPR pins
// the closed-stack projection: a stored record whose repositories block
// carries a name, layer, and pull-request URL renders a summary naming all
// three, without leaking diagnostics.
func TestRenderRecordPublishStackPullRequestClosedNamesRepositoryLayerAndPR(t *testing.T) {
	rendered := RenderRecord(FailureRecord{
		Code: PublishStackPullRequestClosed,
		Context: &RecordContext{
			Repositories: []CodeRepository{{
				Name:           "publish-web",
				Branch:         "agentico/my-feature",
				LayerPosition:  2,
				LayerTitle:     "Fix auth",
				PullRequestURL: "https://github.com/acme/publish-web/pull/12",
			}},
		},
		Diagnostics: "pull request state: closed",
	})
	want := `The stack pull request for repository "publish-web" at layer 2 (Fix auth) (https://github.com/acme/publish-web/pull/12) is closed without merge and cannot receive new commits.`
	if rendered.Summary != want {
		t.Fatalf("summary = %q; want %q", rendered.Summary, want)
	}
	if strings.Contains(rendered.Summary, rendered.Diagnostics) {
		t.Fatalf("summary leaks raw diagnostics: %q", rendered.Summary)
	}
	if rendered.Class != ClassNeedsAction {
		t.Fatalf("class = %q; want needs_action", rendered.Class)
	}
	if rendered.Remediation == nil ||
		rendered.Remediation.Hint != "Reopen the closed pull request to restore it on GitHub, or recreate it as a fresh pull request for the same layer branch." {
		t.Fatalf("remediation = %#v; want the reopen-or-recreate hint", rendered.Remediation)
	}
	if len(rendered.Remediation.Actions) != 2 ||
		rendered.Remediation.Actions[0] != "reopen-pull-request" ||
		rendered.Remediation.Actions[1] != "recreate-pull-request" {
		t.Fatalf("publish_stack_pull_request_closed must reference reopen then recreate: %#v", rendered.Remediation)
	}
	if rendered.Context == nil || len(rendered.Context.Repositories) != 1 {
		t.Fatalf("repositories block not carried: %#v", rendered.Context)
	}
	repo := rendered.Context.Repositories[0]
	if repo.LayerPosition != 2 || repo.LayerTitle != "Fix auth" ||
		repo.PullRequestURL != "https://github.com/acme/publish-web/pull/12" {
		t.Fatalf("repositories block lost the stack fields: %#v", repo)
	}
}

// TestRenderRecordPullRequestResolutionCodesNameRepositoryLayerAndPR pins
// the closed-PR resolution family: reopen-failed, head-branch-missing, and
// recreate-failed records render layer-aware summaries naming the
// repository, layer, and pull request, with the pinned remediation and
// action lists.
func TestRenderRecordPullRequestResolutionCodesNameRepositoryLayerAndPR(t *testing.T) {
	cases := []struct {
		code     Code
		summary  string
		hint     string
		actions  []string
		diagRecv bool
	}{
		{
			code:    PublishReopenFailed,
			summary: `Reopening the stack pull request for repository "publish-web" at layer 2 (Fix auth) (https://github.com/acme/publish-web/pull/12) failed because the remote refused the change.`,
			hint:    "Retry reopen, or recreate the pull request as a fresh one for the same layer branch.",
			actions: []string{"reopen-pull-request", "recreate-pull-request"},
		},
		{
			code:    PublishHeadBranchMissing,
			summary: `The layer branch for repository "publish-web" at layer 2 (Fix auth) (https://github.com/acme/publish-web/pull/12) no longer exists on the remote, so the closed stack pull request cannot be reopened; Recreate will push the branch again.`,
			hint:    "Recreate the pull request; Recreate pushes the layer branch again and opens a fresh pull request for it.",
			actions: []string{"recreate-pull-request"},
		},
		{
			code:    PublishRecreateFailed,
			summary: `Creating the replacement pull request for repository "publish-web" at layer 2 (Fix auth) (https://github.com/acme/publish-web/pull/12) failed.`,
			hint:    "Check GitHub access, then retry Recreate.",
			actions: []string{"recreate-pull-request"},
		},
	}
	for _, tc := range cases {
		rendered := RenderRecord(FailureRecord{
			Code: tc.code,
			Context: &RecordContext{
				Repositories: []CodeRepository{{
					Name:           "publish-web",
					Branch:         "agentico/my-feature",
					LayerPosition:  2,
					LayerTitle:     "Fix auth",
					PullRequestURL: "https://github.com/acme/publish-web/pull/12",
				}},
			},
			Diagnostics: "raw detail",
		})
		if rendered.Summary != tc.summary {
			t.Errorf("%s: summary = %q; want %q", tc.code, rendered.Summary, tc.summary)
		}
		if strings.Contains(rendered.Summary, "raw detail") {
			t.Errorf("%s: summary leaks diagnostics: %q", tc.code, rendered.Summary)
		}
		if rendered.Class != ClassNeedsAction {
			t.Errorf("%s: class = %q; want needs_action", tc.code, rendered.Class)
		}
		if rendered.Remediation == nil || rendered.Remediation.Hint != tc.hint {
			t.Errorf("%s: remediation = %#v; want hint %q", tc.code, rendered.Remediation, tc.hint)
		}
		if len(rendered.Remediation.Actions) != len(tc.actions) {
			t.Errorf("%s: actions = %#v; want %#v", tc.code, rendered.Remediation.Actions, tc.actions)
		} else {
			for i, action := range tc.actions {
				if rendered.Remediation.Actions[i] != action {
					t.Errorf("%s: actions = %#v; want %#v", tc.code, rendered.Remediation.Actions, tc.actions)
					break
				}
			}
		}
		if rendered.Context == nil || len(rendered.Context.Repositories) != 1 {
			t.Errorf("%s: repositories block not carried: %#v", tc.code, rendered.Context)
		}
	}
}

// TestRenderRecordPublishStackMissingNamesRepository pins the missing-stack
// projection: a stored record whose repositories block carries a name
// renders a summary naming it, with the rewind-to-roadmap remediation and
// the publish action.
func TestRenderRecordPublishStackMissingNamesRepository(t *testing.T) {
	rendered := RenderRecord(FailureRecord{
		Code: PublishStackMissing,
		Context: &RecordContext{
			Repositories: []CodeRepository{{Name: "publish-api"}},
		},
	})
	want := `The run reached publish for repository "publish-api" without an approved stack of pull requests.`
	if rendered.Summary != want {
		t.Fatalf("summary = %q; want %q", rendered.Summary, want)
	}
	if rendered.Class != ClassNeedsAction {
		t.Fatalf("class = %q; want needs_action", rendered.Class)
	}
	if rendered.Remediation == nil ||
		rendered.Remediation.Hint != "Rewind to the roadmap phase and approve a valid Pull Requests table." {
		t.Fatalf("remediation = %#v; want the rewind-to-roadmap hint", rendered.Remediation)
	}
	if len(rendered.Remediation.Actions) != 1 || rendered.Remediation.Actions[0] != "publish" {
		t.Fatalf("publish_stack_missing must reference the publish action: %#v", rendered.Remediation)
	}
}

// TestPublishSummariesNameLayerOnlyWhenPresent pins the layer projection: a
// summary rendered with a layer parameter names it (with and without a layer
// title), and one rendered without stays byte-identical to the pre-stack
// text.
func TestPublishSummariesNameLayerOnlyWhenPresent(t *testing.T) {
	base := PublishRepoParams{Repo: "publish-web", Branch: "agentico/my-feature", RemoteOnlyCommits: 3}
	layered := base
	layered.LayerPosition = 2
	layered.LayerTitle = "Fix auth"
	untitled := base
	untitled.LayerPosition = 2
	cases := []struct {
		code            Code
		plain           string
		withLayer       string
		withLayerNoName string
	}{
		{
			code:            PublishRemoteDiverged,
			plain:           `The pull-request branch for "publish-web" contains 3 remote commits that are not in this workspace.`,
			withLayer:       `The pull-request branch for "publish-web" at layer 2 (Fix auth) contains 3 remote commits that are not in this workspace.`,
			withLayerNoName: `The pull-request branch for "publish-web" at layer 2 contains 3 remote commits that are not in this workspace.`,
		},
		{
			code:            PublishRemoteChanged,
			plain:           `The pull-request branch for "publish-web" changed while Agentico was publishing.`,
			withLayer:       `The pull-request branch for "publish-web" at layer 2 (Fix auth) changed while Agentico was publishing.`,
			withLayerNoName: `The pull-request branch for "publish-web" at layer 2 changed while Agentico was publishing.`,
		},
		{
			code:            PublishPullRequestFailed,
			plain:           `Creating the pull request for repository "publish-web" failed.`,
			withLayer:       `Creating the pull request for repository "publish-web" at layer 2 (Fix auth) failed.`,
			withLayerNoName: `Creating the pull request for repository "publish-web" at layer 2 failed.`,
		},
		{
			code:            PublishDescriptionFailed,
			plain:           `Generating the pull-request description for repository "publish-web" failed.`,
			withLayer:       `Generating the pull-request description for repository "publish-web" at layer 2 (Fix auth) failed.`,
			withLayerNoName: `Generating the pull-request description for repository "publish-web" at layer 2 failed.`,
		},
		{
			code:            PublishPushFailed,
			plain:           `Publishing repository "publish-web" (branch "agentico/my-feature") failed.`,
			withLayer:       `Publishing repository "publish-web" at layer 2 (Fix auth) (branch "agentico/my-feature") failed.`,
			withLayerNoName: `Publishing repository "publish-web" at layer 2 (branch "agentico/my-feature") failed.`,
		},
		{
			code:            PublishStateWriteFailed,
			plain:           `Recording the publish state for repository "publish-web" failed after the remote change succeeded.`,
			withLayer:       `Recording the publish state for repository "publish-web" at layer 2 (Fix auth) failed after the remote change succeeded.`,
			withLayerNoName: `Recording the publish state for repository "publish-web" at layer 2 failed after the remote change succeeded.`,
		},
	}
	for _, tc := range cases {
		if rendered := New(tc.code, WithParams(base)); rendered.Summary != tc.plain {
			t.Errorf("%s: summary without a layer = %q; want %q", tc.code, rendered.Summary, tc.plain)
		}
		if rendered := New(tc.code, WithParams(layered)); rendered.Summary != tc.withLayer {
			t.Errorf("%s: summary with a layer = %q; want %q", tc.code, rendered.Summary, tc.withLayer)
		}
		if rendered := New(tc.code, WithParams(untitled)); rendered.Summary != tc.withLayerNoName {
			t.Errorf("%s: summary with an untitled layer = %q; want %q", tc.code, rendered.Summary, tc.withLayerNoName)
		}
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
// repository publish-failure record: the rebase_target, remote_only_commits,
// layer, and pull-request-url block fields survive both marshal cycles
// unchanged.
func TestPublishRecordRoundTripsYAMLAndJSON(t *testing.T) {
	record := FailureRecord{
		Code: PublishStackPullRequestClosed,
		Context: &RecordContext{
			Repositories: []CodeRepository{{
				Name:              "publish-web",
				Branch:            "agentico/my-feature",
				RebaseTarget:      "main",
				RemoteOnlyCommits: 2,
				LayerPosition:     2,
				LayerTitle:        "Fix auth",
				PullRequestURL:    "https://github.com/acme/publish-web/pull/12",
			}},
		},
		Diagnostics: "pull request state: closed",
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
	for _, want := range []string{
		"rebase_target: main",
		"remote_only_commits: 2",
		"layer_position: 2",
		"layer_title: Fix auth",
		"pull_request_url: https://github.com/acme/publish-web/pull/12",
	} {
		if !strings.Contains(string(yamlBytes), want) {
			t.Fatalf("YAML does not carry %q:\n%s", want, yamlBytes)
		}
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

// TestPublishRecordLoadsLegacyRepositoriesBlock pins backward-compatible
// loading: records stored before the stack-layer fields existed unmarshal
// with a zero layer position, title, and pull-request URL, and render the
// pre-stack summary text.
func TestPublishRecordLoadsLegacyRepositoriesBlock(t *testing.T) {
	legacy := `code: publish_remote_diverged
context:
  repositories:
  - name: publish-web
    branch: agentico/my-feature
    remote_only_commits: 3
`
	var record FailureRecord
	if err := yaml.Unmarshal([]byte(legacy), &record); err != nil {
		t.Fatal(err)
	}
	repo := record.Context.Repositories[0]
	if repo.LayerPosition != 0 || repo.LayerTitle != "" || repo.PullRequestURL != "" {
		t.Fatalf("legacy record gained stack fields: %#v", repo)
	}
	rendered := RenderRecord(record)
	want := `The pull-request branch for "publish-web" contains 3 remote commits that are not in this workspace.`
	if rendered.Summary != want {
		t.Fatalf("summary = %q; want %q", rendered.Summary, want)
	}
}

// TestFprintRendersPublishRepositoryFields pins the CLI shape: the rebase
// target and remote-only commit count render as key-value lines under the
// repository line when present.
func TestFprintRendersPublishRepositoryFields(t *testing.T) {
	rendered := New(
		PublishStackPullRequestClosed,
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
