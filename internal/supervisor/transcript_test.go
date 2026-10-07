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
	"sync"
	"testing"
	"time"
)

func openTestStore(t *testing.T, dir string) *transcriptStore {
	t.Helper()
	store, err := openTranscriptStore(dir, "conv-1", randomID, time.Now)
	if err != nil {
		t.Fatalf("openTranscriptStore: %v", err)
	}
	t.Cleanup(func() { _ = store.close() })
	return store
}

func appendText(t *testing.T, store *transcriptStore, gen int64, kind RecordKind, cmid, text string) Record {
	t.Helper()
	data, _ := json.Marshal(UserData{Text: text})
	rec, _, err := store.appendRecord(Record{Generation: gen, Kind: kind, Visibility: VisibilityContent, ClientMessageID: cmid, Data: data})
	if err != nil {
		t.Fatalf("appendRecord: %v", err)
	}
	return rec
}

func recordTexts(t *testing.T, recs []Record) []string {
	t.Helper()
	out := make([]string, 0, len(recs))
	for _, rec := range recs {
		var data UserData
		if err := json.Unmarshal(rec.Data, &data); err != nil {
			t.Fatalf("decode record %d: %v", rec.Seq, err)
		}
		out = append(out, data.Text)
	}
	return out
}

func TestTranscriptStore_ConcurrentAppendsAllocateStrictlyIncreasingSeqAndSurviveReopen(t *testing.T) {
	dir := t.TempDir()
	store := openTestStore(t, dir)
	const writers, perWriter = 8, 25
	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range perWriter {
				data, _ := json.Marshal(UserData{Text: fmt.Sprintf("w%d-%d", w, i)})
				if _, _, err := store.appendRecord(Record{Kind: KindAssistant, Visibility: VisibilityContent, Data: data}); err != nil {
					t.Errorf("appendRecord: %v", err)
				}
			}
		}()
	}
	wg.Wait()
	page, err := store.page(PageQuery{HasAfter: true, Limit: maxPageLimit})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != writers*perWriter {
		t.Fatalf("records = %d, want %d", len(page.Items), writers*perWriter)
	}
	ids := map[string]bool{}
	for i, rec := range page.Items {
		if rec.Seq != int64(i+1) {
			t.Fatalf("record %d has seq %d", i, rec.Seq)
		}
		if ids[rec.ID] {
			t.Fatalf("duplicate record id %q", rec.ID)
		}
		ids[rec.ID] = true
	}
	want := recordTexts(t, page.Items)
	_ = store.close()

	reopened := openTestStore(t, dir)
	again, err := reopened.page(PageQuery{HasAfter: true, Limit: maxPageLimit})
	if err != nil {
		t.Fatal(err)
	}
	got := recordTexts(t, again.Items)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("reopened order differs:\n got %v\nwant %v", got, want)
	}
}

func TestTranscriptStore_RebuildsMissingOrTruncatedIndex(t *testing.T) {
	for _, damage := range []string{"missing", "truncated"} {
		t.Run(damage, func(t *testing.T) {
			dir := t.TempDir()
			store := openTestStore(t, dir)
			for i := range 7 {
				appendText(t, store, 0, KindAssistant, "", fmt.Sprintf("m%d", i+1))
			}
			_ = store.close()
			indexPath := filepath.Join(dir, indexFileName)
			switch damage {
			case "missing":
				if err := os.Remove(indexPath); err != nil {
					t.Fatal(err)
				}
			case "truncated":
				data, err := os.ReadFile(indexPath)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(indexPath, data[:len(data)/2], 0o644); err != nil {
					t.Fatal(err)
				}
			}
			reopened := openTestStore(t, dir)
			if _, err := os.Stat(indexPath); err != nil {
				t.Fatalf("index not rebuilt: %v", err)
			}
			page, err := reopened.page(PageQuery{Before: 6, HasBefore: true, Limit: 2})
			if err != nil {
				t.Fatal(err)
			}
			if got := recordTexts(t, page.Items); fmt.Sprint(got) != "[m4 m5]" {
				t.Fatalf("paged read after rebuild = %v, want [m4 m5]", got)
			}
			appendText(t, reopened, 0, KindAssistant, "", "m8")
			if head := reopened.head(); head != 8 {
				t.Fatalf("head after rebuild append = %d, want 8", head)
			}
		})
	}
}

func TestTranscriptStore_DropsTornTrailingLine(t *testing.T) {
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
	if head := reopened.head(); head != 1 {
		t.Fatalf("head = %d, want 1 after dropping the torn line", head)
	}
	rec := appendText(t, reopened, 0, KindAssistant, "", "next")
	if rec.Seq != 2 {
		t.Fatalf("seq after torn tail = %d, want 2", rec.Seq)
	}
	page, _ := reopened.page(PageQuery{})
	if got := recordTexts(t, page.Items); fmt.Sprint(got) != "[kept next]" {
		t.Fatalf("records = %v", got)
	}
}

