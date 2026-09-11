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

package orchestrator

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// TestPublishFailureRecordClassifiesEveryFailureSite pins the one-code-per-
// remediation contract: every publish failure site classifies into its code
// with the repositories block naming the repository, its branch, and — for
// layer-scoped failures — the stack layer position and title, with the raw
// error as diagnostics.
func TestPublishFailureRecordClassifiesEveryFailureSite(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantCode    errcat.Code
		wantBranch  string
		checkRecord func(t *testing.T, repo errcat.CodeRepository)
	}{
		{
			name: "rewritten-push diverged",
			err: &PublishRemoteDivergedError{RepoName: "web", Branch: "agentico/f", RemoteOnlyCommits: 3, LayerPosition: 2, LayerTitle: "Fix auth"},
			wantCode:    errcat.PublishRemoteDiverged,
			wantBranch:  "agentico/f",
			checkRecord: layerCheck(2, "Fix auth", remoteOnlyCheck(3)),
		},
		{
			name:        "rewritten-push changed",
			err:         &PublishRemoteChangedError{RepoName: "web", Branch: "agentico/f", LayerPosition: 2, LayerTitle: "Fix auth"},
			wantCode:    errcat.PublishRemoteChanged,
			wantBranch:  "agentico/f",
			checkRecord: layerCheck(2, "Fix auth", nil),
		},
		{
			name: "stack pull request closed",
			err: &PublishStackClosedError{RepoName: "web", Branch: "agentico/f", LayerPosition: 1, LayerTitle: "Foundation", PRURL: "https://github.example/org/web/pull/9", State: "closed"},
			wantCode:   errcat.PublishStackPullRequestClosed,
			wantBranch: "agentico/f",
			checkRecord: func(t *testing.T, repo errcat.CodeRepository) {
				if repo.LayerPosition != 1 || repo.LayerTitle != "Foundation" {
					t.Errorf("layer = %d (%q), want 1 (Foundation)", repo.LayerPosition, repo.LayerTitle)
				}
				if repo.PullRequestURL != "https://github.example/org/web/pull/9" {
					t.Errorf("pull request URL = %q, want the closed pull request's link", repo.PullRequestURL)
				}
			},
		},
		{
			name:       "run without an approved stack",
			err:        &PublishStackMissingError{RepoName: "web", Branch: "agentico/f"},
			wantCode:   errcat.PublishStackMissing,
			wantBranch: "agentico/f",
		},
		{
			name:        "pull-request creation",
			err:         &PublishPRCreateError{RepoName: "web", LayerPosition: 3, LayerTitle: "Top", Err: errors.New("POST /repos/org/web/pulls: 502 Bad Gateway")},
			wantCode:    errcat.PublishPullRequestFailed,
			wantBranch:  "agentico/f",
			checkRecord: layerCheck(3, "Top", nil),
		},
		{
			name:        "description generation",
			err:         &PublishDescriptionError{RepoName: "web", LayerPosition: 3, LayerTitle: "Top", Err: errors.New("generating description: model unavailable")},
			wantCode:    errcat.PublishDescriptionFailed,
			wantBranch:  "agentico/f",
			checkRecord: layerCheck(3, "Top", nil),
		},
		{
			name:        "layer push failure",
			err:         &PublishPushError{RepoName: "web", Branch: "agentico/f-2", LayerPosition: 2, LayerTitle: "Fix auth", Err: errors.New("remote rejected")},
			wantCode:    errcat.PublishPushFailed,
			wantBranch:  "agentico/f-2",
			checkRecord: layerCheck(2, "Fix auth", nil),
		},
		{
			name:       "commit failure",
			err:        fmt.Errorf("commit failed: exit status 1"),
			wantCode:   errcat.PublishPushFailed,
			wantBranch: "agentico/f",
		},
		{
			name:       "push failure",
			err:        errors.New("push failed: non-fast-forward"),
			wantCode:   errcat.PublishPushFailed,
			wantBranch: "agentico/f",
		},
		{
			name:       "artifact scrub failure",
			err:        errors.New("remove untracked final review artifact agentico-outcome.md: permission denied"),
			wantCode:   errcat.PublishPushFailed,
			wantBranch: "agentico/f",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			record := publishFailureRecord("web", "agentico/f", tc.err)
			if record.Code != tc.wantCode {
				t.Fatalf("code = %q, want %q", record.Code, tc.wantCode)
			}
			if record.Context == nil || len(record.Context.Repositories) != 1 {
				t.Fatalf("context = %+v, want exactly one repository", record.Context)
			}
			repo := record.Context.Repositories[0]
			if repo.Name != "web" {
				t.Errorf("repository name = %q, want web", repo.Name)
			}
			if repo.Branch != tc.wantBranch {
				t.Errorf("repository branch = %q, want %q", repo.Branch, tc.wantBranch)
			}
			if record.Diagnostics != tc.err.Error() {
				t.Errorf("diagnostics = %q, want the raw error %q", record.Diagnostics, tc.err.Error())
			}
			if tc.checkRecord != nil {
				tc.checkRecord(t, repo)
			}
			rendered := errcat.RenderRecord(record)
			if rendered.Class != errcat.ClassNeedsAction {
				t.Errorf("rendered class = %q, want needs_action", rendered.Class)
			}
		})
	}
}

