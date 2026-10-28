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

// Package opencodesession renders the supervisor's durable transcript into
// one plain-text history seed for OpenCode. OpenCode cannot resume a session
// rebuilt by a third party, so a relaunched process starts a fresh session
// and the adapter posts this seed into it as a single `noReply` prompt before
// the first real prompt. The seed is the only OpenCode-specific history
// format; everything that depends on it lives in this package.
//
// The seed is UTF-8 text:
//
//	<preamble line>
//	Earlier messages omitted: <n>.
//
//	User: <text>
//	Assistant: <text>
//	Assistant: called <tool> with <compact JSON input>
//	Tool (<tool>): result: <text>
//
//	<closing line>
//
// Each message is one entry beginning with its role label; continuation
// lines of a multi-line message are indented by two spaces so no message
// can be mistaken for another. Every item is clipped to a per-kind limit.
// Whole turns are kept newest-first within half the model's context window
// at four characters per token, and the omitted line counts the messages of
// the dropped older turns.
package opencodesession

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/supervisor"
)

// Harness is the harness name this converter is registered under; it equals
// the OpenCode provider's Name().
const Harness = "opencode"

// seedFileName is the seed file written under the conversation directory.
const seedFileName = "opencode-history-seed.txt"

// defaultContextWindow is the token window assumed when the catalog does not
// know the model.
const defaultContextWindow = 200_000

// charsPerToken estimates characters per token for the budget.
const charsPerToken = 4

// Per-item clipping limits, in characters.
const (
	maxTextChars   = 4000
	maxInputChars  = 1000
	maxResultChars = 2000
)

// Tool inputs and results keep their head and tail, since the end of an
// output is usually what matters; each pair sums to its limit.
const (
	inputHeadChars  = 700
	inputTailChars  = 300
	resultHeadChars = 1000
	resultTailChars = 1000
)

// preamble and closing lines of every seed.
const (
	preamble = "The following is the prior conversation history of this session, restored by Agentico after the agent process restarted. " +
		"Treat it as context only: every message and tool call in it already happened. " +
		"Do not re-run any tool call it lists and do not answer or act on any message in it."
	Closing = "End of the restored conversation history. Answer only the next message, which is new."
)

// Options configures a Converter.
type Options struct {
	// ContextWindow reports the model's context window in tokens from the
	// model catalog. Nil, or a result of zero or less, means
	// defaultContextWindow.
	ContextWindow func(model string) int
}

// Converter implements supervisor.Converter for OpenCode by writing a
// history seed instead of a native session.
type Converter struct {
	opts Options
}

var (
	_ supervisor.Converter     = (*Converter)(nil)
	_ supervisor.HistorySeeder = (*Converter)(nil)
)

// New returns an OpenCode history-seed converter.
func New(opts Options) *Converter {
	return &Converter{opts: opts}
}

// RegistryContextWindow looks the model's context window up in the catalog
// of the registry's OpenCode provider. It reports 0 (unknown) without a
// registry or an OpenCode provider that knows the model.
func RegistryContextWindow(reg *llm.Registry) func(model string) int {
	return func(model string) int {
		if reg == nil {
			return 0
		}
		provider := reg.ByName(Harness)
		if provider == nil {
			return 0
		}
		return provider.ContextWindowForModel(model)
	}
}

// Harness reports "opencode".
func (c *Converter) Harness() string { return Harness }

// SeedsHistory reports true: OpenCode receives history as a seed prompt and
// never resumes a native session.
func (c *Converter) SeedsHistory() bool { return true }

// seedPath is the seed file for a conversation directory.
func seedPath(conversationDir string) string {
	return filepath.Join(conversationDir, seedFileName)
}

