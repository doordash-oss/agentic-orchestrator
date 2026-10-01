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

package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReviewFailureNeverAcceptsWireDiagnostics(t *testing.T) {
	var result ResultMessage
	if err := json.Unmarshal([]byte(`{"type":"result","subtype":"error","ReviewFailure":"transport","review_failure":"SECRET"}`), &result); err != nil {
		t.Fatal(err)
	}
	if result.ReviewFailure != "" {
		t.Fatal("provider JSON injected a diagnostic")
	}
	if strings.Contains(ReviewFailure("SECRET").Reason(), "SECRET") {
		t.Fatal("unknown diagnostic leaked")
	}
}

func TestReviewFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		message string
		want    ReviewFailure
	}{
		{"401 unauthorized SECRET", ReviewFailureAuth},
		{"model not supported SECRET", ReviewFailureModel},
		{"transport disconnected SECRET", ReviewFailureTransport},
		{"429 too many requests SECRET", ReviewFailureRateLimit},
		{"503 service unavailable SECRET", ReviewFailureServer},
		{"maximum context is 15000 tokens", ReviewFailureProvider},
	} {
		if got := ClassifyReviewFailure(tc.message); got != tc.want {
			t.Errorf("classification = %s, want %s", got, tc.want)
		}
	}
}
