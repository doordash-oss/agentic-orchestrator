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

package observe

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/sdk/resource"
)

const (
	outboxSegmentLimit = 4 << 20
	outboxBatchRecords = 256
	outboxBatchBytes   = 1 << 20
	outboxRetention    = 7 * 24 * time.Hour
	outboxCapacity     = 256 << 20
	maxWideFieldBytes  = 4 << 10
	maxWideEventBytes  = 32 << 10
)

var absolutePathPattern = regexp.MustCompile(`(?:/Users/[^/\s]+|/home/[^/\s]+|[A-Za-z]:\\Users\\[^\\\s]+)(?:[/\\][^\s"']*)?`)

type wideRecord struct {
	EventID   string            `json:"event_id"`
	SpanID    string            `json:"span_id"`
	Event     Event             `json:"event"`
	Bytes     int               `json:"original_bytes,omitempty"`
	Truncated bool              `json:"truncated,omitempty"`
	Resource  map[string]string `json:"resource,omitempty"`
}

type outboxCursor struct {
	Segment string `json:"segment,omitempty"`
	Line    int    `json:"line,omitempty"`
}

type wideEventExporter interface {
	ExportEventBatch(context.Context, []wideRecord) error
}

type eventOutbox struct {
	dir      string
	exporter wideEventExporter
	metrics  *telemetryMetrics
	resource map[string]string

	mu      sync.Mutex
	lossMu  sync.Mutex
	segment string
	stop    chan struct{}
	wake    chan struct{}
	done    chan struct{}
	once    sync.Once
	lossLog sync.Once
}

func newEventOutbox(stateDir string, exporter wideEventExporter, metrics *telemetryMetrics, res *resource.Resource) *eventOutbox {
	o := &eventOutbox{
		dir:      filepath.Join(filepath.Dir(stateDir), "telemetry", "outbox"),
		exporter: exporter,
		metrics:  metrics,
		resource: resourceStrings(res),
		stop:     make(chan struct{}), wake: make(chan struct{}, 1), done: make(chan struct{}),
	}
	if err := os.MkdirAll(o.dir, 0o700); err != nil {
		log.Printf("telemetry outbox unavailable: %v", err)
		o.exporter = nil
		close(o.done)
		return o
	}
	_ = os.Chmod(o.dir, 0o700)
	go o.run()
	return o
}