func TestTranscriptStore_RetiredGenerationAppendIsRejectedAndLeavesFileUnchanged(t *testing.T) {
	dir := t.TempDir()
	store := openTestStore(t, dir)
	store.setGeneration(1)
	appendText(t, store, 1, KindAssistant, "", "gen1")
	store.setGeneration(2)
	before, err := os.ReadFile(filepath.Join(dir, transcriptFileName))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(UserData{Text: "late"})
	_, _, err = store.appendRecord(Record{Generation: 1, Kind: KindAssistant, Visibility: VisibilityContent, Data: data})
	if !errors.Is(err, ErrRetiredGeneration) {
		t.Fatalf("retired append err = %v, want ErrRetiredGeneration", err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, transcriptFileName))
	if !bytes.Equal(before, after) {
		t.Fatal("retired-generation append changed the transcript file")
	}
	if store.head() != 1 {
		t.Fatalf("head = %d, want 1", store.head())
	}
}

func TestTranscriptStore_RepeatedClientMessageIDReturnsCommittedRecord(t *testing.T) {
	dir := t.TempDir()
	store := openTestStore(t, dir)
	first := appendText(t, store, 0, KindUser, "cm-1", "hello")
	data, _ := json.Marshal(UserData{Text: "hello"})
	again, existing, err := store.appendRecord(Record{Kind: KindUser, Visibility: VisibilityContent, ClientMessageID: "cm-1", Data: data})
	if err != nil || !existing {
		t.Fatalf("repeat append = existing %v err %v, want existing record", existing, err)
	}
	if again.Seq != first.Seq || again.ID != first.ID {
		t.Fatalf("repeat returned %+v, want %+v", again, first)
	}
	if store.head() != 1 {
		t.Fatalf("head = %d, want 1", store.head())
	}
	_ = store.close()
	reopened := openTestStore(t, dir)
	if rec, ok := reopened.lookupClientMessage("cm-1"); !ok || rec.Seq != 1 {
		t.Fatalf("client message index not rebuilt on open: %+v %v", rec, ok)
	}
}

func TestTranscriptStore_PagingHonoursCursorsLimitsAndFlags(t *testing.T) {
	store := openTestStore(t, t.TempDir())
	for i := range 620 {
		appendText(t, store, 0, KindAssistant, "", fmt.Sprintf("m%d", i+1))
	}
	cases := []struct {
		name                  string
		query                 PageQuery
		count                 int
		first, last           int64
		moreBefore, moreAfter bool
	}{
		{"newest default page", PageQuery{}, 100, 521, 620, true, false},
		{"newest max page", PageQuery{Limit: 500}, 500, 121, 620, true, false},
		{"before cursor", PageQuery{Before: 101, HasBefore: true, Limit: 50}, 50, 51, 100, true, true},
		{"before near start", PageQuery{Before: 3, HasBefore: true}, 2, 1, 2, false, true},
		{"after zero", PageQuery{After: 0, HasAfter: true, Limit: 10}, 10, 1, 10, false, true},
		{"after cursor", PageQuery{After: 600, HasAfter: true}, 20, 601, 620, true, false},
		{"after head", PageQuery{After: 620, HasAfter: true}, 0, 0, 0, false, false},
		{"after beyond head", PageQuery{After: 900, HasAfter: true}, 0, 0, 0, false, false},
		{"before first", PageQuery{Before: 1, HasBefore: true}, 0, 0, 0, false, false},
		{"before beyond head", PageQuery{Before: 5000, HasBefore: true}, 0, 0, 0, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page, err := store.page(tc.query)
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Items) != tc.count || page.FirstSeq != tc.first || page.LastSeq != tc.last ||
				page.HasMoreBefore != tc.moreBefore || page.HasMoreAfter != tc.moreAfter || page.HeadSeq != 620 {
				t.Fatalf("page = {n:%d first:%d last:%d before:%v after:%v head:%d}, want {n:%d first:%d last:%d before:%v after:%v head:620}",
					len(page.Items), page.FirstSeq, page.LastSeq, page.HasMoreBefore, page.HasMoreAfter, page.HeadSeq,
					tc.count, tc.first, tc.last, tc.moreBefore, tc.moreAfter)
			}
		})
	}
	for _, bad := range []PageQuery{
		{Before: 5, HasBefore: true, After: 1, HasAfter: true},
		{Limit: 501},
		{Limit: -1},
	} {
		if _, err := store.page(bad); !errors.Is(err, ErrInvalidPageQuery) {
			t.Fatalf("page(%+v) err = %v, want ErrInvalidPageQuery", bad, err)
		}
	}
}
