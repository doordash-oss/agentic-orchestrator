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

package clone

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// StoreDirName is the directory (under the runtime state dir) that holds
// clone operation records. It is registered as a non-feature directory in
// the feature store so feature scans skip it.
const StoreDirName = "clone-operations"

// ListQuery is one bounded list request. Limit is clamped to
// [1, MaxListLimit]. AfterToken continues a previous page.
type ListQuery struct {
	Limit int
	After string
}

// ListResult is one list page plus a continuation token when more records
// exist. A truncated page is never proof that an operation does not exist.
type ListResult struct {
	Operations []Record
	NextToken  string
}

// store is the durable clone-operation record store. Every mutation is an
// atomic temp-file+rename write, so a crash never leaves a torn record.
type store struct {
	dir string
	mu  sync.Mutex
	// records is the in-memory mirror of the directory, keyed by
	// operation ID. The single server process owns both.
	records map[string]*Record
	now     func() time.Time
}

func newStore(dir string, now func() time.Time) (*store, error) {
	if now == nil {
		now = time.Now
	}
	s := &store{dir: dir, records: map[string]*Record{}, now: now}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create clone store dir: %w", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read clone store dir: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read clone record: %w", err)
		}
		var rec Record
		if err := json.Unmarshal(data, &rec); err != nil {
			// A malformed record is skipped, not fatal: it can never
			// gate reservations unless it parses.
			continue
		}
		if rec.ID == "" {
			continue
		}
		cp := rec
		s.records[rec.ID] = &cp
	}
	return s, nil
}

// save persists the record atomically. The caller has already set UpdatedAt.
func (s *store) save(rec *Record) error {
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("encode clone record: %w", err)
	}
	cp := *rec
	s.mu.Lock()
	defer s.mu.Unlock()
	tmp, err := os.CreateTemp(s.dir, "record-*.json.tmp")
	if err != nil {
		return fmt.Errorf("stage clone record: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write clone record: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close clone record: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("chmod clone record: %w", err)
	}
	if err := os.Rename(tmpName, s.pathLocked(rec.ID)); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("persist clone record: %w", err)
	}
	s.records[rec.ID] = &cp
	return nil
}

func (s *store) pathLocked(id string) string {
	return filepath.Join(s.dir, id+".json")
}

// get returns a copy of the record with the given ID.
func (s *store) get(id string) (Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[id]
	if !ok {
		return Record{}, false
	}
	return rec.Snapshot(), true
}

// getByIdempotencyKey returns the newest record retaining the given
// idempotency key. Retention follows the record's own resolved-at window;
// active and cleanup-pending records are always found.
func (s *store) getByIdempotencyKey(key string) (Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var best *Record
	for _, rec := range s.records {
		if rec.IdempotencyKey != key {
			continue
		}
		if best == nil || rec.CreatedAt.After(best.CreatedAt) {
			best = rec
		}
	}
	if best == nil {
		return Record{}, false
	}
	return best.Snapshot(), true
}

// reservation returns the unresolved record holding a reservation on the
// destination path, if any.
func (s *store) reservation(destinationPath string) (Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var best *Record
	for _, rec := range s.records {
		if !UnresolvedStates[rec.State] {
			continue
		}
		if rec.DestinationPath != destinationPath {
			continue
		}
		if best == nil || rec.CreatedAt.Before(best.CreatedAt) {
			best = rec
		}
	}
	if best == nil {
		return Record{}, false
	}
	return best.Snapshot(), true
}