func (o *eventOutbox) Enqueue(evt Event) bool {
	if o == nil || o.exporter == nil {
		return false
	}
	record := sanitizeWideEvent(evt)
	record.Resource = o.resource
	data, err := json.Marshal(record)
	if err != nil {
		o.recordDrop(1)
		return false
	}
	if len(data) > maxWideEventBytes {
		record.Event.Data = map[string]any{"truncated": true, "original_bytes": len(data)}
		record.Event.Error = boundWideText(record.Event.Error)
		record.Truncated = true
		record.Bytes = len(data)
		data, _ = json.Marshal(record)
		if len(data) > maxWideEventBytes {
			record.Resource = essentialResource(record.Resource)
			data, _ = json.Marshal(record)
		}
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	segment, err := o.appendTarget(len(data) + 1)
	if err == nil {
		f, openErr := os.OpenFile(segment, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if openErr != nil {
			err = openErr
		} else {
			_ = f.Chmod(0o600)
			_, err = f.Write(append(data, '\n'))
			if err == nil {
				err = f.Sync()
			}
			_ = f.Close()
		}
	}
	if err != nil {
		log.Printf("telemetry outbox append failed: %v", err)
		o.recordDrop(1)
		return false
	}
	o.enforceLimitsLocked()
	select {
	case o.wake <- struct{}{}:
	default:
	}
	return true
}

func essentialResource(in map[string]string) map[string]string {
	out := make(map[string]string)
	for _, key := range []string{"service.name", "service.version", "service.instance.id", "agentico.build.revision", "agentico.installation.id", "agentico.telemetry.schema.version"} {
		if value := in[key]; value != "" {
			out[key] = value
		}
	}
	return out
}

func resourceStrings(res *resource.Resource) map[string]string {
	out := map[string]string{}
	if res == nil {
		return out
	}
	iter := res.Iter()
	for iter.Next() {
		kv := iter.Attribute()
		out[string(kv.Key)] = kv.Value.Emit()
	}
	return out
}

func (o *eventOutbox) appendTarget(next int) (string, error) {
	if o.segment != "" {
		if err := repairSegmentTail(o.segment); err != nil {
			return "", err
		}
		if info, err := os.Stat(o.segment); err == nil && info.Size()+int64(next) <= outboxSegmentLimit {
			return o.segment, nil
		}
	}
	o.segment = filepath.Join(o.dir, fmt.Sprintf("segment-%020d-%s.jsonl", time.Now().UnixNano(), randomHex(4)))
	return o.segment, nil
}

// repairSegmentTail removes an incomplete or corrupt suffix before appending.
// A process crash can interrupt the final write even though normal writes are
// fsynced; appending after that partial line would otherwise make every later
// record in the segment unreadable.
func repairSegmentTail(path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	validEnd := 0
	for offset := 0; offset < len(data); {
		relativeEnd := bytes.IndexByte(data[offset:], '\n')
		if relativeEnd < 0 {
			break
		}
		lineEnd := offset + relativeEnd
		if !json.Valid(data[offset:lineEnd]) {
			break
		}
		validEnd = lineEnd + 1
		offset = validEnd
	}
	if validEnd == len(data) {
		return nil
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err = f.Truncate(int64(validEnd)); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

func (o *eventOutbox) run() {
	defer close(o.done)
	backoff := time.Second
	for {
		if err := o.drainOnce(context.Background()); err != nil {
			if o.metrics != nil {
				o.metrics.exportFailures.Add(context.Background(), 1)
			}
			jitter := time.Duration(rand.Int64N(int64(backoff/2 + 1)))
			t := time.NewTimer(backoff + jitter)
			select {
			case <-o.stop:
				t.Stop()
				return
			case <-t.C:
			}
			backoff = min(backoff*2, 5*time.Minute)
			continue
		}
		backoff = time.Second
		select {
		case <-o.stop:
			return
		case <-o.wake:
		case <-time.After(time.Minute):
		}
	}
}

func (o *eventOutbox) drainOnce(parent context.Context) error {
	for {
		records, cursor, next, err := o.readBatch()
		if err != nil {
			return err
		}
		if len(records) == 0 {
			if next != cursor {
				if err := o.saveCursor(next); err != nil {
					return err
				}
				o.cleanupAcknowledged(next)
			}
			return nil
		}
		ctx, cancel := context.WithTimeout(parent, 15*time.Second)
		err = o.exporter.ExportEventBatch(ctx, records)
		cancel()
		if err != nil {
			return err
		}
		o.emitLossSummary()
		if err := o.saveCursor(next); err != nil {
			return err
		}
		o.cleanupAcknowledged(next)
	}
}

func (o *eventOutbox) cleanupAcknowledged(cursor outboxCursor) {
	o.mu.Lock()
	defer o.mu.Unlock()
	names, _ := o.segments()
	for _, name := range names {
		if name < cursor.Segment {
			_ = os.Remove(filepath.Join(o.dir, name))
		}
	}
	if cursor.Segment == "" || cursor.Segment == o.currentSegmentName() {
		return
	}
	f, err := os.Open(filepath.Join(o.dir, cursor.Segment))
	if err != nil {
		return
	}
	scanner := bufio.NewScanner(f)
	lines := 0
	for scanner.Scan() {
		lines++
	}
	_ = f.Close()
	if lines <= cursor.Line {
		_ = os.Remove(filepath.Join(o.dir, cursor.Segment))
		_ = o.saveCursor(outboxCursor{})
	}
}

func (o *eventOutbox) readBatch() ([]wideRecord, outboxCursor, outboxCursor, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	cursor := o.loadCursor()
	segments, err := o.segments()
	if err != nil {
		return nil, cursor, cursor, err
	}
	if len(segments) == 0 {
		return nil, cursor, cursor, nil
	}
	start := 0
	if cursor.Segment != "" {
		start = sort.SearchStrings(segments, cursor.Segment)
		if start == len(segments) {
			return nil, cursor, cursor, nil
		}
	}
	var records []wideRecord
	total := 0
	next := cursor
	for si := start; si < len(segments) && len(records) < outboxBatchRecords; si++ {
		name := segments[si]
		f, err := os.Open(filepath.Join(o.dir, name))
		if err != nil {
			return nil, cursor, cursor, err
		}
		s := bufio.NewScanner(f)
		s.Buffer(make([]byte, 64<<10), maxWideEventBytes+1024)
		line := 0
		for s.Scan() {
			line++
			if name == cursor.Segment && line <= cursor.Line {
				continue
			}
			if total+len(s.Bytes()) > outboxBatchBytes && len(records) > 0 {
				break
			}
			var record wideRecord
			if err := json.Unmarshal(s.Bytes(), &record); err != nil {
				// A partial/corrupt trailing record is ignored until a later append
				// completes it; corruption in a sealed segment is explicitly lost.
				if name != o.currentSegmentName() {
					o.recordDrop(1)
					next = outboxCursor{Segment: name, Line: line}
					continue
				}
				break
			}
			records = append(records, record)
			total += len(s.Bytes())
			next = outboxCursor{Segment: name, Line: line}
			if len(records) == outboxBatchRecords {
				break
			}
		}
		scanErr := s.Err()
		_ = f.Close()
		if scanErr != nil && !errors.Is(scanErr, io.EOF) {
			return nil, cursor, cursor, scanErr
		}
		if len(records) >= outboxBatchRecords || total >= outboxBatchBytes {
			break
		}
	}
	return records, cursor, next, nil
}

func (o *eventOutbox) currentSegmentName() string { return filepath.Base(o.segment) }

func (o *eventOutbox) segments() ([]string, error) {
	entries, err := os.ReadDir(o.dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "segment-") && strings.HasSuffix(e.Name(), ".jsonl") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

func (o *eventOutbox) loadCursor() outboxCursor {
	data, err := os.ReadFile(filepath.Join(o.dir, "cursor.json"))
	if err != nil {
		return outboxCursor{}
	}
	var c outboxCursor
	_ = json.Unmarshal(data, &c)
	return c
}

func (o *eventOutbox) saveCursor(c outboxCursor) error {
	data, _ := json.Marshal(c)
	tmp, err := os.CreateTemp(o.dir, ".cursor-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	_ = tmp.Chmod(0o600)
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, filepath.Join(o.dir, "cursor.json"))
	}
	return err
}

func (o *eventOutbox) enforceLimitsLocked() {
	names, _ := o.segments()
	cursor := o.loadCursor()
	var total int64
	type item struct {
		name string
		size int64
		mod  time.Time
	}
	items := make([]item, 0, len(names))
	for _, name := range names {
		if info, err := os.Stat(filepath.Join(o.dir, name)); err == nil {
			total += info.Size()
			items = append(items, item{name, info.Size(), info.ModTime()})
		}
	}
	now := time.Now()
	dropped := int64(0)
	for _, item := range items {
		if now.Sub(item.mod) <= outboxRetention && total <= outboxCapacity {
			continue
		}
		if item.name == o.currentSegmentName() {
			continue
		}
		skip := 0
		if item.name < cursor.Segment {
			skip = -1
		} else if item.name == cursor.Segment {
			skip = cursor.Line
		}
		count := countSegmentRecords(filepath.Join(o.dir, item.name), skip)
		if os.Remove(filepath.Join(o.dir, item.name)) == nil {
			total -= item.size
			dropped += count
		}
	}
	if dropped > 0 {
		o.recordDrop(dropped)
		o.lossLog.Do(func() {
			log.Printf("telemetry outbox evicted unacknowledged events due to retention/capacity limits")
		})
	}
}

// countSegmentRecords returns the number of non-empty records after skip.
// A negative skip means the whole segment was already acknowledged.
func countSegmentRecords(path string, skip int) int64 {
	if skip < 0 {
		return 0
	}
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	var count int64
	line := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), maxWideEventBytes+1024)
	for scanner.Scan() {
		line++
		if line > skip && len(strings.TrimSpace(scanner.Text())) > 0 {
			count++
		}
	}
	return count
}

func (o *eventOutbox) recordDrop(n int64) {
	if o.metrics != nil {
		o.metrics.dropped.Add(context.Background(), n)
	}
	o.lossMu.Lock()
	defer o.lossMu.Unlock()
	path := filepath.Join(o.dir, "loss.json")
	var state outboxLoss
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &state)
	}
	if state.Since.IsZero() {
		state.Since = time.Now().UTC()
	}
	state.Dropped += n
	o.writeLossLocked(path, state)
}

