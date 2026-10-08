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
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func appendProvider(t *testing.T, store *transcriptStore, gen int64, kind RecordKind, providerID, text string) (Record, bool) {
	t.Helper()
	data, _ := json.Marshal(UserData{Text: text})
	rec, existing, err := store.appendRecord(Record{
		ID:         ProviderRecordID(store.conversationID, gen, kind, providerID),
		Generation: gen,
		Kind:       kind,
		Visibility: VisibilityContent,
		Data:       data,
	})
	if err != nil {
		t.Fatalf("appendRecord: %v", err)
	}
	return rec, existing
}

func TestTranscriptStore_ProviderItemAppendedTwiceCommitsOnce(t *testing.T) {
	dir := t.TempDir()
	store := openTestStore(t, dir)
	first, existing := appendProvider(t, store, 0, KindAssistant, "msg_1", "hello")
	if existing {
		t.Fatal("first append reported existing")
	}
	again, existing := appendProvider(t, store, 0, KindAssistant, "msg_1", "hello")
	if !existing || again.Seq != first.Seq || again.ID != first.ID {
		t.Fatalf("second append = %+v existing=%v, want the committed record", again, existing)
	}
	use, _ := appendProvider(t, store, 0, KindToolUse, "item-1-2", "use")
	result, _ := appendProvider(t, store, 0, KindToolResult, "item-1-2", "result")
	if use.ID == result.ID || use.Seq == result.Seq {
		t.Fatalf("tool_use and tool_result sharing an item id collided: %q", use.ID)
	}
	if store.head() != 3 {
		t.Fatalf("head = %d, want 3", store.head())
	}
	_ = store.close()

	reopened := openTestStore(t, dir)
	if got := ProviderRecordID("conv-1", 0, KindAssistant, "msg_1"); got != first.ID {
		t.Fatalf("id not stable: %q vs %q", got, first.ID)
	}
	if rec, existing := appendProvider(t, reopened, 0, KindAssistant, "msg_1", "hello"); !existing || rec.Seq != 1 {
		t.Fatalf("append after reopen = %+v existing=%v, want seq 1 existing", rec, existing)
	}
	if reopened.head() != 3 {
		t.Fatalf("head after reopen = %d, want 3", reopened.head())
	}
}

func TestTranscriptStore_ProviderIDDedupPrecedesGenerationFence(t *testing.T) {
	store := openTestStore(t, t.TempDir())
	store.setGeneration(1)
	first, _ := appendProvider(t, store, 1, KindAssistant, "msg_1", "hello")
	store.setGeneration(2)
	data, _ := json.Marshal(UserData{Text: "hello"})
	rec, existing, err := store.appendRecord(Record{ID: first.ID, Generation: 1, Kind: KindAssistant, Visibility: VisibilityContent, Data: data})
	if err != nil || !existing || rec.Seq != first.Seq {
		t.Fatalf("retired duplicate = %+v existing=%v err=%v, want committed record", rec, existing, err)
	}
}

func writeLines(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func transcriptLines(t *testing.T, dir string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, transcriptFileName))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func TestTranscriptStore_CorruptMidFileLineKeepsPrefixAndPreservesOriginal(t *testing.T) {
	for _, damage := range []string{"unparseable", "seq gap"} {
		t.Run(damage, func(t *testing.T) {
			dir := t.TempDir()
			store := openTestStore(t, dir)
			for i := range 8 {
				appendText(t, store, 0, KindAssistant, "", fmt.Sprintf("m%d", i+1))
			}
			_ = store.close()
			lines := transcriptLines(t, dir)
			const k = 5
			switch damage {
			case "unparseable":
				lines[k-1] = `{"seq":5,"kind":"assist` + "\x00garbage"
			case "seq gap":
				lines[k-1] = strings.Replace(lines[k-1], `"seq":5`, `"seq":9`, 1)
			}
			writeLines(t, filepath.Join(dir, transcriptFileName), lines)
			original, _ := os.ReadFile(filepath.Join(dir, transcriptFileName))

			reopened := openTestStore(t, dir)
			if head := reopened.head(); head != k-1 {
				t.Fatalf("head = %d, want %d", head, k-1)
			}
			note := reopened.takeRecovery()
			if note == nil || note.AfterSeq != k-1 || note.Unread != 8-k+1 {
				t.Fatalf("recovery = %+v, want after %d unread %d", note, k-1, 8-k+1)
			}
			if !strings.HasPrefix(filepath.Base(note.PreservedPath), transcriptFileName+".corrupt-") || filepath.Dir(note.PreservedPath) != dir {
				t.Fatalf("preserved path = %q", note.PreservedPath)
			}
			preserved, err := os.ReadFile(note.PreservedPath)
			if err != nil || !bytes.Equal(preserved, original) {
				t.Fatalf("preserved copy differs from original (err %v)", err)
			}
			if reopened.takeRecovery() != nil {
				t.Fatal("recovery note returned twice")
			}
			if rec := appendText(t, reopened, 0, KindAssistant, "", "after"); rec.Seq != k {
				t.Fatalf("next seq = %d, want %d", rec.Seq, k)
			}
		})
	}
}

