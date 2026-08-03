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
	"fmt"
	"os"
	"path/filepath"
)

const addressedReviewFeedbackIDsFilename = "addressed-ids.json"

// LoadAddressedReviewFeedbackIDs reads the durable parent- and repo-scoped
// set of GitHub comment IDs already handled by completed review-feedback
// children. The ledger intentionally lives outside runs so rewinds and run
// rollover do not make addressed comments visible again.
func (s *Store) LoadAddressedReviewFeedbackIDs(parentID, repoName string) (map[int]bool, error) {
	path := filepath.Join(s.BaseDir, parentID, "review-feedback", repoName, addressedReviewFeedbackIDsFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[int]bool), nil
		}
		return nil, fmt.Errorf("read addressed review-feedback IDs for repo %q: %w", repoName, err)
	}

	var ids []int
	if err := json.Unmarshal(data, &ids); err != nil {
		return nil, fmt.Errorf("parse addressed review-feedback IDs for repo %q: %w", repoName, err)
	}
	addressed := make(map[int]bool, len(ids))
	for _, id := range ids {
		addressed[id] = true
	}
	return addressed, nil
}