type outboxLoss struct {
	Dropped int64     `json:"dropped"`
	Since   time.Time `json:"since"`
}

func (o *eventOutbox) writeLossLocked(path string, state outboxLoss) {
	data, _ := json.Marshal(state)
	tmp, err := os.CreateTemp(o.dir, ".loss-*")
	if err != nil {
		return
	}
	name := tmp.Name()
	defer os.Remove(name)
	_ = tmp.Chmod(0o600)
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	_ = tmp.Close()
	if err == nil {
		_ = os.Rename(name, path)
	}
}

func (o *eventOutbox) emitLossSummary() {
	o.lossMu.Lock()
	path := filepath.Join(o.dir, "loss.json")
	data, err := os.ReadFile(path)
	o.lossMu.Unlock()
	if err != nil {
		return
	}
	var state outboxLoss
	if json.Unmarshal(data, &state) != nil || state.Dropped <= 0 {
		return
	}
	if !o.Enqueue(Event{Timestamp: time.Now(), EventType: "telemetry.data_loss", Status: "dropped", Data: map[string]any{"dropped_count": state.Dropped, "since": state.Since.Format(time.RFC3339Nano)}}) {
		return
	}
	o.lossMu.Lock()
	var current outboxLoss
	if latest, readErr := os.ReadFile(path); readErr == nil && json.Unmarshal(latest, &current) == nil {
		current.Dropped -= state.Dropped
		if current.Dropped <= 0 {
			_ = os.Remove(path)
		} else {
			o.writeLossLocked(path, current)
		}
	}
	o.lossMu.Unlock()
}