// list returns one bounded page in deterministic order: unresolved records
// (active and cleanup-pending, always discoverable) first, then resolved
// terminal records, each group ordered by timestamp descending with ID
// ascending as the tiebreak.
func (s *store) list(q ListQuery) ListResult {
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultListLimit
	}
	if limit > MaxListLimit {
		limit = MaxListLimit
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	unresolved := make([]*Record, 0)
	resolved := make([]*Record, 0)
	for _, rec := range s.records {
		if UnresolvedStates[rec.State] {
			unresolved = append(unresolved, rec)
		} else if ResolvedStates[rec.State] {
			resolved = append(resolved, rec)
		}
	}
	sort.SliceStable(unresolved, func(i, j int) bool {
		if unresolved[i].UpdatedAt.Equal(unresolved[j].UpdatedAt) {
			return unresolved[i].ID < unresolved[j].ID
		}
		return unresolved[i].UpdatedAt.After(unresolved[j].UpdatedAt)
	})
	sort.SliceStable(resolved, func(i, j int) bool {
		if resolved[i].ResolvedAt.Equal(resolved[j].ResolvedAt) {
			return resolved[i].ID < resolved[j].ID
		}
		return resolved[i].ResolvedAt.After(resolved[j].ResolvedAt)
	})
	ordered := append(unresolved, resolved...)
	group, afterNano, afterID, hasCursor := parseListCursor(q.After)
	start := 0
	if hasCursor {
		start = len(ordered)
		for i, rec := range ordered {
			recGroup := 1
			if ResolvedStates[rec.State] {
				recGroup = 2
			}
			key := rec.UpdatedAt.UnixNano()
			if recGroup == 2 {
				key = rec.ResolvedAt.UnixNano()
			}
			if recGroup > group || (recGroup == group && (key < afterNano || (key == afterNano && rec.ID > afterID))) {
				start = i
				break
			}
		}
	}
	result := ListResult{Operations: []Record{}}
	for i := start; i < len(ordered) && len(result.Operations) < limit; i++ {
		result.Operations = append(result.Operations, ordered[i].Snapshot())
	}
	if start+len(result.Operations) < len(ordered) {
		last := ordered[start+len(result.Operations)-1]
		group := 1
		key := last.UpdatedAt.UnixNano()
		if ResolvedStates[last.State] {
			group = 2
			key = last.ResolvedAt.UnixNano()
		}
		result.NextToken = formatListCursor(group, key, last.ID)
	}
	return result
}

// parseListCursor decodes "<group>|<unixnano>|<id>".
func parseListCursor(token string) (int, int64, string, bool) {
	if token == "" {
		return 0, 0, "", false
	}
	parts := strings.SplitN(token, "|", 3)
	if len(parts) != 3 {
		return 0, 0, "", false
	}
	group, err := strconv.Atoi(parts[0])
	if err != nil || (group != 1 && group != 2) {
		return 0, 0, "", false
	}
	nano, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, 0, "", false
	}
	return group, nano, parts[2], true
}

func formatListCursor(group int, nano int64, id string) string {
	return strconv.Itoa(group) + "|" + strconv.FormatInt(nano, 10) + "|" + id
}

// all returns copies of every retained record (for recovery and pruning).
func (s *store) all() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Record, 0, len(s.records))
	for _, rec := range s.records {
		out = append(out, rec.Snapshot())
	}
	return out
}

// prune removes only resolved terminal records older than the retention
// window. It never touches repositories, staging, or unresolved records.
func (s *store) prune(now time.Time) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var pruned []string
	for id, rec := range s.records {
		if !ResolvedStates[rec.State] {
			continue
		}
		if rec.ResolvedAt.IsZero() {
			continue
		}
		if now.Sub(rec.ResolvedAt) < RetentionWindow {
			continue
		}
		if err := os.Remove(s.pathLocked(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
			// Keep the in-memory record when the file cannot be
			// removed; the next sweep retries.
			continue
		}
		delete(s.records, id)
		pruned = append(pruned, id)
	}
	return pruned
}

// remove drops a record from memory and disk. It is only used by prune and
// tests; reservations and repositories are never removed this way in
// production flows.
func (s *store) remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(s.pathLocked(id)); err == nil {
		_ = os.Remove(s.pathLocked(id))
	}
	delete(s.records, id)
}
