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
	"strings"
)

// SelectionRules parameterizes SelectEntries for one converter's entry and
// part types. The converters own their wire formats: Decode renders one
// selected record into an entry, and the callbacks expose just enough of the
// entry and its parts for the shared exclusions to run. EntryKey and NonEmpty
// carry each format's duplicate-collapse rules (Claude keys on role, prompt
// and raw blocks and counts a prompt-only entry as non-empty; Codex and
// OpenCode key on role and rendered parts).
type SelectionRules[E any, T any] struct {
	// Decode renders one selected record; errors propagate unchanged.
	Decode func(rec *Record) (E, error)
	// Parts and SetParts expose the entry's parts so the shared
	// interrupted-turn drop and pairing can filter them in place.
	Parts    func(e *E) []T
	SetParts func(e *E, parts []T)
	// PartKind and PartID name a part for the interrupted-turn drop and the
	// tool pairing.
	PartKind func(t T) string
	PartID   func(t T) string
	// ToolUseKind and ToolResultKind are the kind names of a tool-use and a
	// tool-result part in this format.
	ToolUseKind    string
	ToolResultKind string
	// EntryKey keys an entry for the consecutive-duplicate collapse.
	EntryKey func(e E) string
	// NonEmpty reports whether an entry yields anything; empty entries are
	// dropped before the collapse.
	NonEmpty func(e E) bool
}

// SelectEntries applies the shared selection algorithm every converter's
// rebuild is built on: keep the content- and model-only records of the
// conversational kinds, decode each through rules (a decode failure is a
// *ConversionError and aborts the rebuild), drop the tool-use and
// tool-result parts of turns an interrupted marker names, keep only paired
// tool parts (first use per id, first result per id, a result only when its
// use was kept) and collapse consecutive duplicate entries.
func SelectEntries[E any, T any](records []Record, rules SelectionRules[E, T]) ([]E, error) {
	interrupted, err := interruptedTurns(records)
	if err != nil {
		return nil, err
	}
	var entries []E
	for i := range records {
		rec := &records[i]
		if !selectedRecord(rec) {
			continue
		}
		e, err := rules.Decode(rec)
		if err != nil {
			return nil, err
		}
		if rec.TurnID != "" && interrupted[rec.TurnID] {
			rules.SetParts(&e, dropKindParts(rules.Parts(&e), rules.PartKind, rules.ToolUseKind, rules.ToolResultKind))
		}
		entries = append(entries, e)
	}
	entries = PairToolParts(entries, rules)
	return CollapseDuplicateEntries(entries, rules), nil
}

// PairToolParts keeps a tool use only when a later result answers it and a
// result only when an earlier use asked for it; the first of each per id
// wins so a resumed request never repeats a tool id.
func PairToolParts[E any, T any](entries []E, rules SelectionRules[E, T]) []E {
	usesSeen := map[string]bool{}
	answered := map[string]bool{}
	for i := range entries {
		for _, t := range rules.Parts(&entries[i]) {
			switch rules.PartKind(t) {
			case rules.ToolUseKind:
				usesSeen[rules.PartID(t)] = true
			case rules.ToolResultKind:
				if usesSeen[rules.PartID(t)] {
					answered[rules.PartID(t)] = true
				}
			}
		}
	}
	keptUse := map[string]bool{}
	keptResult := map[string]bool{}
	for i := range entries {
		var kept []T
		for _, t := range rules.Parts(&entries[i]) {
			switch rules.PartKind(t) {
			case rules.ToolUseKind:
				if !answered[rules.PartID(t)] || keptUse[rules.PartID(t)] {
					continue
				}
				keptUse[rules.PartID(t)] = true
			case rules.ToolResultKind:
				if !answered[rules.PartID(t)] || !keptUse[rules.PartID(t)] || keptResult[rules.PartID(t)] {
					continue
				}
				keptResult[rules.PartID(t)] = true
			}
			kept = append(kept, t)
		}
		rules.SetParts(&entries[i], kept)
	}
	return entries
}