func TestTranscriptStore_TornTailIsTruncatedWithoutCopyOrNote(t *testing.T) {
	dir := t.TempDir()
	store := openTestStore(t, dir)
	appendText(t, store, 0, KindAssistant, "", "kept")
	_ = store.close()
	f, err := os.OpenFile(filepath.Join(dir, transcriptFileName), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"seq":2,"kind":"assist`)
	_ = f.Close()
	reopened := openTestStore(t, dir)
	if reopened.takeRecovery() != nil {
		t.Fatal("torn tail produced a recovery note")
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "*.corrupt-*"))
	if len(matches) != 0 {
		t.Fatalf("torn tail preserved a copy: %v", matches)
	}
}

func readIndexLines(t *testing.T, dir string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, indexFileName))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func assertIndexMatchesStore(t *testing.T, dir string, store *transcriptStore) {
	t.Helper()
	lines := readIndexLines(t, dir)
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(lines) != len(store.offsets)+1 {
		t.Fatalf("index has %d lines, want %d offsets + size", len(lines), len(store.offsets))
	}
	for i, off := range store.offsets {
		if lines[i] != strconv.FormatInt(off, 10) {
			t.Fatalf("index line %d = %q, want %d", i, lines[i], off)
		}
	}
	info, _ := os.Stat(filepath.Join(dir, transcriptFileName))
	if lines[len(lines)-1] != "size "+strconv.FormatInt(info.Size(), 10) {
		t.Fatalf("size line = %q, want size %d", lines[len(lines)-1], info.Size())
	}
}

func TestTranscriptStore_IndexAppendsOneOffsetPerRecordWithoutRewritingPrefix(t *testing.T) {
	dir := t.TempDir()
	store := openTestStore(t, dir)
	var prefix []byte
	for i := range 12 {
		appendText(t, store, 0, KindAssistant, "", fmt.Sprintf("m%d", i+1))
		data, _ := os.ReadFile(filepath.Join(dir, indexFileName))
		body := data[:bytes.LastIndex(data[:len(data)-1], []byte("\n"))+1]
		if !bytes.HasPrefix(body, prefix) {
			t.Fatalf("append %d rewrote earlier index lines:\n was %q\n now %q", i+1, prefix, body)
		}
		prefix = body
	}
	assertIndexMatchesStore(t, dir, store)
}

func TestTranscriptStore_ConcurrentAppendsKeepIndexConsistent(t *testing.T) {
	dir := t.TempDir()
	store := openTestStore(t, dir)
	var wg sync.WaitGroup
	for w := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 20 {
				data, _ := json.Marshal(UserData{Text: fmt.Sprintf("w%d-%d", w, i)})
				if _, _, err := store.appendRecord(Record{Kind: KindAssistant, Visibility: VisibilityContent, Data: data}); err != nil {
					t.Errorf("appendRecord: %v", err)
				}
			}
		}()
	}
	wg.Wait()
	assertIndexMatchesStore(t, dir, store)
	_ = store.close()
	before, _ := os.ReadFile(filepath.Join(dir, indexFileName))
	reopened := openTestStore(t, dir)
	after, _ := os.ReadFile(filepath.Join(dir, indexFileName))
	if !bytes.Equal(before, after) {
		t.Fatal("a consistent index was rewritten on open")
	}
	assertIndexMatchesStore(t, dir, reopened)
}

