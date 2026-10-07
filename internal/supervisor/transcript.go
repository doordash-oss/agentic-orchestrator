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

package supervisor

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// RecordKind classifies a durable transcript record.
type RecordKind string

const (
	KindUser       RecordKind = "user"
	KindAssistant  RecordKind = "assistant"
	KindToolUse    RecordKind = "tool_use"
	KindToolResult RecordKind = "tool_result"
	KindPermission RecordKind = "permission"
	KindQuestion   RecordKind = "question"
)

// Visibility says who a record is for: the model and the UI (content), the
// model only, or the UI only (markers and request verdicts).
type Visibility string

const (
	VisibilityContent     Visibility = "content"
	VisibilityModelOnly   Visibility = "model_only"
	VisibilityDisplayOnly Visibility = "display_only"
)

const (
	defaultPageLimit = 100
	maxPageLimit     = 500

	transcriptFileName = "transcript.jsonl"
	indexFileName      = "transcript.idx"
)

// Record is one committed transcript record. Data holds the kind-specific
// payload; projection to the wire applies redaction.
type Record struct {
	Seq             int64           `json:"seq"`
	ID              string          `json:"id"`
	ConversationID  string          `json:"conversation_id"`
	Generation      int64           `json:"generation"`
	TurnID          string          `json:"turn_id"`
	Kind            RecordKind      `json:"kind"`
	Visibility      Visibility      `json:"visibility"`
	CreatedAt       time.Time       `json:"created_at"`
	ClientMessageID string          `json:"client_message_id,omitempty"`
	StreamMessageID string          `json:"stream_message_id,omitempty"`
	Data            json.RawMessage `json:"data"`
}

// Page is one paged read of the transcript.
type Page struct {
	Items         []Record
	FirstSeq      int64
	LastSeq       int64
	HasMoreBefore bool
	HasMoreAfter  bool
	HeadSeq       int64
}

// PageQuery selects a page: Before and After are exclusive seq cursors,
// each applying only when its Has flag is set (setting both is a caller
// error). With neither, the newest page is returned. Limit zero means the
// default page size.
type PageQuery struct {
	Before    int64
	HasBefore bool
	After     int64
	HasAfter  bool
	Limit     int
}

// ErrRetiredGeneration rejects an append tagged with a generation other than
// the store's current one.
var ErrRetiredGeneration = errors.New("supervisor transcript: append from a retired generation")

// ErrInvalidPageQuery rejects a page request that sets both cursors or an
// out-of-range limit.
var ErrInvalidPageQuery = errors.New("supervisor transcript: invalid page query")

// transcriptStore is the append-only system of record for one conversation.
// Seq allocation, the generation fence, and the append itself share one
// mutex, so seq order is append order and a retired generation can never
// interleave a late write.
type transcriptStore struct {
	mu             sync.Mutex
	dir            string
	conversationID string
	generation     int64
	file           *os.File
	// offsets[i] is the byte offset of the record with seq i+1.
	offsets []int64
	size    int64
	byCMID  map[string]int64
	newID   func() string
	now     func() time.Time
}

// openTranscriptStore opens (creating if needed) the conversation's JSONL
// and sidecar index. A missing or inconsistent index is rebuilt from the
// JSONL; a torn trailing line from an interrupted append is truncated away.
func openTranscriptStore(dir, conversationID string, newID func() string, now func() time.Time) (*transcriptStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create transcript dir: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, transcriptFileName), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open transcript: %w", err)
	}
	s := &transcriptStore{
		dir:            dir,
		conversationID: conversationID,
		file:           f,
		byCMID:         map[string]int64{},
		newID:          newID,
		now:            now,
	}
	if err := s.load(); err != nil {
		_ = f.Close()
		return nil, err
	}
	return s, nil
}

func (s *transcriptStore) load() error {
	info, err := s.file.Stat()
	if err != nil {
		return fmt.Errorf("stat transcript: %w", err)
	}
	offsets, size, records, err := scanTranscript(s.file, info.Size())
	if err != nil {
		return err
	}
	if size != info.Size() {
		// Drop a torn trailing line so the next append starts on a record
		// boundary.
		if err := s.file.Truncate(size); err != nil {
			return fmt.Errorf("truncate torn transcript tail: %w", err)
		}
	}
	s.offsets = offsets
	s.size = size
	for _, rec := range records {
		if rec.Kind == KindUser && rec.ClientMessageID != "" {
			s.byCMID[rec.ClientMessageID] = rec.Seq
		}
		if rec.Generation > s.generation {
			s.generation = rec.Generation
		}
	}
	if !s.indexMatches() {
		return s.writeIndex()
	}
	return nil
}

// scanTranscript reads every complete record line, verifying that seq runs
// 1..n. It returns the offsets, the byte length of the valid prefix, and the
// decoded records.
func scanTranscript(f *os.File, size int64) ([]int64, int64, []Record, error) {
	reader := bufio.NewReaderSize(io.NewSectionReader(f, 0, size), 64*1024)
	var (
		offsets []int64
		records []Record
		offset  int64
	)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			var rec Record
			if jsonErr := json.Unmarshal(bytes.TrimSpace(line), &rec); jsonErr != nil || rec.Seq != int64(len(offsets)+1) {
				// A corrupt or out-of-order record ends the valid prefix.
				return offsets, offset, records, nil
			}
			offsets = append(offsets, offset)
			records = append(records, rec)
			offset += int64(len(line))
		}
		if err == io.EOF {
			return offsets, offset, records, nil
		}
		if err != nil {
			return nil, 0, nil, fmt.Errorf("read transcript: %w", err)
		}
	}
}