// CollapseDuplicateEntries drops entries that yield nothing and any entry
// whose key equals the previous kept entry's; only consecutive duplicates
// collapse.
func CollapseDuplicateEntries[E any, T any](entries []E, rules SelectionRules[E, T]) []E {
	var out []E
	prev := ""
	for _, e := range entries {
		if !rules.NonEmpty(e) {
			continue
		}
		key := rules.EntryKey(e)
		if key == prev {
			continue
		}
		prev = key
		out = append(out, e)
	}
	return out
}

// selectedRecord reports whether a record enters a rebuild: content and
// model-only visibility, and the conversational kinds.
func selectedRecord(rec *Record) bool {
	switch rec.Visibility {
	case VisibilityContent, VisibilityModelOnly:
	default:
		return false
	}
	switch rec.Kind {
	case KindUser, KindAssistant, KindToolUse, KindToolResult, KindNote:
		return true
	}
	return false
}

// interruptedTurns collects the turns named by interrupted markers.
func interruptedTurns(records []Record) (map[string]bool, error) {
	turns := map[string]bool{}
	for i := range records {
		rec := &records[i]
		if rec.Kind != KindMarker {
			continue
		}
		var m MarkerData
		if err := DecodeRecordPayload(rec, &m); err != nil {
			return nil, err
		}
		if m.Marker == MarkerInterrupted && rec.TurnID != "" {
			turns[rec.TurnID] = true
		}
	}
	return turns, nil
}

// dropKindParts removes the parts whose kind is one of kinds.
func dropKindParts[T any](parts []T, kind func(T) string, kinds ...string) []T {
	out := parts[:0:0]
	for _, p := range parts {
		drop := false
		for _, k := range kinds {
			if kind(p) == k {
				drop = true
			}
		}
		if !drop {
			out = append(out, p)
		}
	}
	return out
}

// DecodeRecordPayload decodes a record's payload into v. An absent or null
// payload and an undecodable one are *ConversionError: deterministic
// properties of the history, not I/O faults.
func DecodeRecordPayload(rec *Record, v any) error {
	data := bytes.TrimSpace(rec.Data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return &ConversionError{Seq: rec.Seq, Reason: fmt.Sprintf("%s record has no payload", rec.Kind)}
	}
	if err := json.Unmarshal(data, v); err != nil {
		return &ConversionError{Seq: rec.Seq, Reason: fmt.Sprintf("decode %s payload: %v", rec.Kind, err)}
	}
	return nil
}

// OmittedImagePlaceholder is the placeholder a converter writes for an image
// whose data could not be restored; the three native formats share the
// wording.
func OmittedImagePlaceholder(mediaType string) string {
	if mediaType == "" {
		mediaType = "image"
	}
	return "[image omitted: " + mediaType + " could not be restored]"
}

// ToolResultItemPlaceholder is the text a converter writes for a tool-result
// content item it has no native form for, so one unrecognised block does not
// fail the whole conversion. A tool_reference names the tool it points at.
func ToolResultItemPlaceholder(itemType string, item json.RawMessage) string {
	if itemType == "tool_reference" {
		var ref struct {
			ToolName string `json:"tool_name"`
		}
		if json.Unmarshal(item, &ref) == nil && ref.ToolName != "" {
			return "[tool reference: " + ref.ToolName + "]"
		}
		return "[tool reference]"
	}
	if itemType == "" {
		itemType = "unknown"
	}
	return "[" + itemType + " content omitted: could not be restored]"
}

// ValidateNativeRebuildInput checks a rebuild request that targets a native
// session id: a non-empty, path-free session id and a working directory.
// prefix names the caller ("claude session rebuild") in the error text.
func ValidateNativeRebuildInput(in RebuildInput, prefix string) error {
	if in.NativeSessionID == "" {
		return errors.New(prefix + ": empty native session id")
	}
	if strings.ContainsAny(in.NativeSessionID, `/\`) || in.NativeSessionID == "." || in.NativeSessionID == ".." {
		return fmt.Errorf("%s: unsafe native session id %q", prefix, in.NativeSessionID)
	}
	if in.WorkDir == "" {
		return errors.New(prefix + ": empty working directory")
	}
	return nil
}
