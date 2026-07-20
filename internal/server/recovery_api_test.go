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

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// recoveryLogMutationTarget serves a recovery scan with one item whose log
// path points at a real temp file, so the log-read endpoint can be exercised
// end to end.
type recoveryLogMutationTarget struct {
	MutationTarget
	items []ports.RecoveryItem
}

func (t *recoveryLogMutationTarget) ScanRecovery(_ context.Context) ([]ports.RecoveryItem, error) {
	return t.items, nil
}

func (recoveryLogMutationTarget) ExecuteRecovery(_ context.Context, _ []ports.RecoveryItem, _ map[string]ports.RecoveryAction) (RecoveryActionResponse, error) {
	return RecoveryActionResponse{Result: "recovered"}, nil
}

func writeRecoveryLogFixture(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "session.log")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write log fixture: %v", err)
	}
	return path
}

func TestRecoveryLogReadReturnsBoundedRedactedText(t *testing.T) {
	t.Parallel()
	logPath := writeRecoveryLogFixture(t, "line one\nline two\nline three\n")
	target := &recoveryLogMutationTarget{
		items: []ports.RecoveryItem{
			{
				PIDFile:      ports.PIDFile{FeatureID: fixtureFeatureID, RepoName: "repo-a", LogPath: logPath, Phase: "implement"},
				ProcessAlive: true,
				Feature:      &feature.Feature{ID: fixtureFeatureID, Name: "Fixture"},
				RepoName:     "repo-a",
			},
		},
	}
	handler := NewHandler(HandlerOptions{
		Mutations:             target,
		AuthToken:             testAuthToken,
		DisableHostValidation: true,
	})

	// Scan to mint a snapshot id.
	scan := authedGet(handler, apiPathRecovery)
	if scan.Code != http.StatusOK {
		t.Fatalf("scan status = %d body=%s", scan.Code, scan.Body.String())
	}
	var snap RecoverySnapshotResponse
	if err := json.NewDecoder(scan.Result().Body).Decode(&snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if len(snap.Items) != 1 || !snap.Items[0].LogAvailable {
		t.Fatalf("snapshot items = %+v; want one with log_available true", snap.Items)
	}
	itemKey := snap.Items[0].Key

	// Read the bounded log.
	w := authedGet(handler, apiPathRecoveryLogs+"?snapshot_id="+snap.SnapshotID+"&key="+itemKey)
	if w.Code != http.StatusOK {
		t.Fatalf("log read status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	var resp TextContentResponse
	if err := json.NewDecoder(w.Result().Body).Decode(&resp); err != nil {
		t.Fatalf("decode log: %v", err)
	}
	if resp.ID != itemKey {
		t.Fatalf("id = %q; want %q", resp.ID, itemKey)
	}
	if resp.Text == "" || !contains(resp.Text, "line one") {
		t.Fatalf("text = %q; want bounded log content", resp.Text)
	}
}

func TestRecoveryLogReadRejectsStaleSnapshot(t *testing.T) {
	t.Parallel()
	logPath := writeRecoveryLogFixture(t, "content")
	target := &recoveryLogMutationTarget{
		items: []ports.RecoveryItem{
			{PIDFile: ports.PIDFile{FeatureID: fixtureFeatureID, RepoName: "repo-a", LogPath: logPath}, RepoName: "repo-a"},
		},
	}
	handler := NewHandler(HandlerOptions{
		Mutations:             target,
		AuthToken:             testAuthToken,
		DisableHostValidation: true,
	})
	w := authedGet(handler, apiPathRecoveryLogs+"?snapshot_id=stale-rev&key="+fixtureFeatureID+":repo-a")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want 404 for stale snapshot", w.Code)
	}
}

func TestRecoveryLogReadRejectsCrossItemAccess(t *testing.T) {
	t.Parallel()
	logPath := writeRecoveryLogFixture(t, "content")
	target := &recoveryLogMutationTarget{
		items: []ports.RecoveryItem{
			{PIDFile: ports.PIDFile{FeatureID: fixtureFeatureID, RepoName: "repo-a", LogPath: logPath}, RepoName: "repo-a"},
		},
	}
	handler := NewHandler(HandlerOptions{
		Mutations:             target,
		AuthToken:             testAuthToken,
		DisableHostValidation: true,
	})
	scan := authedGet(handler, apiPathRecovery)
	var snap RecoverySnapshotResponse
	if err := json.NewDecoder(scan.Result().Body).Decode(&snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	// A key that is well-formed but not present in this snapshot.
	w := authedGet(handler, apiPathRecoveryLogs+"?snapshot_id="+snap.SnapshotID+"&key="+fixtureFeatureID+":other-repo")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want 404 for cross-item access", w.Code)
	}
}

func TestRecoveryLogReadRejectsTraversalKey(t *testing.T) {
	t.Parallel()
	handler := NewHandler(HandlerOptions{
		Mutations:             &recoveryLogMutationTarget{},
		AuthToken:             testAuthToken,
		DisableHostValidation: true,
	})
	w := authedGet(handler, apiPathRecoveryLogs+"?snapshot_id=any&key=..%2Fetc%2Fpasswd")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400 for traversal key", w.Code)
	}
}

func TestRecoveryLogReadRejectsMissingParams(t *testing.T) {
	t.Parallel()
	handler := NewHandler(HandlerOptions{
		Mutations:             &recoveryLogMutationTarget{},
		AuthToken:             testAuthToken,
		DisableHostValidation: true,
	})
	w := authedGet(handler, apiPathRecoveryLogs)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400 for missing params", w.Code)
	}
}

func TestRecoveryItemDTOOffersOnlyResumeAndKill(t *testing.T) {
	t.Parallel()
	item := ports.RecoveryItem{
		PIDFile:  ports.PIDFile{FeatureID: fixtureFeatureID, RepoName: "repo-a"},
		RepoName: "repo-a",
	}
	dto := recoveryItemDTO(item)
	if dto.DefaultAction != actionResume {
		t.Fatalf("default action = %q; want %q", dto.DefaultAction, actionResume)
	}
	if len(dto.AllowedActions) != 2 || dto.AllowedActions[0] != actionResume || dto.AllowedActions[1] != recoveryActionKill {
		t.Fatalf("allowed actions = %v; want [resume kill]", dto.AllowedActions)
	}
}

func TestDecodeRecoveryActionsRejectsLegacySkip(t *testing.T) {
	t.Parallel()
	item := ports.RecoveryItem{
		PIDFile:  ports.PIDFile{FeatureID: fixtureFeatureID, RepoName: "repo-a"},
		RepoName: "repo-a",
	}
	key := ports.RecoveryActionKey(fixtureFeatureID, "repo-a")
	if _, err := decodeRecoveryActions([]ports.RecoveryItem{item}, map[string]string{key: recoveryActionSkip}); err == nil {
		t.Fatal("decode legacy skip action succeeded; want fail-closed rejection")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
