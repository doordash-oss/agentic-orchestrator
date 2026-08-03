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

package feature

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
)

// ReviewFeedbackOutcomeDispositionAddressed is the disposition for a comment
// that was resolved by changing code.
const ReviewFeedbackOutcomeDispositionAddressed = "addressed"

// ReviewFeedbackOutcomeDispositionDismissed is the disposition for a comment
// that was intentionally not acted on.
const ReviewFeedbackOutcomeDispositionDismissed = "dismissed"

// ReviewFeedbackOutcome is one entry in the child's outcomes artifact: the
// disposition (addressed/dismissed) and a short explanation for a single
// selected comment.
type ReviewFeedbackOutcome struct {
	ID          int    `json:"id"`
	Disposition string `json:"disposition"`
	Explanation string `json:"explanation"`
}

// ReviewFeedbackOutcomesPath returns the absolute path to the outcomes
// artifact inside the child's active run directory. The path is deterministic
// for a given feature and run number.
func ReviewFeedbackOutcomesPath(stateDir string, f *Feature) string {
	runDir := RunDirName(f.ActiveRun)
	if f.ActiveRun <= 0 {
		runDir = RunDirName(1)
	}
	return filepath.Join(stateDir, f.ID, "runs", runDir, ReviewFeedbackOutcomesFilename)
}

// LoadReviewFeedbackOutcomes reads the child's outcomes artifact and returns
// a map keyed by comment ID. A missing file, malformed JSON, or missing
// entries yield an empty or partial outcome map without error — the tail
// degrades to fallback replies rather than fail.
func LoadReviewFeedbackOutcomes(stateDir string, f *Feature) map[int]ReviewFeedbackOutcome {
	path := ReviewFeedbackOutcomesPath(stateDir, f)
	data, err := os.ReadFile(path)
	if err != nil {
		return make(map[int]ReviewFeedbackOutcome)
	}
	var entries []ReviewFeedbackOutcome
	if err := json.Unmarshal(data, &entries); err != nil {
		return make(map[int]ReviewFeedbackOutcome)
	}
	outcomes := make(map[int]ReviewFeedbackOutcome, len(entries))
	for _, e := range entries {
		if e.ID == 0 {
			continue
		}
		outcomes[e.ID] = e
	}
	return outcomes
}

// ReviewFeedbackReplyBody builds the reply body for a single comment from
// the outcomes artifact and the merge SHA. Addressed comments use
// "Addressed in `<sha>` — `<explanation>`"; dismissed comments use
// "Dismissed: `<explanation>`". When no usable outcome entry exists, the
// deterministic fallback "Addressed in `<sha>`" is returned.
func ReviewFeedbackReplyBody(outcome ReviewFeedbackOutcome, ok bool, mergeSHA string) string {
	if !ok {
		return "Addressed in `" + mergeSHA + "`"
	}
	if outcome.Disposition == ReviewFeedbackOutcomeDispositionDismissed {
		return "Dismissed: " + outcome.Explanation
	}
	if outcome.Explanation != "" {
		return "Addressed in `" + mergeSHA + "` — " + outcome.Explanation
	}
	return "Addressed in `" + mergeSHA + "`"
}

// formatCommentID is a small helper for deterministic ID-to-string conversion.
func formatCommentID(id int) string {
	return strconv.Itoa(id)
}
