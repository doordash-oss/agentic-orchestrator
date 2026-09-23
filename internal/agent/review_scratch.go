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
	"sync"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// PruneStaleReviewScratch reclaims live-run review cache and temp roots that
// earlier server runs left behind. busyIDs names features whose sessions from
// a previous server may still be alive; their state is left untouched. Reviews
// in this process are protected by the scratch registry.
func PruneStaleReviewScratch(fm *feature.Manager, busyIDs []string) {
	busy := make(map[string]bool, len(busyIDs))
	for _, id := range busyIDs {
		busy[id] = true
	}
	features, _ := fm.List()
	for _, f := range features {
		if busy[f.ID] {
			continue
		}
		if err := PruneLiveRunReviewScratch(filepath.Join(fm.Store.BaseDir, f.ID)); err != nil {
			log.Printf("feature %s: pruning live-run review scratch: %v", f.ID, err)
		}
	}
}

// liveRunScratchRegistry serializes scratch pruning with live-run reviews
// starting or resuming in the same helper directory.
var liveRunScratchRegistry = struct {
	mu      sync.Mutex
	inUse   map[string]int
	pruning map[string]chan struct{}
}{inUse: map[string]int{}, pruning: map[string]chan struct{}{}}

func scratchRegistryKey(helperDir string) string {
	if resolved, err := filepath.EvalSymlinks(helperDir); err == nil {
		return resolved
	}
	return filepath.Clean(helperDir)
}

// acquireLiveRunScratch marks an existing helperDir in use, waiting for any
// prune of it to finish first. It returns the key to pass to release.
func acquireLiveRunScratch(helperDir string) string {
	key := scratchRegistryKey(helperDir)
	r := &liveRunScratchRegistry
	for {
		r.mu.Lock()
		done, pruning := r.pruning[key]
		if !pruning {
			r.inUse[key]++
			r.mu.Unlock()
			return key
		}
		r.mu.Unlock()
		<-done
	}
}

func releaseLiveRunScratch(key string) {
	r := &liveRunScratchRegistry
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.inUse[key]--; r.inUse[key] <= 0 {
		delete(r.inUse, key)
	}
}

// pruneIfUnused removes helperDir's disposable roots unless a review holds it.
func pruneIfUnused(helperDir string) error {
	finish, ok := claimScratchPrune(helperDir)
	if !ok {
		return nil
	}
	defer finish()
	return newLiveRunReviewScratch(helperDir).removeDisposableRoots()
}

// claimScratchPrune reserves helperDir for pruning so reviews starting there
// wait until finish is called. It fails when a review holds the directory.
func claimScratchPrune(helperDir string) (finish func(), ok bool) {
	key := scratchRegistryKey(helperDir)
	r := &liveRunScratchRegistry
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.inUse[key] > 0 || r.pruning[key] != nil {
		return nil, false
	}
	done := make(chan struct{})
	r.pruning[key] = done
	return func() {
		r.mu.Lock()
		delete(r.pruning, key)
		r.mu.Unlock()
		close(done)
	}, true
}

// PruneLiveRunReviewScratch deletes cache and temp roots that live-run reviews
// left under dir, e.g. from before scratch was released or from a crash.
// Helper directories held by a running review are skipped.
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
		if err := pruneIfUnused(path); err != nil {
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