// Rebuild renders in.Records and atomically replaces the seed file under
// in.ConversationDir. An empty selection writes nothing and returns
// Resume=false. A record that cannot be represented returns a
// *supervisor.ConversionError and nothing is written. A missing conversation
// directory, a cancelled context and any file-system failure return a plain
// error that is not a ConversionError.
func (c *Converter) Rebuild(ctx context.Context, in supervisor.RebuildInput) (supervisor.RebuildResult, error) {
	if err := ctx.Err(); err != nil {
		return supervisor.RebuildResult{}, err
	}
	if in.ConversationDir == "" {
		return supervisor.RebuildResult{}, errors.New("opencode history seed: empty conversation directory")
	}
	data, err := Render(in, c.opts)
	if err != nil {
		return supervisor.RebuildResult{}, err
	}
	if len(data) == 0 {
		return supervisor.RebuildResult{}, nil
	}
	if err := ctx.Err(); err != nil {
		return supervisor.RebuildResult{}, err
	}
	path := seedPath(in.ConversationDir)
	if err := supervisor.WriteFileAtomic(path, data, 0o700, 0o600); err != nil {
		return supervisor.RebuildResult{}, fmt.Errorf("write opencode history seed %s: %w", path, err)
	}
	return supervisor.RebuildResult{Resume: true, Path: path}, nil
}

