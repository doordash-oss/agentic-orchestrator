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
	"strconv"
	"unicode/utf8"
)

// Tool-result trimming limits for rebuilt history, in characters. A rebuild
// lands on a cold prompt cache, so shrinking old results costs no cache hit.
const (
	trimKeepTurns      = 3
	trimMaxResultChars = 4000
	trimHeadChars      = 1500
	trimTailChars      = 1500
	trimBudgetChars    = 200_000
)

// ClearedToolResult replaces an old tool result dropped to fit the budget.
const ClearedToolResult = "[Old tool result cleared when Agentico restored this session]"

// TrimStats reports what TrimToolResults changed.
type TrimStats struct {
	Trimmed     int // results shortened to their head and tail
	Cleared     int // results replaced by ClearedToolResult
	CharsBefore int // characters of the trimmable results before
	CharsAfter  int // and after
}

// TrimToolResults shortens the tool results a rebuild replays. Results in
// the last trimKeepTurns user turns are kept whole. Older text over
// trimMaxResultChars keeps its head and tail, and when the older results
// still exceed trimBudgetChars the oldest are cleared. The durable
// transcript is untouched and the output depends only on records, so equal
// transcripts rebuild to equal bytes.
func TrimToolResults(records []Record) ([]Record, TrimStats) {
	var stats TrimStats
	protectFrom := protectedTail(records)
	type candidate struct {
		index   int
		data    ContentData
		chars   int
		changed bool
	}
	var candidates []candidate
	total := 0
	for i := range protectFrom {
		rec := &records[i]
		if rec.Kind != KindToolResult || !selectedRecord(rec) {
			continue
		}
		var cd ContentData
		if DecodeRecordPayload(rec, &cd) != nil {
			continue // the converter reports the record itself
		}
		before, after, trimmed := 0, 0, false
		for j := range cd.Content {
			b := &cd.Content[j]
			if b.Type != "tool_result" {
				continue
			}
			n := toolResultChars(b.Content)
			before += n
			if content, ok := trimToolResultContent(b.Content); ok {
				b.Content = content
				trimmed = true
			}
			after += toolResultChars(b.Content)
		}
		if before == 0 {
			continue
		}
		stats.CharsBefore += before
		if trimmed {
			stats.Trimmed++
		}
		candidates = append(candidates, candidate{index: i, data: cd, chars: after, changed: trimmed})
		total += after
	}
	if len(candidates) == 0 {
		return records, stats
	}
	for i := range candidates {
		if total <= trimBudgetChars {
			break
		}
		c := &candidates[i]
		for j := range c.data.Content {
			if c.data.Content[j].Type == "tool_result" {
				c.data.Content[j].Content = jsonString(ClearedToolResult)
			}
		}
		cleared := toolResultsChars(c.data)
		total -= c.chars - cleared
		if c.changed {
			stats.Trimmed--
		}
		c.chars, c.changed = cleared, true
		stats.Cleared++
	}
	stats.CharsAfter = total
	if stats.Trimmed == 0 && stats.Cleared == 0 {
		return records, stats
	}
	out := append([]Record(nil), records...)
	for _, c := range candidates {
		if !c.changed {
			continue
		}
		data, err := json.Marshal(c.data)
		if err != nil {
			continue
		}
		out[c.index].Data = data
	}
	return out, stats
}

// protectedTail returns the index of the first record of the last
// trimKeepTurns user turns, or 0 when there are fewer turns.
func protectedTail(records []Record) int {
	seen := 0
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].Kind == KindUser && records[i].Visibility == VisibilityContent {
			seen++
			if seen == trimKeepTurns {
				return i
			}
		}
	}
	return 0
}

// trimToolResultContent shortens a string result, or each text item of a
// block-array result, that exceeds trimMaxResultChars. Images are kept.
func trimToolResultContent(content json.RawMessage) (json.RawMessage, bool) {
	content = bytes.TrimSpace(content)
	if len(content) == 0 {
		return content, false
	}
	switch content[0] {
	case '"':
		var s string
		if json.Unmarshal(content, &s) != nil {
			return content, false
		}
		if t, ok := trimText(s); ok {
			return jsonString(t), true
		}
	case '[':
		var items []json.RawMessage
		if json.Unmarshal(content, &items) != nil {
			return content, false
		}
		changed := false
		for i, item := range items {
			var text struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if json.Unmarshal(item, &text) != nil || text.Type != "text" {
				continue
			}
			if t, ok := trimText(text.Text); ok {
				text.Text = t
				raw, err := json.Marshal(text)
				if err != nil {
					continue
				}
				items[i] = raw
				changed = true
			}
		}
		if changed {
			if raw, err := json.Marshal(items); err == nil {
				return raw, true
			}
		}
	}
	return content, false
}

func trimText(s string) (string, bool) {
	n := utf8.RuneCountInString(s)
	if n <= trimMaxResultChars {
		return s, false
	}
	runes := []rune(s)
	omitted := n - trimHeadChars - trimTailChars
	return string(runes[:trimHeadChars]) +
		"\n[... " + strconv.Itoa(omitted) + " characters trimmed when Agentico restored this session ...]\n" +
		string(runes[n-trimTailChars:]), true
}

// toolResultChars counts the text characters of a tool result's content;
// images count as zero.
func toolResultChars(content json.RawMessage) int {
	content = bytes.TrimSpace(content)
	if len(content) == 0 {
		return 0
	}
	switch content[0] {
	case '"':
		var s string
		if json.Unmarshal(content, &s) == nil {
			return utf8.RuneCountInString(s)
		}
	case '[':
		var items []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(content, &items) == nil {
			n := 0
			for _, it := range items {
				if it.Type == "text" {
					n += utf8.RuneCountInString(it.Text)
				}
			}
			return n
		}
	}
	return 0
}

func toolResultsChars(cd ContentData) int {
	n := 0
	for _, b := range cd.Content {
		if b.Type == "tool_result" {
			n += toolResultChars(b.Content)
		}
	}
	return n
}

func jsonString(s string) json.RawMessage {
	raw, _ := json.Marshal(s)
	return raw
}
