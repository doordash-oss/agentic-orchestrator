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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	operationSchemaVersion      = 1
	operationActiveIndexFile    = "active.yml"
	operationMetadataIndexFile  = "index.yml"
	operationRecordFileSuffix   = ".yaml"
	operationRecordTempFileGlob = "operation-*.yaml.tmp"
)

type OperationStatus string

const (
	OperationStatusQueued      OperationStatus = "queued"
	OperationStatusRunning     OperationStatus = "running"
	OperationStatusSucceeded   OperationStatus = "succeeded"
	OperationStatusFailed      OperationStatus = "failed"
	OperationStatusRejected    OperationStatus = "rejected"
	OperationStatusInterrupted OperationStatus = "interrupted"
)

type OperationRegistryOptions struct {
	Dir          string
	DefaultLimit int
	MaxLimit     int
	Reconcile    OperationReconciler
}

type OperationTarget struct {
	Type      string `json:"type" yaml:"type"`
	FeatureID string `json:"feature_id,omitempty" yaml:"feature_id,omitempty"`
	RunNumber int    `json:"run_number,omitempty" yaml:"run_number,omitempty"`
	SessionID string `json:"session_id,omitempty" yaml:"session_id,omitempty"`
	RequestID string `json:"request_id,omitempty" yaml:"request_id,omitempty"`
}

