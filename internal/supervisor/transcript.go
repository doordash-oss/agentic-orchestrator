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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	KindMarker     RecordKind = "marker"
	KindNote       RecordKind = "note"
	KindCheckpoint RecordKind = "checkpoint"
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

	// maxReplay bounds a stream resume; a cursor further behind the head
	// must re-snapshot through the paged endpoint.
	maxReplay = maxPageLimit

	transcriptFileName = "transcript.jsonl"
	indexFileName      = "transcript.idx"
	corruptCopyInfix   = ".corrupt-"
	indexSizePrefix    = "size "
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

// CursorOutOfRangeError refuses a page cursor beyond the stored range: an
// after past the head or a before past the head plus one.
type CursorOutOfRangeError struct {
	HeadSeq int64
}

func (e *CursorOutOfRangeError) Error() string {
	return fmt.Sprintf("supervisor transcript: cursor out of range (head %d)", e.HeadSeq)
}

// RecoveryNote describes records an open could not read: a complete line
// that failed to parse or broke the seq sequence ended the valid prefix, and
// the untouched original was preserved at PreservedPath.
type RecoveryNote struct {
	AfterSeq      int64
	Unread        int
	PreservedPath string
}

// ProviderRecordID derives the deterministic id of a provider-originated
// record, so a provider item appended twice commits once. Kind is part of
// the key, so one provider item yielding several kinds gets distinct ids.
func ProviderRecordID(conversationID string, generation int64, kind RecordKind, providerID string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{conversationID, strconv.FormatInt(generation, 10), string(kind), providerID}, "\x00")))
	return "prov-" + hex.EncodeToString(sum[:16])
}

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
	// index is the sidecar holding one offset line per record and a final
	// size line; indexBody is the byte length of the offset lines.
	index     *os.File
	indexBody int64
	// offsets[i] is the byte offset of the record with seq i+1.
	offsets         []int64
	size            int64
	byCMID          map[string]int64
	byID            map[string]int64
	creationDisplay creationDisplayIndex
	// recovery is set when open preserved a corrupt transcript; the
	// coordinator turns it into a marker once.
	recovery *RecoveryNote
	// content reports whether any record carries history a harness would
	// see on rebuild.
	content bool
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
		byID:           map[string]int64{},
		newID:          newID,
		now:            now,
	}
	if err := s.load(); err != nil {
		_ = f.Close()
		if s.index != nil {
			_ = s.index.Close()
		}
		return nil, err
	}
	return s, nil
}

func (s *transcriptStore) load() error {
	info, err := s.file.Stat()
	if err != nil {
		return fmt.Errorf("stat transcript: %w", err)
	}
	scan, err := scanTranscript(s.file, info.Size())
	if err != nil {
		return err
	}
	if scan.unread > 0 {
		// A damaged complete line is not an interrupted append: keep the
		// original intact beside the transcript before cutting it back.
		path, err := s.preserveCorrupt()
		if err != nil {
			return err
		}
		s.recovery = &RecoveryNote{AfterSeq: int64(len(scan.offsets)), Unread: scan.unread, PreservedPath: path}
	}
	if scan.size != info.Size() {
		// Drop a torn trailing line (or the unreadable suffix) so the next
		// append starts on a record boundary.
		if err := s.file.Truncate(scan.size); err != nil {
			return fmt.Errorf("truncate transcript tail: %w", err)
		}
		if err := s.file.Sync(); err != nil {
			return fmt.Errorf("sync transcript: %w", err)
		}
	}
	s.offsets = scan.offsets
	s.size = scan.size
	for _, rec := range scan.records {
		s.creationDisplay.observe(rec)
		if rec.Kind == KindUser && rec.ClientMessageID != "" {
			s.byCMID[rec.ClientMessageID] = rec.Seq
		}
		if rec.ID != "" {
			s.byID[rec.ID] = rec.Seq
		}
		if rec.Generation > s.generation {
			s.generation = rec.Generation
		}
		if isHistoryContent(rec) {
			s.content = true
		}
	}
	if !s.indexMatches() {
		if err := s.writeIndex(); err != nil {
			return err
		}
	}
	index, err := os.OpenFile(s.indexPath(), os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("open transcript index: %w", err)
	}
	s.index = index
	return nil
}

