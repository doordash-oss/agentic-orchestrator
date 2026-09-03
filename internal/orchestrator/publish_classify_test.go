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
// with the repositories block naming the repository, its branch, and the
// rebase target or remote-only commit count where known, with the raw error
// as diagnostics.
func TestPublishFailureRecordClassifiesEveryFailureSite(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantCode    errcat.Code
		wantBranch  string
		checkRecord func(t *testing.T, repo errcat.CodeRepository)
	}{
		{
			name:       "pull-rebase conflict",
			err:        &PublishConflictError{RepoName: "web", Branch: "agentico/f", RebaseTarget: "main"},
			wantCode:   errcat.PublishRebaseConflict,
			wantBranch: "agentico/f",
			checkRecord: func(t *testing.T, repo errcat.CodeRepository) {
				if repo.RebaseTarget != "main" {
					t.Errorf("rebase target = %q, want main", repo.RebaseTarget)
				}
			},
		},
		{
			name:       "rewritten-push diverged",
			err:        &PublishRemoteDivergedError{RepoName: "web", Branch: "agentico/f", RemoteOnlyCommits: 3},
			wantCode:   errcat.PublishRemoteDiverged,
			wantBranch: "agentico/f",
			checkRecord: func(t *testing.T, repo errcat.CodeRepository) {
				if repo.RemoteOnlyCommits != 3 {
					t.Errorf("remote-only commits = %d, want 3", repo.RemoteOnlyCommits)
				}
			},
		},
		{
			name:       "rewritten-push changed",
			err:        &PublishRemoteChangedError{RepoName: "web", Branch: "agentico/f"},
			wantCode:   errcat.PublishRemoteChanged,
			wantBranch: "agentico/f",
		},
		{
			name:       "closed pull request",
			err:        &PublishPRClosedError{RepoName: "web", PRURL: "https://github.example/org/web/pull/9", State: "merged"},
			wantCode:   errcat.PublishPullRequestClosed,
			wantBranch: "agentico/f",
			checkRecord: func(t *testing.T, repo errcat.CodeRepository) {
				_ = repo
			},
		},
		{
			name:       "pull-request creation",
			err:        &PublishPRCreateError{RepoName: "web", Err: errors.New("POST /repos/org/web/pulls: 502 Bad Gateway")},
			wantCode:   errcat.PublishPullRequestFailed,
			wantBranch: "agentico/f",
		},
		{
			name:       "description generation",
			err:        &PublishDescriptionError{RepoName: "web", Err: errors.New("generating description: model unavailable")},
			wantCode:   errcat.PublishDescriptionFailed,
			wantBranch: "agentico/f",
		},
		{
			name:       "commit failure",
			err:        fmt.Errorf("commit failed: exit status 1"),
			wantCode:   errcat.PublishPushFailed,
			wantBranch: "agentico/f",
		},
		{
			name:       "non-conflict pull-rebase failure",
			err:        errors.New("pull-rebase failed: fetch origin: dial tcp: refused"),
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

// TestPublishFailureRecordClosedPRCarriesURLInDiagnostics pins the closed-PR
// contract: the pull-request URL travels in diagnostics because the row
// already renders the link.
func TestPublishFailureRecordClosedPRCarriesURLInDiagnostics(t *testing.T) {
	closed := &PublishPRClosedError{RepoName: "web", PRURL: "https://github.example/org/web/pull/9", State: "merged"}
	record := publishFailureRecord("web", "agentico/f", closed)
	if record.Code != errcat.PublishPullRequestClosed {
		t.Fatalf("code = %q, want publish_pull_request_closed", record.Code)
	}
	if !strings.Contains(record.Diagnostics, "https://github.example/org/web/pull/9") {
		t.Errorf("diagnostics = %q, want the pull-request URL", record.Diagnostics)
	}
}

// TestPublishConflictRecordCoversTheConflictFamily pins the server mapper's
// shared classification: exactly the conflict, diverged, and changed errors
// produce a record, with the same code and repository block the stored
// record carries.
func TestPublishConflictRecordCoversTheConflictFamily(t *testing.T) {
	conflict := &PublishConflictError{RepoName: "web", Branch: "agentico/f", RebaseTarget: "main"}
	diverged := &PublishRemoteDivergedError{RepoName: "web", Branch: "agentico/f", RemoteOnlyCommits: 2}
	changed := &PublishRemoteChangedError{RepoName: "web", Branch: "agentico/f"}
	for _, tc := range []struct {
		err      error
		wantCode errcat.Code
	}{
		{conflict, errcat.PublishRebaseConflict},
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
			record.Context.Repositories[0].RebaseTarget != stored.Context.Repositories[0].RebaseTarget ||
			record.Context.Repositories[0].RemoteOnlyCommits != stored.Context.Repositories[0].RemoteOnlyCommits {
			t.Errorf("%v: envelope record %+v disagrees with stored record %+v", tc.err, record, stored)
		}
	}
	if _, ok := PublishConflictRecord(errors.New("not a publish conflict")); ok {
		t.Error("PublishConflictRecord = ok for a generic error, want false")
	}
	if _, ok := PublishConflictRecord(&PublishPRCreateError{RepoName: "web", Err: errors.New("502")}); ok {
		t.Error("PublishConflictRecord = ok for a PR creation failure, want false")
	}
}

// TestPublishDispatchErrorPreservesItsChain pins the never-terminal marker:
// the wrapper keeps the underlying error reachable through errors.Is/As so
// conflict routing and sentinel checks still work.
func TestPublishDispatchErrorPreservesItsChain(t *testing.T) {
	inner := &PublishConflictError{RepoName: "web", Branch: "agentico/f", RebaseTarget: "main"}
	wrapped := &PublishDispatchError{Err: inner}
	if wrapped.Error() != inner.Error() {
		t.Errorf("Error() = %q, want the underlying text %q", wrapped.Error(), inner.Error())
	}
	var conflict *PublishConflictError
	if !errors.As(wrapped, &conflict) {
		t.Fatal("errors.As does not find the conflict through the wrapper")
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
		Err: &PublishPRCreateError{RepoName: "r1", Err: errors.New("502 Bad Gateway")},
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