type OperationError struct {
	Code     string            `json:"code" yaml:"code"`
	Message  string            `json:"message" yaml:"message"`
	Metadata map[string]string `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}

type OperationRecord struct {
	SchemaVersion int               `json:"-" yaml:"schema_version"`
	ID            string            `json:"id" yaml:"id"`
	Kind          string            `json:"kind" yaml:"kind"`
	Target        OperationTarget   `json:"target" yaml:"target"`
	RequestedAt   time.Time         `json:"requested_at" yaml:"requested_at"`
	UpdatedAt     time.Time         `json:"updated_at" yaml:"updated_at"`
	CompletedAt   *time.Time        `json:"completed_at,omitempty" yaml:"completed_at,omitempty"`
	Status        OperationStatus   `json:"status" yaml:"status"`
	Result        map[string]string `json:"result,omitempty" yaml:"result,omitempty"`
	Error         *OperationError   `json:"error,omitempty" yaml:"error,omitempty"`
}

type OperationReconciliation struct {
	Status OperationStatus
	Result map[string]string
	Error  *OperationError
}

type OperationReconciler func(OperationRecord) OperationReconciliation

type operationActiveIndex struct {
	Active []string `yaml:"active"`
}

type operationMetadataIndex struct {
	SchemaVersion int                   `yaml:"schema_version"`
	Operations    []operationIndexEntry `yaml:"operations"`
}

type operationIndexEntry struct {
	ID          string            `yaml:"id"`
	Kind        string            `yaml:"kind"`
	Target      OperationTarget   `yaml:"target"`
	RequestedAt time.Time         `yaml:"requested_at"`
	UpdatedAt   time.Time         `yaml:"updated_at"`
	CompletedAt *time.Time        `yaml:"completed_at,omitempty"`
	Status      OperationStatus   `yaml:"status"`
	Result      map[string]string `yaml:"result,omitempty"`
	Error       *OperationError   `yaml:"error,omitempty"`
}

type OperationListOptions struct {
	State     OperationStatus
	FeatureID string
	Kind      string
	Cursor    string
	Limit     int
}

type OperationListPage struct {
	Operations []OperationDTO
	NextCursor string
}

type OperationRegistry struct {
	mu           sync.RWMutex
	dir          string
	defaultLimit int
	maxLimit     int
	records      map[string]OperationRecord
	files        []string
	index        []operationIndexEntry
	indexByID    map[string]int
	reconcile    OperationReconciler
}

var operationIDSequence atomic.Uint64

func NewOperationRegistry(opts OperationRegistryOptions) (*OperationRegistry, error) {
	if opts.Dir == "" {
		return nil, errors.New("operation registry dir is required")
	}
	defaultLimit := opts.DefaultLimit
	if defaultLimit <= 0 {
		defaultLimit = 50
	}
	maxLimit := opts.MaxLimit
	if maxLimit <= 0 {
		maxLimit = 200
	}
	if defaultLimit > maxLimit {
		defaultLimit = maxLimit
	}
	if err := os.MkdirAll(opts.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating operation registry dir: %w", err)
	}
	r := &OperationRegistry{
		dir:          opts.Dir,
		defaultLimit: defaultLimit,
		maxLimit:     maxLimit,
		records:      map[string]OperationRecord{},
		indexByID:    map[string]int{},
		reconcile:    opts.Reconcile,
	}
	if err := r.load(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *OperationRegistry) Create(kind string, target OperationTarget) (OperationRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now().UTC()
	id, err := r.nextIDLocked(now)
	if err != nil {
		return OperationRecord{}, err
	}
	rec := OperationRecord{
		SchemaVersion: operationSchemaVersion,
		ID:            id,
		Kind:          kind,
		Target:        target,
		RequestedAt:   now,
		UpdatedAt:     now,
		Status:        OperationStatusQueued,
	}
	if err := r.saveLocked(rec); err != nil {
		return OperationRecord{}, err
	}
	r.records[rec.ID] = rec
	r.upsertIndexLocked(rec)
	r.rememberFileLocked(rec.ID)
	if err := r.saveIndexesLocked(); err != nil {
		return OperationRecord{}, err
	}
	return rec, nil
}

func (r *OperationRegistry) UpdateStatus(id string, status OperationStatus) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	rec, ok := r.records[id]
	if !ok {
		loaded, err := r.readRecordByID(id)
		if err != nil {
			return err
		}
		rec = loaded
	}
	now := time.Now().UTC()
	rec.Status = status
	rec.UpdatedAt = now
	if isTerminalOperationStatus(status) {
		rec.CompletedAt = &now
	}
	if err := r.saveLocked(rec); err != nil {
		return err
	}
	if isTerminalOperationStatus(rec.Status) {
		delete(r.records, id)
	} else {
		r.records[id] = rec
	}
	r.upsertIndexLocked(rec)
	r.rememberFileLocked(id)
	if err := r.saveIndexesLocked(); err != nil {
		return err
	}
	return nil
}

func (r *OperationRegistry) Complete(id string, status OperationStatus, result map[string]string, opErr *OperationError) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	rec, ok := r.records[id]
	if !ok {
		loaded, err := r.readRecordByID(id)
		if err != nil {
			return err
		}
		rec = loaded
	}
	if isTerminalOperationStatus(rec.Status) {
		return nil
	}
	now := time.Now().UTC()
	rec.Status = status
	rec.UpdatedAt = now
	rec.CompletedAt = &now
	rec.Result = safeOperationResult(result)
	rec.Error = safeOperationError(opErr)
	if err := r.saveLocked(rec); err != nil {
		return err
	}
	delete(r.records, id)
	r.upsertIndexLocked(rec)
	r.rememberFileLocked(id)
	if err := r.saveIndexesLocked(); err != nil {
		return err
	}
	return nil
}

func (r *OperationRegistry) List(opts OperationListOptions) (OperationListPage, error) {
	r.mu.RLock()
	limit := r.pageLimit(opts.Limit)
	index := append([]operationIndexEntry(nil), r.index...)
	r.mu.RUnlock()

	start := 0
	if opts.Cursor != "" {
		n, err := strconv.Atoi(opts.Cursor)
		if err != nil {
			return OperationListPage{}, fmt.Errorf("parsing operation cursor: %w", err)
		}
		if n < 0 {
			return OperationListPage{}, errors.New("parsing operation cursor: negative offset")
		}
		start = n
	}
	out := make([]OperationDTO, 0, limit)
	matches := 0
	for _, entry := range index {
		if entry.ID == "" || !operationIndexMatches(entry, opts) {
			continue
		}
		if matches < start {
			matches++
			continue
		}
		if len(out) >= limit {
			return OperationListPage{Operations: out, NextCursor: strconv.Itoa(start + len(out))}, nil
		}
		out = append(out, operationDTOFromIndex(entry))
		matches++
	}
	return OperationListPage{Operations: out}, nil
}

func (r *OperationRegistry) load() error {
	index, indexedMetadata, err := r.loadMetadataIndex()
	if err != nil {
		return err
	}
	if indexedMetadata {
		r.index = index
		r.rebuildIndexLookupLocked()
		return r.loadActiveFromMetadataIndexLocked()
	}

	files, err := r.operationFiles()
	if err != nil {
		return err
	}
	r.files = files
	activeIDs, indexed, err := r.loadActiveIndex()
	if err != nil {
		return err
	}
	loaded := map[string]struct{}{}
	if indexed {
		for _, id := range activeIDs {
			rec, err := r.readRecordByID(id)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			if rec.ID == "" {
				continue
			}
			if !isTerminalOperationStatus(rec.Status) {
				if err := r.markReconciledLocked(&rec); err != nil {
					return err
				}
			} else {
				r.upsertIndexLocked(rec)
			}
			loaded[rec.ID] = struct{}{}
		}
		return r.loadTerminalWindowLocked(files, loaded)
	}

	terminalLoaded := 0
	scanned := 0
	for _, path := range files {
		if terminalLoaded >= r.defaultLimit || scanned >= r.defaultLimit {
			break
		}
		scanned++
		rec, err := readOperationRecord(path)
		if err != nil {
			return err
		}
		if rec.ID == "" {
			continue
		}
		if !isTerminalOperationStatus(rec.Status) {
			if err := r.markReconciledLocked(&rec); err != nil {
				return err
			}
			continue
		}
		r.upsertIndexLocked(rec)
		terminalLoaded++
	}
	if err := r.saveIndexesLocked(); err != nil {
		return err
	}
	return nil
}

func (r *OperationRegistry) loadActiveFromMetadataIndexLocked() error {
	activeIDs, indexed, err := r.loadActiveIndex()
	if err != nil {
		return err
	}
	activeSet := map[string]struct{}{}
	if indexed {
		for _, id := range activeIDs {
			activeSet[id] = struct{}{}
		}
	}
	for _, entry := range r.index {
		if !isTerminalOperationStatus(entry.Status) {
			activeSet[entry.ID] = struct{}{}
		}
	}
	ids := make([]string, 0, len(activeSet))
	for id := range activeSet {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		rec, err := r.readRecordByID(id)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if rec.ID == "" {
			continue
		}
		if !isTerminalOperationStatus(rec.Status) {
			if err := r.markReconciledLocked(&rec); err != nil {
				return err
			}
			continue
		}
		r.upsertIndexLocked(rec)
	}
	return r.saveIndexesLocked()
}

func (r *OperationRegistry) loadTerminalWindowLocked(files []string, loaded map[string]struct{}) error {
	terminalLoaded := 0
	scanned := 0
	scanLimit := r.defaultLimit + len(loaded)
	for _, path := range files {
		if terminalLoaded >= r.defaultLimit || scanned >= scanLimit {
			break
		}
		scanned++
		rec, err := readOperationRecord(path)
		if err != nil {
			return err
		}
		if rec.ID == "" {
			continue
		}
		if _, ok := loaded[rec.ID]; ok {
			continue
		}
		if !isTerminalOperationStatus(rec.Status) {
			if err := r.markReconciledLocked(&rec); err != nil {
				return err
			}
			loaded[rec.ID] = struct{}{}
			continue
		}
		r.upsertIndexLocked(rec)
		loaded[rec.ID] = struct{}{}
		terminalLoaded++
	}
	return r.saveIndexesLocked()
}

func (r *OperationRegistry) markReconciledLocked(rec *OperationRecord) error {
	now := time.Now().UTC()
	reconciled := r.reconcileStaleOperationLocked(*rec)
	rec.Status = reconciled.Status
	rec.UpdatedAt = now
	rec.CompletedAt = &now
	rec.Result = reconciled.Result
	rec.Error = reconciled.Error
	if err := r.saveLocked(*rec); err != nil {
		return err
	}
	delete(r.records, rec.ID)
	r.upsertIndexLocked(*rec)
	return nil
}

func (r *OperationRegistry) reconcileStaleOperationLocked(rec OperationRecord) OperationReconciliation {
	if r.reconcile != nil {
		reconciled := r.reconcile(rec)
		if isTerminalOperationStatus(reconciled.Status) {
			if reconciled.Status != OperationStatusSucceeded && reconciled.Error == nil {
				reconciled.Error = &OperationError{Code: string(reconciled.Status)}
			}
			if reconciled.Status == OperationStatusSucceeded {
				reconciled.Error = nil
			}
			return reconciled
		}
	}
	return OperationReconciliation{
		Status: OperationStatusInterrupted,
		Error:  &OperationError{Code: "interrupted", Message: "operation interrupted by server restart"},
	}
}

func (r *OperationRegistry) operationFiles() ([]string, error) {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return nil, fmt.Errorf("reading operation registry dir: %w", err)
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), operationRecordFileSuffix) {
			continue
		}
		files = append(files, filepath.Join(r.dir, entry.Name()))
	}
	sort.Slice(files, func(i, j int) bool {
		return operationFileID(files[i]) > operationFileID(files[j])
	})
	return files, nil
}

func (r *OperationRegistry) loadActiveIndex() ([]string, bool, error) {
	data, err := os.ReadFile(filepath.Join(r.dir, operationActiveIndexFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("reading operation active index: %w", err)
	}
	var index operationActiveIndex
	if err := yaml.Unmarshal(data, &index); err != nil {
		return nil, false, fmt.Errorf("parsing operation active index: %w", err)
	}
	return index.Active, true, nil
}

func (r *OperationRegistry) loadMetadataIndex() ([]operationIndexEntry, bool, error) {
	data, err := os.ReadFile(filepath.Join(r.dir, operationMetadataIndexFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("reading operation metadata index: %w", err)
	}
	var index operationMetadataIndex
	if err := yaml.Unmarshal(data, &index); err != nil {
		return nil, false, fmt.Errorf("parsing operation metadata index: %w", err)
	}
	entries := make([]operationIndexEntry, 0, len(index.Operations))
	seen := map[string]struct{}{}
	for _, entry := range index.Operations {
		if entry.ID == "" {
			continue
		}
		if _, ok := seen[entry.ID]; ok {
			continue
		}
		entry.Result = safeOperationResult(entry.Result)
		entry.Error = safeOperationError(entry.Error)
		entries = append(entries, entry)
		seen[entry.ID] = struct{}{}
	}
	sortOperationIndex(entries)
	return entries, true, nil
}

func (r *OperationRegistry) saveIndexesLocked() error {
	if err := r.saveMetadataIndexLocked(); err != nil {
		return err
	}
	return r.saveActiveIndexLocked()
}

func (r *OperationRegistry) saveActiveIndexLocked() error {
	index := operationActiveIndex{}
	for id, rec := range r.records {
		if !isTerminalOperationStatus(rec.Status) {
			index.Active = append(index.Active, id)
		}
	}
	sort.Strings(index.Active)
	data, err := yaml.Marshal(index)
	if err != nil {
		return fmt.Errorf("marshaling operation active index: %w", err)
	}
	tmp, err := os.CreateTemp(r.dir, "active-*.yml.tmp")
	if err != nil {
		return fmt.Errorf("creating operation active index temp file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("writing operation active index temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("closing operation active index temp file: %w", err)
	}
	if err := os.Rename(tmpName, filepath.Join(r.dir, operationActiveIndexFile)); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("renaming operation active index file: %w", err)
	}
	return nil
}

func (r *OperationRegistry) saveMetadataIndexLocked() error {
	index := operationMetadataIndex{
		SchemaVersion: operationSchemaVersion,
		Operations:    append([]operationIndexEntry(nil), r.index...),
	}
	sortOperationIndex(index.Operations)
	data, err := yaml.Marshal(index)
	if err != nil {
		return fmt.Errorf("marshaling operation metadata index: %w", err)
	}
	tmp, err := os.CreateTemp(r.dir, "index-*.yml.tmp")
	if err != nil {
		return fmt.Errorf("creating operation metadata index temp file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("writing operation metadata index temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("closing operation metadata index temp file: %w", err)
	}
	if err := os.Rename(tmpName, filepath.Join(r.dir, operationMetadataIndexFile)); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("renaming operation metadata index file: %w", err)
	}
	return nil
}

func (r *OperationRegistry) readRecordByID(id string) (OperationRecord, error) {
	rec, err := readOperationRecord(filepath.Join(r.dir, id+operationRecordFileSuffix))
	if err != nil {
		return OperationRecord{}, err
	}
	return rec, nil
}

func readOperationRecord(path string) (OperationRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return OperationRecord{}, fmt.Errorf("reading operation record: %w", err)
	}
	var rec OperationRecord
	if err := yaml.Unmarshal(data, &rec); err != nil {
		return OperationRecord{}, fmt.Errorf("parsing operation record: %w", err)
	}
	return rec, nil
}

func operationFileID(path string) string {
	return strings.TrimSuffix(filepath.Base(path), operationRecordFileSuffix)
}

func (r *OperationRegistry) nextIDLocked(now time.Time) (string, error) {
	for {
		id := fmt.Sprintf("op-%d-%06d", now.UnixNano(), operationIDSequence.Add(1))
		if _, exists := r.records[id]; !exists {
			if _, err := os.Stat(filepath.Join(r.dir, id+operationRecordFileSuffix)); errors.Is(err, os.ErrNotExist) {
				return id, nil
			} else if err != nil {
				return "", fmt.Errorf("checking operation id collision: %w", err)
			}
		}
		now = time.Now().UTC()
	}
}

func (r *OperationRegistry) saveLocked(rec OperationRecord) error {
	rec.SchemaVersion = operationSchemaVersion
	rec.Result = safeOperationResult(rec.Result)
	rec.Error = safeOperationError(rec.Error)
	data, err := yaml.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshaling operation record: %w", err)
	}
	tmp, err := os.CreateTemp(r.dir, operationRecordTempFileGlob)
	if err != nil {
		return fmt.Errorf("creating operation temp file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("writing operation temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("closing operation temp file: %w", err)
	}
	if err := os.Rename(tmpName, filepath.Join(r.dir, rec.ID+operationRecordFileSuffix)); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("renaming operation file: %w", err)
	}
	return nil
}

func (r *OperationRegistry) rememberFileLocked(id string) {
	path := filepath.Join(r.dir, id+operationRecordFileSuffix)
	for _, existing := range r.files {
		if existing == path {
			return
		}
	}
	r.files = append(r.files, path)
	sort.Slice(r.files, func(i, j int) bool {
		return operationFileID(r.files[i]) > operationFileID(r.files[j])
	})
}

func (r *OperationRegistry) upsertIndexLocked(rec OperationRecord) {
	entry := operationIndexEntryFromRecord(rec)
	if entry.ID == "" {
		return
	}
	if r.indexByID == nil {
		r.rebuildIndexLookupLocked()
	}
	if pos, ok := r.indexByID[entry.ID]; ok {
		r.index[pos] = entry
	} else {
		r.index = append(r.index, entry)
	}
	sortOperationIndex(r.index)
	r.rebuildIndexLookupLocked()
}

func (r *OperationRegistry) rebuildIndexLookupLocked() {
	r.indexByID = make(map[string]int, len(r.index))
	for i, entry := range r.index {
		if entry.ID == "" {
			continue
		}
		r.indexByID[entry.ID] = i
	}
}

func sortOperationIndex(entries []operationIndexEntry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].RequestedAt.Equal(entries[j].RequestedAt) {
			return entries[i].ID > entries[j].ID
		}
		return entries[i].RequestedAt.After(entries[j].RequestedAt)
	})
}

func (r *OperationRegistry) pageLimit(limit int) int {
	if limit <= 0 {
		return r.defaultLimit
	}
	if limit > r.maxLimit {
		return r.maxLimit
	}
	return limit
}

func isTerminalOperationStatus(status OperationStatus) bool {
	switch status {
	case OperationStatusSucceeded, OperationStatusFailed, OperationStatusRejected, OperationStatusInterrupted:
		return true
	default:
		return false
	}
}

func operationIndexMatches(entry operationIndexEntry, opts OperationListOptions) bool {
	if opts.State != "" && entry.Status != opts.State {
		return false
	}
	if opts.FeatureID != "" && entry.Target.FeatureID != opts.FeatureID {
		return false
	}
	if opts.Kind != "" && entry.Kind != opts.Kind {
		return false
	}
	return true
}

func safeOperationResult(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		switch k {
		case "feature_id", "session_id", "request_id", "status", "decision", "reason", "kind",
			"action", "repo", "repo_name", "cycle_type", "target", "target_phase", "effective_phase",
			"roadmap_phase", "run_number", "recovery_action", "conflict", "branch", "rebase_target",
			"conflict_files", "warning_count", "pipeline", "mode", "had_changes", "phase":
			out[k] = safeDisplayText(v, 120)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func safeOperationError(in *OperationError) *OperationError {
	if in == nil {
		return nil
	}
	code := safeDisplayText(in.Code, 80)
	if code == "" {
		code = "failed"
	}
	message := "operation failed"
	switch code {
	case "conflict":
		message = "operation conflicts with active work"
	case "backpressure":
		message = "operation queue is full"
	case "interrupted":
		message = "operation interrupted by server restart"
	case "bad_request":
		message = "invalid operation request"
	}
	return &OperationError{Code: code, Message: message, Metadata: safeOperationResult(in.Metadata)}
}

func operationIndexEntryFromRecord(rec OperationRecord) operationIndexEntry {
	return operationIndexEntry{
		ID:          rec.ID,
		Kind:        rec.Kind,
		Target:      rec.Target,
		RequestedAt: rec.RequestedAt,
		UpdatedAt:   rec.UpdatedAt,
		CompletedAt: rec.CompletedAt,
		Status:      rec.Status,
		Result:      safeOperationResult(rec.Result),
		Error:       safeOperationError(rec.Error),
	}
}

func operationDTOFromIndex(entry operationIndexEntry) OperationDTO {
	return OperationDTO{
		ID:          entry.ID,
		Kind:        entry.Kind,
		Target:      entry.Target,
		RequestedAt: entry.RequestedAt,
		UpdatedAt:   entry.UpdatedAt,
		CompletedAt: entry.CompletedAt,
		Status:      entry.Status,
		Result:      safeOperationResult(entry.Result),
		Error:       safeOperationError(entry.Error),
	}
}