// transcriptScan is the valid prefix of a transcript: its record offsets,
// byte length and decoded records, plus how many complete lines after it
// could not be read.
type transcriptScan struct {
	offsets []int64
	size    int64
	records []Record
	unread  int
}

// scanTranscript reads every complete record line, verifying that seq runs
// 1..n. A complete line that fails to parse or breaks the sequence ends the
// valid prefix; every complete line from there on counts as unread. A
// trailing line without a newline is a torn append and is not counted.
func scanTranscript(f *os.File, size int64) (transcriptScan, error) {
	reader := bufio.NewReaderSize(io.NewSectionReader(f, 0, size), 64*1024)
	var (
		scan   transcriptScan
		offset int64
	)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			if scan.unread > 0 {
				scan.unread++
			} else {
				var rec Record
				if jsonErr := json.Unmarshal(bytes.TrimSpace(line), &rec); jsonErr != nil || rec.Seq != int64(len(scan.offsets)+1) {
					scan.unread = 1
				} else {
					scan.offsets = append(scan.offsets, offset)
					scan.records = append(scan.records, rec)
					offset += int64(len(line))
				}
			}
		}
		if err == io.EOF {
			scan.size = offset
			return scan, nil
		}
		if err != nil {
			return transcriptScan{}, fmt.Errorf("read transcript: %w", err)
		}
	}
}

// preserveCorrupt copies the untouched transcript to a timestamped sibling
// and returns its path.
func (s *transcriptStore) preserveCorrupt() (string, error) {
	path := filepath.Join(s.dir, transcriptFileName+corruptCopyInfix+s.now().UTC().Format("20060102T150405.000000000Z"))
	dst, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", fmt.Errorf("preserve corrupt transcript: %w", err)
	}
	if _, err := io.Copy(dst, io.NewSectionReader(s.file, 0, 1<<62)); err != nil {
		_ = dst.Close()
		return "", fmt.Errorf("preserve corrupt transcript: %w", err)
	}
	if err := dst.Sync(); err != nil {
		_ = dst.Close()
		return "", fmt.Errorf("preserve corrupt transcript: %w", err)
	}
	if err := dst.Close(); err != nil {
		return "", fmt.Errorf("preserve corrupt transcript: %w", err)
	}
	return path, nil
}

// takeRecovery returns the pending recovery note once.
func (s *transcriptStore) takeRecovery() *RecoveryNote {
	s.mu.Lock()
	defer s.mu.Unlock()
	note := s.recovery
	s.recovery = nil
	return note
}

func (s *transcriptStore) indexPath() string { return filepath.Join(s.dir, indexFileName) }

// indexMatches reports whether the sidecar names exactly the scanned
// offsets followed by the scanned size, and records the offset lines'
// length for the next append.
func (s *transcriptStore) indexMatches() bool {
	data, err := os.ReadFile(s.indexPath())
	if err != nil || len(data) == 0 || data[len(data)-1] != '\n' {
		return false
	}
	lines := strings.Split(string(data[:len(data)-1]), "\n")
	if len(lines) != len(s.offsets)+1 {
		return false
	}
	for i, off := range s.offsets {
		if lines[i] != strconv.FormatInt(off, 10) {
			return false
		}
	}
	if lines[len(lines)-1] != indexSizePrefix+strconv.FormatInt(s.size, 10) {
		return false
	}
	s.indexBody = int64(len(data) - len(lines[len(lines)-1]) - 1)
	return true
}

// writeIndex rewrites the whole sidecar atomically; it runs only on open
// when the sidecar disagrees with the scan.
func (s *transcriptStore) writeIndex() error {
	var b strings.Builder
	for _, off := range s.offsets {
		b.WriteString(strconv.FormatInt(off, 10))
		b.WriteByte('\n')
	}
	s.indexBody = int64(b.Len())
	b.WriteString(indexSizePrefix)
	b.WriteString(strconv.FormatInt(s.size, 10))
	b.WriteByte('\n')
	return writeFileAtomic(s.indexPath(), []byte(b.String()))
}

