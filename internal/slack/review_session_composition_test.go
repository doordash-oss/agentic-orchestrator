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

package slack

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	serverruntime "github.com/doordash-oss/agentic-orchestrator/internal/server"
)

func TestSlackReviewGatePersistedRevisionMatchesRealReviewSession(t *testing.T) {
	settings := defaultTestSettings(testToken, testRecipients()[1])
	settings.Categories.Progress = false
	harness := newReviewGateHarness(t, settings)
	featureID := "F-review-session-composition"
	original := []byte("# Phase 3 plan\n\n## Tasks\n\n- Ship the first revision.\n")
	artifactPath := harness.seedReview(
		featureID,
		feature.StatusPlanNeedsReview,
		"phase-3-plan",
		"phase-plan.md",
		original,
		3,
		11,
		nil,
	)
	reviewHandler := serverruntime.NewHandler(serverruntime.HandlerOptions{
		Runtime:               serverruntime.RuntimeIdentity{StateDir: harness.stateDir},
		Features:              harness.store,
		FeatureStore:          harness.store,
		DisableHostValidation: true,
	})
	scriptReviewShareResponses(harness.server, 2)
	notifier := harness.start()

	firstCreated := requestReviewSession(
		t, reviewHandler, http.MethodPost, featureID, true,
	)
	firstRead := requestReviewSession(
		t, reviewHandler, http.MethodGet, featureID, false,
	)
	assertReviewSessionRoundTrip(t, firstCreated, firstRead, string(original))

	notifier.DomainEventTap(ports.Event{
		Type: ports.ReviewRequired, FeatureID: featureID,
	})
	firstItem := waitForPersistedReviewSession(
		t, harness.stateDir, featureID, firstCreated.SourceRevision, "#1",
	)
	assertPersistedReviewMatchesSession(t, firstItem, firstCreated, firstRead)

	revised := []byte("# Phase 3 plan\n\n## Tasks\n\n- Ship the revised artifact.\n")
	if err := os.WriteFile(artifactPath, revised, 0o644); err != nil {
		t.Fatalf("rewrite review artifact: %v", err)
	}
	secondCreated := requestReviewSession(
		t, reviewHandler, http.MethodPost, featureID, true,
	)
	secondRead := requestReviewSession(
		t, reviewHandler, http.MethodGet, featureID, false,
	)
	assertReviewSessionRoundTrip(t, secondCreated, secondRead, string(revised))
	if secondCreated.ReviewID != firstCreated.ReviewID {
		t.Errorf(
			"revised ReviewID = %q; want stable %q",
			secondCreated.ReviewID,
			firstCreated.ReviewID,
		)
	}
	if secondCreated.SourceRevision == firstCreated.SourceRevision {
		t.Fatalf(
			"revised SourceRevision = %q; want change from %q",
			secondCreated.SourceRevision,
			firstCreated.SourceRevision,
		)
	}

	notifier.DomainEventTap(ports.Event{
		Type: ports.ReviewRequired, FeatureID: featureID,
	})
	secondItem := waitForPersistedReviewSession(
		t, harness.stateDir, featureID, secondCreated.SourceRevision, "#2",
	)
	assertPersistedReviewMatchesSession(t, secondItem, secondCreated, secondRead)
	if secondItem.Identity == firstItem.Identity {
		t.Errorf(
			"revised Slack review identity = %q; want change from %q",
			secondItem.Identity,
			firstItem.Identity,
		)
	}
	if got := len(harness.server.Requests("files.getUploadURLExternal")); got != 2 {
		t.Errorf("review upload allocations = %d; want one per artifact revision", got)
	}
	if got := len(reviewThreadPosts(harness.server)); got != 2 {
		t.Errorf("review messages = %d; want one per artifact revision", got)
	}
}

func requestReviewSession(
	t *testing.T,
	handler http.Handler,
	method, featureID string,
	mutation bool,
) serverruntime.ReviewSessionResponse {
	t.Helper()
	var body *bytes.Reader
	if mutation {
		body = bytes.NewReader([]byte("{}"))
	} else {
		body = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(
		method,
		"/api/v1/features/"+featureID+"/reviews",
		body,
	)
	if mutation {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Agentico-Client", "local")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	response := recorder.Result()
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf(
			"%s review session status = %d; want %d",
			method,
			response.StatusCode,
			http.StatusOK,
		)
	}
	var session serverruntime.ReviewSessionResponse
	if err := json.NewDecoder(response.Body).Decode(&session); err != nil {
		t.Fatalf("decode %s review session: %v", method, err)
	}
	return session
}

func assertReviewSessionRoundTrip(
	t *testing.T,
	created, read serverruntime.ReviewSessionResponse,
	wantText string,
) {
	t.Helper()
	if created.ReviewID == "" ||
		created.SourceRevision == "" ||
		created.DraftRevision == "" {
		t.Fatalf("created review session lacks identity or revisions: %+v", created)
	}
	if created.SourceRevision != created.DraftRevision {
		t.Errorf(
			"created review revisions = source %q draft %q; want equal",
			created.SourceRevision,
			created.DraftRevision,
		)
	}
	if read.ReviewID != created.ReviewID ||
		read.SourceRevision != created.SourceRevision ||
		read.DraftRevision != created.DraftRevision {
		t.Errorf(
			"read review identity/revisions = %q/%q/%q; want %q/%q/%q",
			read.ReviewID,
			read.SourceRevision,
			read.DraftRevision,
			created.ReviewID,
			created.SourceRevision,
			created.DraftRevision,
		)
	}
	if created.Text != wantText || read.Text != wantText {
		t.Errorf(
			"review session text = created %q read %q; want %q",
			created.Text,
			read.Text,
			wantText,
		)
	}
}

func waitForPersistedReviewSession(
	t *testing.T,
	stateDir, featureID, sourceRevision, tag string,
) pendingInputRecord {
	t.Helper()
	var matched pendingInputRecord
	waitFor(t, 10*time.Second, func() bool {
		record, err := loadFeatureRecord(stateDir, featureID)
		if err != nil || len(record.Pending) != 1 {
			return false
		}
		candidate := record.Pending[0]
		if candidate.SourceRevision != sourceRevision ||
			candidate.Tag != tag ||
			len(candidate.MessageTS) != 1 ||
			len(candidate.FileIDs) != 1 {
			return false
		}
		matched = candidate
		return true
	})
	return matched
}

func assertPersistedReviewMatchesSession(
	t *testing.T,
	item pendingInputRecord,
	created, read serverruntime.ReviewSessionResponse,
) {
	t.Helper()
	if item.ReviewID != created.ReviewID ||
		item.ReviewID != read.ReviewID ||
		item.SourceRevision != created.SourceRevision ||
		item.SourceRevision != created.DraftRevision ||
		item.SourceRevision != read.SourceRevision ||
		item.SourceRevision != read.DraftRevision {
		t.Errorf(
			"persisted Slack review = id %q revision %q; created = %q/%q/%q; read = %q/%q/%q",
			item.ReviewID,
			item.SourceRevision,
			created.ReviewID,
			created.SourceRevision,
			created.DraftRevision,
			read.ReviewID,
			read.SourceRevision,
			read.DraftRevision,
		)
	}
}
