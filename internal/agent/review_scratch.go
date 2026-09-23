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

package agent

import (
	"errors"
	"log"
	"os"
	"path/filepath"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// PruneIdleFeatureReviewScratch reclaims live-run review cache and temp roots
// that earlier server runs left behind. Features that may still own a review
// session, including those listed in busyIDs, are skipped.
func PruneIdleFeatureReviewScratch(fm *feature.Manager, busyIDs []string) {
	busy := make(map[string]bool, len(busyIDs))
	for _, id := range busyIDs {
		busy[id] = true
	}
	features, _ := fm.List()
	for _, f := range features {
		if busy[f.ID] || reviewScratchMayBeInUse(f) {
			continue
		}
		// Re-read so a review started since the listing keeps its scratch.
		if current, err := fm.Get(f.ID); err != nil || reviewScratchMayBeInUse(current) {
			continue
		}
		if err := PruneLiveRunReviewScratch(filepath.Join(fm.Store.BaseDir, f.ID)); err != nil {
			log.Printf("feature %s: pruning live-run review scratch: %v", f.ID, err)
		}
	}
}

func reviewScratchMayBeInUse(f *feature.Feature) bool {
	return f.Status.IsRunning() || f.Status == feature.StatusReviewing
}

// PruneLiveRunReviewScratch deletes cache and temp roots that live-run reviews
// left under dir, e.g. from before scratch was released or from a crash.
// Callers must ensure no review is running under dir.
func PruneLiveRunReviewScratch(dir string) error {
	var errs []error
	walkErr := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			if path == dir {
				return err
			}
			return nil
		}
		if !entry.IsDir() || !isLiveRunReviewHelperDir(path) {
			return nil
		}
		if err := newLiveRunReviewScratch(path).release(); err != nil {
			errs = append(errs, err)
		}
		return filepath.SkipDir
	})
	if walkErr != nil && !os.IsNotExist(walkErr) {
		errs = append(errs, walkErr)
	}
	return errors.Join(errs...)
}

// isLiveRunReviewHelperDir matches the layout prepareLiveRunReviewScratch
// and RunLiveRunReviewHelper produce.
func isLiveRunReviewHelperDir(path string) bool {
	if info, err := os.Stat(newLiveRunReviewScratch(path).EvidenceRoot); err != nil || !info.IsDir() {
		return false
	}
	info, err := os.Stat(filepath.Join(path, "review-prompt.md"))
	return err == nil && info.Mode().IsRegular()
}
