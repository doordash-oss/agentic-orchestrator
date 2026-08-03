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
	"sort"
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

// AppendAddressedReviewFeedbackIDs durably appends the given comment IDs to
// the parent- and repo-scoped ledger, merging with any existing recorded IDs
// (deduplicated). The location is created on first write. Appending the same
// IDs twice is a no-op. The tail appends incrementally per successful reply
// and recovery may re-append, so this operation must be idempotent.
func (s *Store) AppendAddressedReviewFeedbackIDs(parentID, repoName string, ids []int) error {
	dir := filepath.Join(s.BaseDir, parentID, "review-feedback", repoName)
	path := filepath.Join(dir, addressedReviewFeedbackIDsFilename)

	existing := make(map[int]bool)
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read addressed review-feedback IDs for repo %q: %w", repoName, err)
	}
	if err == nil {
		var stored []int
		if jsonErr := json.Unmarshal(data, &stored); jsonErr != nil {
			return fmt.Errorf("parse addressed review-feedback IDs for repo %q: %w", repoName, jsonErr)
		}
		for _, id := range stored {
			existing[id] = true
		}
	}

	changed := false
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if !existing[id] {
			existing[id] = true
			changed = true
		}
	}
	if !changed && len(existing) > 0 {
		return nil
	}
	if len(existing) == 0 && len(ids) == 0 {
		return nil
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create addressed review-feedback ledger dir for repo %q: %w", repoName, err)
	}

	all := make([]int, 0, len(existing))
	for id := range existing {
		all = append(all, id)
	}
	sort.Ints(all)
	encoded, err := json.Marshal(all)
	if err != nil {
		return fmt.Errorf("encode addressed review-feedback IDs for repo %q: %w", repoName, err)
	}
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		return fmt.Errorf("write addressed review-feedback IDs for repo %q: %w", repoName, err)
	}
	return nil
}