func (o *eventOutbox) Shutdown(ctx context.Context) error {
	if o == nil || o.exporter == nil {
		return nil
	}
	o.once.Do(func() { close(o.stop) })
	select {
	case <-o.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	return o.drainOnce(ctx)
}

func sanitizeWideEvent(evt Event) wideRecord {
	// Sanitization must not alter the local JSONL event or the caller's map.
	// Nested maps and slices are rebuilt by sanitizeWideValue below.
	evt.Data = copyAnyMap(evt.Data)
	if decoded, err := hex.DecodeString(evt.TraceID); err != nil || len(decoded) != 16 {
		seed := evt.TraceID
		if evt.FeatureID != "" {
			seed = evt.FeatureID
		}
		if seed == "" {
			evt.TraceID = randomHex(16)
		} else {
			digest := sha256.Sum256([]byte(seed))
			evt.TraceID = hex.EncodeToString(digest[:16])
		}
	}
	raw, _ := json.Marshal(evt)
	original := len(raw)
	truncated := map[string]int{}
	if value, n, cut := sanitizeWideString(evt.Error); true {
		evt.Error = value
		if cut {
			truncated["error"] = n
		}
	}
	for key, value := range evt.Data {
		if forbiddenWideField(key) {
			data, _ := json.Marshal(value)
			truncated[key] = len(data)
			delete(evt.Data, key)
			continue
		}
		sanitized, n, cut := sanitizeWideValue(value)
		evt.Data[key] = sanitized
		if cut {
			truncated[key] = n
		}
	}
	if len(truncated) > 0 {
		if evt.Data == nil {
			evt.Data = map[string]any{}
		}
		evt.Data["telemetry_truncated_fields"] = truncated
	}
	return wideRecord{EventID: uuid.NewString(), SpanID: randomHex(8), Event: evt, Bytes: original}
}

func forbiddenWideField(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	switch key {
	case "prompt", "full_prompt", "system_prompt", "system_instructions", "transcript", "raw_result", "raw_model_result", "source_file", "source_files", "diff", "environment_variables", "config_contents", "authentication_material":
		return true
	default:
		return false
	}
}

func sanitizeWideString(s string) (string, int, bool) {
	original := len(s)
	s = permission.RedactTelemetryText(s)
	s = absolutePathPattern.ReplaceAllString(s, "<user-path>")
	bounded := boundWideText(s)
	return bounded, original, len(s) > len(bounded)
}

func sanitizeWideValue(value any) (any, int, bool) {
	switch v := value.(type) {
	case string:
		s, n, cut := sanitizeWideString(v)
		return s, n, cut
	case []string:
		cut := false
		original := 0
		out := make([]string, len(v))
		for i, item := range v {
			var c bool
			out[i], _, c = sanitizeWideString(item)
			original += len(item)
			cut = cut || c
		}
		return out, original, cut
	case []any:
		cut := false
		original := 0
		out := make([]any, len(v))
		for i, item := range v {
			var n int
			var c bool
			out[i], n, c = sanitizeWideValue(item)
			original += n
			cut = cut || c
		}
		return out, original, cut
	case map[string]any:
		cut := false
		original := 0
		out := make(map[string]any, len(v))
		for key, item := range v {
			if forbiddenWideField(key) {
				data, _ := json.Marshal(item)
				original += len(data)
				cut = true
				continue
			}
			var n int
			var c bool
			out[key], n, c = sanitizeWideValue(item)
			original += n
			cut = cut || c
		}
		return out, original, cut
	case map[string]string:
		cut := false
		original := 0
		out := make(map[string]string, len(v))
		for key, item := range v {
			var c bool
			out[key], _, c = sanitizeWideString(item)
			original += len(item)
			cut = cut || c
		}
		return out, original, cut
	default:
		data, _ := json.Marshal(value)
		return value, len(data), false
	}
}

func boundWideText(s string) string {
	if len(s) <= maxWideFieldBytes {
		return s
	}
	n := maxWideFieldBytes
	for n > 0 && !utf8.ValidString(s[:n]) {
		n--
	}
	return s[:n]
}