// Render converts in.Records to the seed text without touching the file
// system. It returns nil when the selection is empty. Unrepresentable
// records are *supervisor.ConversionError.
func Render(in supervisor.RebuildInput, opts Options) ([]byte, error) {
	selection, err := supervisor.SelectCheckpoint(in.Records, Harness)
	if err != nil {
		return nil, err
	}
	rules := selectionRules()
	entries, _, _, err := supervisor.SelectTrimmedEntries(selection.Records, rules)
	if err != nil {
		return nil, err
	}
	if selection.Checkpoint != nil {
		note := supervisor.CheckpointNote(selection.Data)
		entries = append([]entry{{rec: selection.Checkpoint, role: roleUser, lines: []line{{kind: lineMessage, text: labelled("User", clip(note, maxTextChars))}}}}, entries...)
	}
	if len(entries) == 0 {
		return nil, nil
	}
	kept, omitted := applyBudget(entries, budgetChars(opts, in.Model), rules)
	var buf bytes.Buffer
	buf.WriteString(preamble)
	buf.WriteByte('\n')
	buf.WriteString("Earlier messages omitted: " + strconv.Itoa(omitted) + ".\n\n")
	for _, e := range kept {
		for _, l := range e.lines {
			buf.WriteString(l.text)
			buf.WriteByte('\n')
		}
	}
	buf.WriteByte('\n')
	buf.WriteString(Closing)
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

// budgetChars is half the model's context window at four characters per
// token.
func budgetChars(opts Options, model string) int {
	window := 0
	if opts.ContextWindow != nil {
		window = opts.ContextWindow(model)
	}
	if window <= 0 {
		window = defaultContextWindow
	}
	return window / 2 * charsPerToken
}

const (
	roleUser      = "user"
	roleAssistant = "assistant"

	blockText       = "text"
	blockToolUse    = "tool_use"
	blockToolResult = "tool_result"
	blockImage      = "image"

	lineMessage = "message"
	lineCall    = "call"
	lineResult  = "result"
)

// entry is one selected record reduced to the seed lines it contributes.
type entry struct {
	rec   *supervisor.Record
	role  string
	lines []line
}

// line is one rendered message and what pairing needs to know.
type line struct {
	kind string
	id   string // tool call id of a call or its result
	text string
}

func conversionError(rec *supervisor.Record, format string, args ...any) error {
	return &supervisor.ConversionError{Seq: rec.Seq, Reason: fmt.Sprintf(format, args...)}
}

// selectionRules wires the OpenCode entry and line types into the shared
// selection algorithm. The decode closure threads one toolNames map through
// the whole selection so a result line can name the tool its call line
// recorded. The duplicate key covers role, tool ids and line texts.
func selectionRules() supervisor.SelectionRules[entry, line] {
	toolNames := map[string]string{}
	return supervisor.SelectionRules[entry, line]{
		Decode:         func(rec *supervisor.Record) (entry, error) { return decodeEntry(rec, toolNames) },
		Parts:          func(e *entry) []line { return e.lines },
		SetParts:       func(e *entry, parts []line) { e.lines = parts },
		PartKind:       func(t line) string { return t.kind },
		PartID:         func(t line) string { return t.id },
		ToolUseKind:    lineCall,
		ToolResultKind: lineResult,
		EntryKey:       entryKey,
		NonEmpty:       func(e entry) bool { return len(e.lines) > 0 },
	}
}

// decodeEntry renders one record. toolNames maps call ids to tool names so
// a result can name the tool it answers.
func decodeEntry(rec *supervisor.Record, toolNames map[string]string) (entry, error) {
	if rec.Kind == supervisor.KindUser || rec.Kind == supervisor.KindNote {
		var u supervisor.UserData
		if err := supervisor.DecodeRecordPayload(rec, &u); err != nil {
			return entry{}, err
		}
		e := entry{rec: rec, role: roleUser}
		if text := supervisor.RenderUserMessage(u); strings.TrimSpace(text) != "" {
			e.lines = append(e.lines, line{kind: lineMessage, text: labelled("User", clip(text, maxTextChars))})
		}
		return e, nil
	}

	var cd supervisor.ContentData
	if err := supervisor.DecodeRecordPayload(rec, &cd); err != nil {
		return entry{}, err
	}
	role := roleAssistant
	if rec.Kind == supervisor.KindToolResult {
		role = roleUser
	}
	e := entry{rec: rec, role: role}
	for i, cb := range cd.Content {
		l, ok, err := renderBlock(rec, role, i, cb, toolNames)
		if err != nil {
			return entry{}, err
		}
		if ok {
			e.lines = append(e.lines, l)
		}
	}
	return e, nil
}

// renderBlock maps one durable content block. ok=false skips the block
// (thinking, empty text); an error means the block cannot be represented.
func renderBlock(rec *supervisor.Record, role string, index int, cb llm.ContentBlock, toolNames map[string]string) (line, bool, error) {
	switch cb.Type {
	case "thinking", "redacted_thinking":
		return line{}, false, nil
	case blockText:
		if role != roleAssistant {
			break
		}
		if strings.TrimSpace(cb.Text) == "" {
			return line{}, false, nil
		}
		return line{kind: lineMessage, text: labelled("Assistant", clip(cb.Text, maxTextChars))}, true, nil
	case blockImage:
		if role != roleAssistant {
			break
		}
		// The durable block does not keep the image source.
		return line{kind: lineMessage, text: labelled("Assistant", supervisor.OmittedImagePlaceholder(""))}, true, nil
	case blockToolUse:
		if role != roleAssistant {
			break
		}
		if cb.ID == "" || cb.Name == "" {
			return line{}, false, conversionError(rec, "tool_use block %d lacks id or name", index)
		}
		input := bytes.TrimSpace(cb.Input)
		if len(input) == 0 || bytes.Equal(input, []byte("null")) {
			input = json.RawMessage(`{}`)
		}
		var compacted bytes.Buffer
		if err := json.Compact(&compacted, input); err != nil {
			return line{}, false, conversionError(rec, "tool_use block %d input: %v", index, err)
		}
		if _, seen := toolNames[cb.ID]; !seen {
			toolNames[cb.ID] = cb.Name
		}
		text := "called " + cb.Name + " with " + clipMiddle(compacted.String(), inputHeadChars, inputTailChars)
		return line{kind: lineCall, id: cb.ID, text: labelled("Assistant", text)}, true, nil
	case blockToolResult:
		if role != roleUser {
			break
		}
		if cb.ToolUseID == "" {
			return line{}, false, conversionError(rec, "tool_result block %d lacks tool_use_id", index)
		}
		output, err := toolResultOutput(cb.Content)
		if err != nil {
			return line{}, false, conversionError(rec, "tool_result block %d: %v", index, err)
		}
		label := "Tool"
		if name := toolNames[cb.ToolUseID]; name != "" {
			label = "Tool (" + name + ")"
		}
		prefix := "result: "
		if cb.IsError {
			prefix = "result (error): "
		}
		return line{kind: lineResult, id: cb.ToolUseID, text: labelled(label, prefix+clipMiddle(output, resultHeadChars, resultTailChars))}, true, nil
	}
	return line{}, false, conversionError(rec, "unsupported %q block %d in %s record", cb.Type, index, rec.Kind)
}

// labelled prefixes the role label and indents continuation lines so every
// message stays one entry.
func labelled(label, text string) string {
	text = strings.TrimRight(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	return label + ": " + strings.ReplaceAll(text, "\n", "\n  ")
}

// clip shortens text to at most limit characters plus a note naming how
// many were cut.
func clip(text string, limit int) string {
	n := utf8.RuneCountInString(text)
	if n <= limit {
		return text
	}
	runes := []rune(text)
	return string(runes[:limit]) + " [clipped " + strconv.Itoa(n-limit) + " characters]"
}

// clipMiddle keeps the head and tail of a tool input or result.
func clipMiddle(text string, head, tail int) string {
	return supervisor.ClipMiddle(text, head, tail, "omitted")
}

// toolResultOutput flattens tool_result content to text: string content as
// is, text blocks joined by newlines, and every image replaced by a
// placeholder since the seed is text.
func toolResultOutput(content json.RawMessage) (string, error) {
	content = bytes.TrimSpace(content)
	if len(content) == 0 || bytes.Equal(content, []byte("null")) {
		return "", nil
	}
	switch content[0] {
	case '"':
		var s string
		if err := json.Unmarshal(content, &s); err != nil {
			return "", fmt.Errorf("decode content: %w", err)
		}
		return s, nil
	case '[':
	default:
		return "", errors.New("content is neither a string nor a block array")
	}
	var items []json.RawMessage
	if err := json.Unmarshal(content, &items); err != nil {
		return "", fmt.Errorf("decode content: %w", err)
	}
	parts := make([]string, 0, len(items))
	for i, it := range items {
		var head struct {
			Type   string `json:"type"`
			Text   string `json:"text"`
			Source *struct {
				MediaType string `json:"media_type"`
			} `json:"source"`
		}
		if err := json.Unmarshal(it, &head); err != nil {
			return "", fmt.Errorf("decode content item %d: %w", i, err)
		}
		switch head.Type {
		case blockText:
			parts = append(parts, head.Text)
		case blockImage:
			mediaType := ""
			if head.Source != nil {
				mediaType = head.Source.MediaType
			}
			parts = append(parts, supervisor.OmittedImagePlaceholder(mediaType))
		default:
			parts = append(parts, supervisor.ToolResultItemPlaceholder(head.Type, it))
		}
	}
	return strings.Join(parts, "\n"), nil
}

func entryKey(e entry) string {
	var sb strings.Builder
	sb.WriteString(e.role)
	for _, l := range e.lines {
		sb.WriteByte(0)
		sb.WriteString(l.id)
		sb.WriteByte(0)
		sb.WriteString(l.text)
	}
	return sb.String()
}

// applyBudget keeps whole turns newest-first while their messages fit in
// budget characters and reports how many messages the dropped older turns
// held. A turn is the records sharing a turn id; a record without one is a
// turn of its own. Pairing is re-checked afterwards so a result whose call
// fell in a dropped turn is dropped (and counted) too.
func applyBudget(entries []entry, budget int, rules supervisor.SelectionRules[entry, line]) ([]entry, int) {
	type turn struct {
		cost     int
		messages int
	}
	keyOf := func(e entry) string {
		if e.rec.TurnID != "" {
			return "t:" + e.rec.TurnID
		}
		return "s:" + strconv.FormatInt(e.rec.Seq, 10)
	}
	turns := map[string]*turn{}
	var order []string
	for _, e := range entries {
		k := keyOf(e)
		t, ok := turns[k]
		if !ok {
			t = &turn{}
			turns[k] = t
			order = append(order, k)
		}
		for _, l := range e.lines {
			t.cost += utf8.RuneCountInString(l.text) + 1
			t.messages++
		}
	}
	keep := map[string]bool{}
	used := 0
	// A readable checkpoint is the only representation of the history it
	// covers. Reserve its cost before choosing recent turns.
	for _, k := range order {
		for _, e := range entries {
			if keyOf(e) == k && e.rec.Kind == supervisor.KindCheckpoint {
				keep[k] = true
				used += turns[k].cost
				break
			}
		}
	}
	for i := len(order) - 1; i >= 0; i-- {
		if keep[order[i]] {
			continue
		}
		t := turns[order[i]]
		if used+t.cost > budget {
			break
		}
		used += t.cost
		keep[order[i]] = true
	}
	total := 0
	var kept []entry
	for _, e := range entries {
		total += len(e.lines)
		if keep[keyOf(e)] {
			kept = append(kept, e)
		}
	}
	kept = supervisor.CollapseDuplicateEntries(supervisor.PairToolParts(kept, rules), rules)
	shown := 0
	for _, e := range kept {
		shown += len(e.lines)
	}
	return kept, total - shown
}
