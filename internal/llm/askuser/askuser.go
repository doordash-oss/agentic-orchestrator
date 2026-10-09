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

// Package askuser owns the ask-user envelope: the AskUserQuestion tool input
// {"questions":[...]} that providers send when their agent asks the operator a
// structured question. It parses raw input into a typed Bundle, encodes a
// Bundle back to the wire envelope, signs a Bundle independently of option
// confidence, and produces byte-bounded display copies.
//
// The package is a leaf: it imports only the standard library so every
// provider adapter, the session and the server can depend on it.
package askuser

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Bundle is one ask-user turn: the questions of a single envelope.
type Bundle struct {
	Questions []Question `json:"questions"`
}

// Question is one question of an ask-user turn.
type Question struct {
	Question    string
	Header      string
	MultiSelect bool
	// Options is nil when the envelope carried no options key and empty when
	// it carried an explicit empty list; encoding preserves the distinction.
	Options []Option
}

// Option is one selectable answer of a question.
//
// Fields are declared in sorted key order, as for wireQuestion.
type Option struct {
	Confidence  *float64 `json:"confidence,omitempty"`
	Description string   `json:"description"`
	Label       string   `json:"label"`
}

// Limits are the byte limits Display applies to each field. A limit of zero
// or less leaves that field unbounded.
type Limits struct {
	Question    int
	Header      int
	Label       int
	Description int
}

// wireQuestion declares its fields in sorted key order so the canonical
// encoding is byte-identical to the map-literal envelopes the adapters built
// before this package existed.
type wireQuestion struct {
	Header      string    `json:"header"`
	MultiSelect bool      `json:"multiSelect"`
	Options     *[]Option `json:"options,omitempty"`
	Question    string    `json:"question"`
}

// MarshalJSON encodes the question in the canonical envelope field order.
func (q Question) MarshalJSON() ([]byte, error) {
	w := wireQuestion{Question: q.Question, Header: q.Header, MultiSelect: q.MultiSelect}
	if q.Options != nil {
		w.Options = &q.Options
	}
	return json.Marshal(w)
}

// UnmarshalJSON decodes one envelope question.
func (q *Question) UnmarshalJSON(data []byte) error {
	var w wireQuestion
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	*q = Question{Question: w.Question, Header: w.Header, MultiSelect: w.MultiSelect}
	if w.Options != nil {
		q.Options = *w.Options
		if q.Options == nil {
			q.Options = []Option{}
		}
	}
	return nil
}

// Parse decodes raw AskUserQuestion tool input. It accepts only the
// {"questions":[...]} envelope and rejects malformed JSON, a bare array, an
// absent or empty questions list, a question with blank text and an option
// with a blank label.
func Parse(raw []byte) (Bundle, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return Bundle{}, errors.New("askuser: empty input")
	}
	if trimmed[0] != '{' {
		return Bundle{}, errors.New("askuser: input is not a questions envelope")
	}
	var bundle Bundle
	if err := json.Unmarshal(trimmed, &bundle); err != nil {
		return Bundle{}, fmt.Errorf("askuser: decode envelope: %w", err)
	}
	if len(bundle.Questions) == 0 {
		return Bundle{}, errors.New("askuser: envelope has no questions")
	}
	for i, q := range bundle.Questions {
		if strings.TrimSpace(q.Question) == "" {
			return Bundle{}, fmt.Errorf("askuser: question %d has blank text", i)
		}
		for j, opt := range q.Options {
			if strings.TrimSpace(opt.Label) == "" {
				return Bundle{}, fmt.Errorf("askuser: question %d option %d has blank label", i, j)
			}
		}
	}
	return bundle, nil
}

// Encode returns the bundle's wire envelope.
func (b Bundle) Encode() json.RawMessage {
	if b.Questions == nil {
		b.Questions = []Question{}
	}
	// Marshal cannot fail: every field is a string, bool, float pointer or a
	// slice of them, and confidence values come from JSON or callers' floats.
	data, _ := json.Marshal(b)
	return data
}

type signatureQuestion struct {
	Question    string            `json:"q"`
	Header      string            `json:"h"`
	MultiSelect bool              `json:"m"`
	Options     []signatureOption `json:"o"`
}

type signatureOption struct {
	Label       string `json:"l"`
	Description string `json:"d"`
}

// Signature identifies the bundle by its trimmed question text, header,
// multi-select flag, option labels and option descriptions, never by option
// confidence. It is the auto-pick signature: a control request and the
// assistant tool-use block that carried confidence share it.
func (b Bundle) Signature() string {
	sig := make([]signatureQuestion, 0, len(b.Questions))
	for _, q := range b.Questions {
		sq := signatureQuestion{
			Question:    strings.TrimSpace(q.Question),
			Header:      strings.TrimSpace(q.Header),
			MultiSelect: q.MultiSelect,
			Options:     make([]signatureOption, 0, len(q.Options)),
		}
		for _, opt := range q.Options {
			sq.Options = append(sq.Options, signatureOption{
				Label:       strings.TrimSpace(opt.Label),
				Description: strings.TrimSpace(opt.Description),
			})
		}
		sig = append(sig, sq)
	}
	data, _ := json.Marshal(sig)
	return string(data)
}

// MergeConfidence returns a copy of b whose options without confidence take
// the confidence of the same option in source. It returns false, and b
// unchanged, when the two bundles have different signatures.
func (b Bundle) MergeConfidence(source Bundle) (Bundle, bool) {
	if b.Signature() != source.Signature() {
		return b, false
	}
	out := b.clone()
	for i := range out.Questions {
		for j := range out.Questions[i].Options {
			opt := &out.Questions[i].Options[j]
			if opt.Confidence != nil {
				continue
			}
			if c := source.Questions[i].Options[j].Confidence; c != nil {
				v := *c
				opt.Confidence = &v
			}
		}
	}
	return out, true
}

// Display returns a copy of b with every field trimmed and, when longer than
// its limit, byte-truncated with a "..." suffix.
func (b Bundle) Display(limits Limits) Bundle {
	out := b.clone()
	for i := range out.Questions {
		q := &out.Questions[i]
		q.Question = bound(q.Question, limits.Question)
		q.Header = bound(q.Header, limits.Header)
		for j := range q.Options {
			opt := &q.Options[j]
			opt.Label = bound(opt.Label, limits.Label)
			opt.Description = bound(opt.Description, limits.Description)
		}
	}
	return out
}

func (b Bundle) clone() Bundle {
	if b.Questions == nil {
		return Bundle{}
	}
	out := Bundle{Questions: make([]Question, len(b.Questions))}
	for i, q := range b.Questions {
		out.Questions[i] = q
		if q.Options == nil {
			continue
		}
		out.Questions[i].Options = make([]Option, len(q.Options))
		for j, opt := range q.Options {
			if opt.Confidence != nil {
				v := *opt.Confidence
				opt.Confidence = &v
			}
			out.Questions[i].Options[j] = opt
		}
	}
	return out
}

func bound(s string, limit int) string {
	s = strings.TrimSpace(s)
	if limit > 0 && len(s) > limit {
		return s[:limit] + "..."
	}
	return s
}