func remoteOnlyCheck(want int) func(t *testing.T, repo errcat.CodeRepository) {
	return func(t *testing.T, repo errcat.CodeRepository) {
		if repo.RemoteOnlyCommits != want {
			t.Errorf("remote-only commits = %d, want %d", repo.RemoteOnlyCommits, want)
		}
	}
}

func layerCheck(wantPosition int, wantTitle string, extra func(t *testing.T, repo errcat.CodeRepository)) func(t *testing.T, repo errcat.CodeRepository) {
	return func(t *testing.T, repo errcat.CodeRepository) {
		if repo.LayerPosition != wantPosition || repo.LayerTitle != wantTitle {
			t.Errorf("layer = %d (%q), want %d (%q)", repo.LayerPosition, repo.LayerTitle, wantPosition, wantTitle)
		}
		if extra != nil {
			extra(t, repo)
		}
	}
}

// TestPublishFailureRecordStackClosedCarriesLayerAndURL pins the closed-stack
// contract: the repositories block names the layer and the closed pull
// request's URL, and the URL also travels in diagnostics.
func TestPublishFailureRecordStackClosedCarriesLayerAndURL(t *testing.T) {
	closed := &PublishStackClosedError{
		RepoName:      "web",
		Branch:        "agentico/f",
		LayerPosition: 2,
		LayerTitle:    "Fix auth",
		PRURL:         "https://github.example/org/web/pull/9",
		State:         "closed",
	}
	record := publishFailureRecord("web", "agentico/f", closed)
	if record.Code != errcat.PublishStackPullRequestClosed {
		t.Fatalf("code = %q, want publish_stack_pull_request_closed", record.Code)
	}
	repo := record.Context.Repositories[0]
	if repo.LayerPosition != 2 || repo.LayerTitle != "Fix auth" {
		t.Errorf("layer = %d (%q), want 2 (Fix auth)", repo.LayerPosition, repo.LayerTitle)
	}
	if repo.PullRequestURL != "https://github.example/org/web/pull/9" {
		t.Errorf("pull request URL = %q, want the closed pull request's link", repo.PullRequestURL)
	}
	if !strings.Contains(record.Diagnostics, "https://github.example/org/web/pull/9") {
		t.Errorf("diagnostics = %q, want the pull-request URL", record.Diagnostics)
	}
}