func TestTranscriptStore_RewritesDisagreeingIndexOnOpen(t *testing.T) {
	damages := map[string]func([]string) []string{
		"missing":      nil,
		"truncated":    func(l []string) []string { return l[:4] },
		"over-long":    func(l []string) []string { return append(append([]string{}, l[:len(l)-1]...), "9999", l[len(l)-1]) },
		"wrong offset": func(l []string) []string { l[3] = "17"; return l },
		"wrong size":   func(l []string) []string { l[len(l)-1] = "size 3"; return l },
		"legacy json":  func([]string) []string { return []string{`{"size":0,"offsets":[]}`} },
	}
	for name, damage := range damages {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			store := openTestStore(t, dir)
			for i := range 7 {
				appendText(t, store, 0, KindAssistant, "", fmt.Sprintf("m%d", i+1))
			}
			_ = store.close()
			indexPath := filepath.Join(dir, indexFileName)
			if damage == nil {
				_ = os.Remove(indexPath)
			} else {
				writeLines(t, indexPath, damage(readIndexLines(t, dir)))
			}
			reopened := openTestStore(t, dir)
			assertIndexMatchesStore(t, dir, reopened)
			page, err := reopened.page(PageQuery{Before: 6, HasBefore: true, Limit: 2})
			if err != nil {
				t.Fatal(err)
			}
			if got := recordTexts(t, page.Items); fmt.Sprint(got) != "[m4 m5]" {
				t.Fatalf("paged read after rewrite = %v", got)
			}
			appendText(t, reopened, 0, KindAssistant, "", "m8")
			assertIndexMatchesStore(t, dir, reopened)
		})
	}
}

func TestTranscriptStore_RangedPageRefusesCursorsBeyondTheStoredRange(t *testing.T) {
	store := openTestStore(t, t.TempDir())
	for i := range 20 {
		appendText(t, store, 0, KindAssistant, "", fmt.Sprintf("m%d", i+1))
	}
	valid := []struct {
		query                 PageQuery
		count                 int
		moreBefore, moreAfter bool
	}{
		{PageQuery{After: 20, HasAfter: true}, 0, false, false},
		{PageQuery{Before: 1, HasBefore: true}, 0, false, false},
		{PageQuery{Before: 21, HasBefore: true}, 20, false, false},
	}
	for _, tc := range valid {
		page, err := store.rangedPage(tc.query)
		if err != nil {
			t.Fatalf("rangedPage(%+v) err = %v", tc.query, err)
		}
		if len(page.Items) != tc.count || page.HasMoreBefore != tc.moreBefore || page.HasMoreAfter != tc.moreAfter || page.HeadSeq != 20 {
			t.Fatalf("rangedPage(%+v) = {n:%d before:%v after:%v head:%d}", tc.query, len(page.Items), page.HasMoreBefore, page.HasMoreAfter, page.HeadSeq)
		}
	}
	for _, q := range []PageQuery{
		{After: 21, HasAfter: true},
		{Before: 22, HasBefore: true},
	} {
		var oor *CursorOutOfRangeError
		if _, err := store.rangedPage(q); !errors.As(err, &oor) || oor.HeadSeq != 20 {
			t.Fatalf("rangedPage(%+v) err = %v, want CursorOutOfRangeError head 20", q, err)
		}
	}
}

func TestTranscriptStore_ReplayIsBoundedAtMaxReplay(t *testing.T) {
	store := openTestStore(t, t.TempDir())
	for i := range maxReplay + 3 {
		appendText(t, store, 0, KindAssistant, "", fmt.Sprintf("m%d", i+1))
	}
	head := store.head()
	recs, ok, err := store.replayAfter(head - maxReplay)
	if err != nil || !ok || len(recs) != maxReplay || recs[0].Seq != head-maxReplay+1 {
		t.Fatalf("replay exactly %d behind = %d records ok=%v err=%v", maxReplay, len(recs), ok, err)
	}
	if _, ok, err := store.replayAfter(head - maxReplay - 1); err != nil || ok {
		t.Fatalf("replay %d behind ok=%v err=%v, want refused", maxReplay+1, ok, err)
	}
	if recs, ok, _ := store.replayAfter(head); !ok || len(recs) != 0 {
		t.Fatalf("replay at head = %d records ok=%v", len(recs), ok)
	}
}