// appendIndexLocked replaces the size line with the new record's offset and
// the new size, leaving every earlier offset line untouched.
func (s *transcriptStore) appendIndexLocked(offset int64) error {
	tail := strconv.FormatInt(offset, 10) + "\n" + indexSizePrefix + strconv.FormatInt(s.size, 10) + "\n"
	if _, err := s.index.WriteAt([]byte(tail), s.indexBody); err != nil {
		return err
	}
	s.indexBody += int64(len(strconv.FormatInt(offset, 10)) + 1)
	return s.index.Truncate(s.indexBody + int64(len(indexSizePrefix)+len(strconv.FormatInt(s.size, 10))+1))
}

func (s *transcriptStore) close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	if s.index != nil {
		_ = s.index.Close()
		s.index = nil
	}
	return err
}

// setGeneration moves the append fence to gen; appends tagged with any other
// generation are rejected from now on.
func (s *transcriptStore) setGeneration(gen int64) {
	s.mu.Lock()
	s.generation = gen
	s.mu.Unlock()
}

// hasContent reports whether the transcript holds any record a rebuild
// selects: a user, assistant, tool_use or tool_result record that the model
// saw.
func (s *transcriptStore) hasContent() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.content
}

func isHistoryContent(rec Record) bool {
	if rec.Visibility != VisibilityContent && rec.Visibility != VisibilityModelOnly {
		return false
	}
	switch rec.Kind {
	case KindUser, KindAssistant, KindToolUse, KindToolResult, KindNote, KindCheckpoint:
		return true
	}
	return false
}

func (s *transcriptStore) head() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return int64(len(s.offsets))
}

// appendRecord commits rec, allocating its seq, id and timestamp. A user
// record whose client message id is already committed, or a record whose
// (deterministic) id is already committed, returns the existing record with
// existing=true and appends nothing.
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
	if rec.ID != "" {
		if seq, ok := s.byID[rec.ID]; ok {
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
	offset := s.size
	s.offsets = append(s.offsets, offset)
	s.size += int64(len(line))
	if rec.Kind == KindUser && rec.ClientMessageID != "" {
		s.byCMID[rec.ClientMessageID] = rec.Seq
	}
	s.byID[rec.ID] = rec.Seq
	s.creationDisplay.observe(rec)
	if isHistoryContent(rec) {
		s.content = true
	}
	// The JSONL is the system of record; a failed index append is repaired
	// by the rebuild on the next open.
	_ = s.appendIndexLocked(offset)
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

// rangedPage reads one page, refusing a cursor beyond the stored range with
// a CursorOutOfRangeError: an after past the head or a before past the head
// plus one. before=1, after=head and before=head+1 stay valid.
func (s *transcriptStore) rangedPage(q PageQuery) (Page, error) {
	return s.readPage(q, true)
}

// page reads one page. Out-of-range cursors return an empty page.
func (s *transcriptStore) page(q PageQuery) (Page, error) {
	return s.readPage(q, false)
}

func (s *transcriptStore) readPage(q PageQuery, strict bool) (Page, error) {
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
	if strict && ((q.HasAfter && q.After > head) || (q.HasBefore && q.Before > head+1)) {
		return Page{}, &CursorOutOfRangeError{HeadSeq: head}
	}
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

// replayAfter returns the records after seq for a stream resume, or ok=false
// when more than maxReplay records follow the cursor.
func (s *transcriptStore) replayAfter(seq int64) ([]Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if int64(len(s.offsets))-seq > maxReplay {
		return nil, false, nil
	}
	recs, err := s.afterLocked(seq)
	for i := range recs {
		recs[i] = s.creationDisplay.project(recs[i])
	}
	return recs, err == nil, err
}

// after returns every record with seq greater than seq, in order.
func (s *transcriptStore) after(seq int64) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.afterLocked(seq)
}

func (s *transcriptStore) afterLocked(seq int64) ([]Record, error) {
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