type transcriptIndex struct {
	Size    int64   `json:"size"`
	Offsets []int64 `json:"offsets"`
}

func (s *transcriptStore) indexPath() string { return filepath.Join(s.dir, indexFileName) }

func (s *transcriptStore) indexMatches() bool {
	data, err := os.ReadFile(s.indexPath())
	if err != nil {
		return false
	}
	var idx transcriptIndex
	if json.Unmarshal(data, &idx) != nil || idx.Size != s.size || len(idx.Offsets) != len(s.offsets) {
		return false
	}
	for i, off := range idx.Offsets {
		if off != s.offsets[i] {
			return false
		}
	}
	return true
}

func (s *transcriptStore) writeIndex() error {
	data, err := json.Marshal(transcriptIndex{Size: s.size, Offsets: s.offsets})
	if err != nil {
		return err
	}
	return writeFileAtomic(s.indexPath(), data)
}

func (s *transcriptStore) close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	return err
}

// setGeneration moves the append fence to gen; appends tagged with any other
// generation are rejected from now on.
func (s *transcriptStore) setGeneration(gen int64) {
	s.mu.Lock()
	s.generation = gen
	s.mu.Unlock()
}

func (s *transcriptStore) head() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return int64(len(s.offsets))
}

// appendRecord commits rec, allocating its seq, id and timestamp. A user
// record whose client message id is already committed returns the existing
// record with existing=true and appends nothing.
func (s *transcriptStore) appendRecord(rec Record) (Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return Record{}, false, errors.New("supervisor transcript is closed")
	}
	if rec.Kind == KindUser && rec.ClientMessageID != "" {
		if seq, ok := s.byCMID[rec.ClientMessageID]; ok {
			existing, err := s.readLocked(seq)
			return existing, true, err
		}
	}
	if rec.Generation != s.generation {
		return Record{}, false, ErrRetiredGeneration
	}
	rec.Seq = int64(len(s.offsets)) + 1
	rec.ConversationID = s.conversationID
	if rec.ID == "" {
		rec.ID = s.newID()
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = s.now().UTC()
	}
	if len(rec.Data) == 0 {
		rec.Data = json.RawMessage("{}")
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return Record{}, false, fmt.Errorf("encode transcript record: %w", err)
	}
	line = append(line, '\n')
	if _, err := s.file.WriteAt(line, s.size); err != nil {
		return Record{}, false, fmt.Errorf("append transcript record: %w", err)
	}
	if err := s.file.Sync(); err != nil {
		return Record{}, false, fmt.Errorf("sync transcript: %w", err)
	}
	s.offsets = append(s.offsets, s.size)
	s.size += int64(len(line))
	if rec.Kind == KindUser && rec.ClientMessageID != "" {
		s.byCMID[rec.ClientMessageID] = rec.Seq
	}
	// The JSONL is the system of record; a failed index rewrite is repaired
	// by the rebuild on the next open.
	_ = s.writeIndex()
	return rec, false, nil
}

// lookupClientMessage returns the committed user record for a client
// message id.
func (s *transcriptStore) lookupClientMessage(cmid string) (Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seq, ok := s.byCMID[cmid]
	if !ok {
		return Record{}, false
	}
	rec, err := s.readLocked(seq)
	return rec, err == nil
}

func (s *transcriptStore) readLocked(seq int64) (Record, error) {
	if seq < 1 || seq > int64(len(s.offsets)) {
		return Record{}, fmt.Errorf("supervisor transcript: seq %d out of range", seq)
	}
	start := s.offsets[seq-1]
	end := s.size
	if seq < int64(len(s.offsets)) {
		end = s.offsets[seq]
	}
	buf := make([]byte, end-start)
	if _, err := s.file.ReadAt(buf, start); err != nil {
		return Record{}, fmt.Errorf("read transcript record %d: %w", seq, err)
	}
	var rec Record
	if err := json.Unmarshal(bytes.TrimSpace(buf), &rec); err != nil {
		return Record{}, fmt.Errorf("decode transcript record %d: %w", seq, err)
	}
	return rec, nil
}

// page reads one page. Out-of-range cursors return an empty page.
func (s *transcriptStore) page(q PageQuery) (Page, error) {
	if q.HasBefore && q.HasAfter {
		return Page{}, ErrInvalidPageQuery
	}
	if q.Before < 0 || q.After < 0 || q.Limit < 0 || q.Limit > maxPageLimit {
		return Page{}, ErrInvalidPageQuery
	}
	limit := q.Limit
	if limit == 0 {
		limit = defaultPageLimit
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	head := int64(len(s.offsets))
	var from, to int64 // inclusive seq bounds
	switch {
	case q.HasAfter:
		from, to = q.After+1, min(q.After+int64(limit), head)
	case q.HasBefore:
		to = min(q.Before-1, head)
		from = max(to-int64(limit)+1, 1)
		if q.Before-1 > head {
			// A cursor past the head is out of range, not "everything".
			to, from = 0, 1
		}
	default:
		to = head
		from = max(head-int64(limit)+1, 1)
	}
	page := Page{HeadSeq: head}
	if from > to || from < 1 {
		return page, nil
	}
	for seq := from; seq <= to; seq++ {
		rec, err := s.readLocked(seq)
		if err != nil {
			return Page{}, err
		}
		page.Items = append(page.Items, rec)
	}
	page.FirstSeq, page.LastSeq = from, to
	page.HasMoreBefore = from > 1
	page.HasMoreAfter = to < head
	return page, nil
}

// after returns every record with seq greater than seq, in order.
func (s *transcriptStore) after(seq int64) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	head := int64(len(s.offsets))
	var out []Record
	for next := seq + 1; next <= head; next++ {
		rec, err := s.readLocked(next)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, nil
}