// TestPublishConflictRecordCoversTheConflictFamily pins the server mapper's
// shared classification: exactly the diverged and changed errors produce a
// record, with the same code and repository block the stored record carries.
func TestPublishConflictRecordCoversTheConflictFamily(t *testing.T) {
	diverged := &PublishRemoteDivergedError{RepoName: "web", Branch: "agentico/f", RemoteOnlyCommits: 2, LayerPosition: 1, LayerTitle: "Foundation"}
	changed := &PublishRemoteChangedError{RepoName: "web", Branch: "agentico/f", LayerPosition: 1, LayerTitle: "Foundation"}
	for _, tc := range []struct {
		err      error
		wantCode errcat.Code
	}{
		{diverged, errcat.PublishRemoteDiverged},
		{changed, errcat.PublishRemoteChanged},
	} {
		record, ok := PublishConflictRecord(tc.err)
		if !ok {
			t.Fatalf("%v: PublishConflictRecord = not ok, want a record", tc.err)
		}
		if record.Code != tc.wantCode {
			t.Errorf("%v: code = %q, want %q", tc.err, record.Code, tc.wantCode)
		}
		// The envelope record matches what the publish boundary stores for
		// the same error.
		stored := publishFailureRecord("web", "agentico/f", tc.err)
		if record.Code != stored.Code ||
			len(record.Context.Repositories) != 1 ||
			len(stored.Context.Repositories) != 1 ||
			record.Context.Repositories[0].Name != stored.Context.Repositories[0].Name ||
			record.Context.Repositories[0].Branch != stored.Context.Repositories[0].Branch ||
			record.Context.Repositories[0].RemoteOnlyCommits != stored.Context.Repositories[0].RemoteOnlyCommits ||
			record.Context.Repositories[0].LayerPosition != stored.Context.Repositories[0].LayerPosition ||
			record.Context.Repositories[0].LayerTitle != stored.Context.Repositories[0].LayerTitle {
			t.Errorf("%v: envelope record %+v disagrees with stored record %+v", tc.err, record, stored)
		}
	}
	for _, err := range []error{
		errors.New("not a publish conflict"),
		&PublishPRCreateError{RepoName: "web", Err: errors.New("502")},
		&PublishStackClosedError{RepoName: "web", LayerPosition: 1, PRURL: "https://github.example/org/web/pull/9", State: "closed"},
	} {
		if _, ok := PublishConflictRecord(err); ok {
			t.Errorf("PublishConflictRecord = ok for %v, want false", err)
		}
	}
}

// TestPublishDispatchErrorPreservesItsChain pins the never-terminal marker:
// the wrapper keeps the underlying error reachable through errors.Is/As so
// conflict routing and sentinel checks still work.
func TestPublishDispatchErrorPreservesItsChain(t *testing.T) {
	inner := &PublishRemoteDivergedError{RepoName: "web", Branch: "agentico/f", RemoteOnlyCommits: 2, LayerPosition: 1, LayerTitle: "Foundation"}
	wrapped := &PublishDispatchError{Err: inner}
	if wrapped.Error() != inner.Error() {
		t.Errorf("Error() = %q, want the underlying text %q", wrapped.Error(), inner.Error())
	}
	var diverged *PublishRemoteDivergedError
	if !errors.As(wrapped, &diverged) {
		t.Fatal("errors.As does not find the diverged error through the wrapper")
	}
	generic := errors.New("publish exploded")
	wrapped = &PublishDispatchError{Err: generic}
	if !errors.Is(wrapped, generic) {
		t.Fatal("errors.Is does not find the sentinel through the wrapper")
	}
}

// TestSurfaceDispatchCompletionErrorSkipsPublishFailures pins the completion
// dispatch contract: a publish failure never marks the run Failed and never
// emits FeatureFailed — the repository's stored record owns the condition.
func TestSurfaceDispatchCompletionErrorSkipsPublishFailures(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-surface",
		Status:       feature.StatusCodeReady,
		CurrentPhase: feature.PhasePublish,
	}
	lc := mocks.NewMockFeatureLifecycle()
	lc.GetFn = func(id string) (*feature.Feature, error) { return f, nil }
	fs := mocks.NewMockFeatureStore()
	fs.LoadFn = func(id string) (*feature.Feature, error) { return f, nil }
	o := New(Deps{Lifecycle: lc, Store: fs}, Hooks{})

	o.surfaceDispatchCompletionError("feat-surface", &PublishDispatchError{
		Err: &PublishPRCreateError{RepoName: "r1", LayerPosition: 2, LayerTitle: "Fix auth", Err: errors.New("502 Bad Gateway")},
	})

	for _, call := range lc.Calls {
		if call.Method == "MarkFailed" {
			t.Fatalf("MarkFailed called for a publish failure: %+v; publish failures are never terminal", call)
		}
	}
	for {
		select {
		case ev := <-o.Events():
			if ev.Type == ports.FeatureFailed {
				t.Fatalf("FeatureFailed emitted for a publish failure: %+v", ev)
			}
		default:
			return
		}
	}
}
