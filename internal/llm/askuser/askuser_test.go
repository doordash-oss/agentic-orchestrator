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

package askuser

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func conf(v float64) *float64 { return &v }

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr bool
		want    Bundle
	}{
		{
			name: "claude cli envelope",
			raw:  `{"questions":[{"question":"Which branch?","header":"Branch","options":[{"label":"main","description":"default"},{"label":"dev","description":"work"}],"multiSelect":false}]}`,
			want: Bundle{Questions: []Question{{Question: "Which branch?", Header: "Branch", Options: []Option{{Label: "main", Description: "default"}, {Label: "dev", Description: "work"}}}}},
		},
		{
			name: "multi-select and confidence carried, absent confidence stays nil",
			raw:  `{"questions":[{"question":"Pick","header":"H","multiSelect":true,"options":[{"label":"a","description":"x","confidence":0.8},{"label":"b","description":"y"}]}]}`,
			want: Bundle{Questions: []Question{{Question: "Pick", Header: "H", MultiSelect: true, Options: []Option{{Label: "a", Description: "x", Confidence: conf(0.8)}, {Label: "b", Description: "y"}}}}},
		},
		{
			name: "codex adapter envelope",
			raw:  `{"questions":[{"header":"Scope","multiSelect":false,"options":[],"question":"Which scope?"}]}`,
			want: Bundle{Questions: []Question{{Question: "Which scope?", Header: "Scope", Options: []Option{}}}},
		},
		{
			name: "opencode adapter envelope",
			raw:  `{"questions":[{"header":"Agent Question","multiSelect":false,"options":[{"confidence":0.7,"description":"d","label":"yes"}],"question":"Proceed?"}]}`,
			want: Bundle{Questions: []Question{{Question: "Proceed?", Header: "Agent Question", Options: []Option{{Label: "yes", Description: "d", Confidence: conf(0.7)}}}}},
		},
		{
			name: "free-text question without options",
			raw:  `{"questions":[{"question":"Version?","header":"Version"}]}`,
			want: Bundle{Questions: []Question{{Question: "Version?", Header: "Version"}}},
		},
		{name: "bare array rejected", raw: `[{"question":"Which branch?","options":[{"label":"main"}]}]`, wantErr: true},
		{name: "malformed rejected", raw: `{"questions":[`, wantErr: true},
		{name: "empty input rejected", raw: ``, wantErr: true},
		{name: "null rejected", raw: `null`, wantErr: true},
		{name: "missing questions rejected", raw: `{}`, wantErr: true},
		{name: "empty questions rejected", raw: `{"questions":[]}`, wantErr: true},
		{name: "blank question text rejected", raw: `{"questions":[{"question":"  ","header":"H"}]}`, wantErr: true},
		{name: "blank option label rejected", raw: `{"questions":[{"question":"Q","options":[{"label":" ","description":"d"}]}]}`, wantErr: true},
		{name: "wrong field type rejected", raw: `{"questions":[{"question":1}]}`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse([]byte(tt.raw))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Parse(%s) = %+v, want error", tt.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%s): %v", tt.raw, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Parse(%s)\n got %#v\nwant %#v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestEncode(t *testing.T) {
	tests := []struct {
		name   string
		bundle Bundle
		want   string
	}{
		{
			// The bytes the Codex native user-input handler emitted as a map literal.
			name:   "codex native user input",
			bundle: Bundle{Questions: []Question{{Question: "Which scope?", Header: "Scope", Options: []Option{{Label: "Source", Description: "Committed files"}}}, {Question: "Which scope? (#2)", Header: "Scope", Options: []Option{}}}},
			want:   `{"questions":[{"header":"Scope","multiSelect":false,"options":[{"description":"Committed files","label":"Source"}],"question":"Which scope?"},{"header":"Scope","multiSelect":false,"options":[],"question":"Which scope? (#2)"}]}`,
		},
		{
			// The Codex structured ask_user tool, without the dropped recommended flag.
			name:   "codex structured tool",
			bundle: Bundle{Questions: []Question{{Question: "Which source?", Header: "Source", Options: []Option{{Label: "Source (Recommended)", Description: "Committed", Confidence: conf(0.9)}, {Label: "All", Description: "Everything", Confidence: conf(0.2)}}}}},
			want:   `{"questions":[{"header":"Source","multiSelect":false,"options":[{"confidence":0.9,"description":"Committed","label":"Source (Recommended)"},{"confidence":0.2,"description":"Everything","label":"All"}],"question":"Which source?"}]}`,
		},
		{
			// The OpenCode HTTP bridge, without the dropped custom flag.
			name:   "opencode bridge",
			bundle: Bundle{Questions: []Question{{Question: "Which branch?", Header: "Branch", MultiSelect: true, Options: []Option{{Label: "main", Description: "default"}, {Label: "dev", Description: "work"}}}}},
			want:   `{"questions":[{"header":"Branch","multiSelect":true,"options":[{"description":"default","label":"main"},{"description":"work","label":"dev"}],"question":"Which branch?"}]}`,
		},
		{
			// The OpenCode ACP question builder and synthesizer.
			name:   "opencode acp",
			bundle: Bundle{Questions: []Question{{Question: "Allow edit?", Header: "Agent Question", Options: []Option{{Label: "Allow once", Description: "allow_once", Confidence: conf(0.6)}, {Label: "Reject", Description: ""}}}}},
			want:   `{"questions":[{"header":"Agent Question","multiSelect":false,"options":[{"confidence":0.6,"description":"allow_once","label":"Allow once"},{"description":"","label":"Reject"}],"question":"Allow edit?"}]}`,
		},
		{
			name:   "absent options omitted",
			bundle: Bundle{Questions: []Question{{Question: "Version?", Header: "Version"}}},
			want:   `{"questions":[{"header":"Version","multiSelect":false,"question":"Version?"}]}`,
		},
		{
			name:   "empty bundle",
			bundle: Bundle{},
			want:   `{"questions":[]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(tt.bundle.Encode()); got != tt.want {
				t.Fatalf("Encode()\n got %s\nwant %s", got, tt.want)
			}
			if tt.bundle.Questions == nil {
				return
			}
			marshaled, err := json.Marshal(tt.bundle)
			if err != nil || string(marshaled) != tt.want {
				t.Fatalf("json.Marshal = %s, %v; want %s", marshaled, err, tt.want)
			}
		})
	}
}

func TestEncodeRoundTripIsCanonical(t *testing.T) {
	canonical := []string{
		`{"questions":[{"header":"Branch","multiSelect":false,"options":[{"description":"default","label":"main"},{"description":"work","label":"dev"}],"question":"Which branch?"}]}`,
		`{"questions":[{"header":"Version","multiSelect":false,"options":[],"question":"What version?"}]}`,
		`{"questions":[{"header":"Version","multiSelect":false,"question":"What version?"}]}`,
		`{"questions":[{"header":"A","multiSelect":true,"options":[{"confidence":0.86,"description":"x","label":"a"},{"description":"y","label":"b"}],"question":"Q1"},{"header":"","multiSelect":false,"options":[{"confidence":0,"description":"","label":"c"}],"question":"Q2"}]}`,
	}
	for _, raw := range canonical {
		bundle, err := Parse([]byte(raw))
		if err != nil {
			t.Fatalf("Parse(%s): %v", raw, err)
		}
		if got := string(bundle.Encode()); got != raw {
			t.Fatalf("round trip\n got %s\nwant %s", got, raw)
		}
	}

	// Any other field order or spacing normalizes to the canonical bytes, and
	// fields outside the type are dropped.
	claude := `{"questions":[{"question":"Which branch?","header":"Branch","options":[{"label":"main","description":"default","preview":"p"},{"label":"dev","description":"work"}],"multiSelect":false,"custom":true}]}`
	bundle, err := Parse([]byte(claude))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(bundle.Encode()); got != canonical[0] {
		t.Fatalf("normalized\n got %s\nwant %s", got, canonical[0])
	}
}

func signatureBase() Bundle {
	return Bundle{Questions: []Question{{
		Question:    "Which branch?",
		Header:      "Branch",
		MultiSelect: false,
		Options:     []Option{{Label: "main", Description: "default"}, {Label: "dev", Description: "work"}},
	}}}
}

func TestSignature(t *testing.T) {
	base := signatureBase()
	withConfidence := base.clone()
	withConfidence.Questions[0].Options[0].Confidence = conf(0.9)
	withConfidence.Questions[0].Options[1].Confidence = conf(0.1)
	if base.Signature() != withConfidence.Signature() {
		t.Fatalf("confidence changed the signature")
	}
	padded := base.clone()
	padded.Questions[0].Question = "  Which branch?\n"
	padded.Questions[0].Options[0].Label = " main "
	if base.Signature() != padded.Signature() {
		t.Fatalf("surrounding whitespace changed the signature")
	}

	mutations := map[string]func(*Bundle){
		"question":    func(b *Bundle) { b.Questions[0].Question = "Which tag?" },
		"header":      func(b *Bundle) { b.Questions[0].Header = "Tag" },
		"multiSelect": func(b *Bundle) { b.Questions[0].MultiSelect = true },
		"label":       func(b *Bundle) { b.Questions[0].Options[1].Label = "develop" },
		"description": func(b *Bundle) { b.Questions[0].Options[1].Description = "feature work" },
		"option count": func(b *Bundle) {
			b.Questions[0].Options = b.Questions[0].Options[:1]
		},
		"option order": func(b *Bundle) {
			o := b.Questions[0].Options
			o[0], o[1] = o[1], o[0]
		},
		"question count": func(b *Bundle) {
			b.Questions = append(b.Questions, Question{Question: "Another?"})
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := base.clone()
			mutate(&changed)
			if changed.Signature() == base.Signature() {
				t.Fatalf("changing %s kept the signature %s", name, base.Signature())
			}
		})
	}
}

func TestMergeConfidence(t *testing.T) {
	target := signatureBase()
	target.Questions[0].Options[1].Confidence = conf(0.3)
	source := signatureBase()
	source.Questions[0].Options[0].Confidence = conf(0.9)
	source.Questions[0].Options[1].Confidence = conf(0.1)

	merged, ok := target.MergeConfidence(source)
	if !ok {
		t.Fatal("MergeConfidence refused a same-signature source")
	}
	if got := merged.Questions[0].Options[0].Confidence; got == nil || *got != 0.9 {
		t.Fatalf("missing confidence not filled: %v", got)
	}
	if got := merged.Questions[0].Options[1].Confidence; got == nil || *got != 0.3 {
		t.Fatalf("present confidence overwritten: %v", got)
	}
	if target.Questions[0].Options[0].Confidence != nil {
		t.Fatal("MergeConfidence mutated the receiver")
	}
	source.Questions[0].Options[0].Confidence = conf(0.5)
	if got := merged.Questions[0].Options[0].Confidence; *got != 0.9 {
		t.Fatal("merged confidence aliases the source")
	}

	other := signatureBase()
	other.Questions[0].Header = "Different"
	other.Questions[0].Options[0].Confidence = conf(0.9)
	unchanged, ok := target.MergeConfidence(other)
	if ok {
		t.Fatal("MergeConfidence accepted a different signature")
	}
	if unchanged.Questions[0].Options[0].Confidence != nil {
		t.Fatal("refused merge still copied confidence")
	}
}

func TestDisplay(t *testing.T) {
	limits := Limits{Question: 5, Header: 3, Label: 4, Description: 6}
	source := Bundle{Questions: []Question{{
		Question:    "  Which branch?  ",
		Header:      "Br",
		MultiSelect: true,
		Options: []Option{
			{Label: " main ", Description: "default branch", Confidence: conf(0.7)},
			{Label: "develop", Description: "work"},
		},
	}}}
	sourceCopy := source.clone()

	got := source.Display(limits)
	want := Bundle{Questions: []Question{{
		Question:    "Which...",
		Header:      "Br",
		MultiSelect: true,
		Options: []Option{
			{Label: "main", Description: "defaul...", Confidence: conf(0.7)},
			{Label: "deve...", Description: "work"},
		},
	}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Display\n got %#v\nwant %#v", got, want)
	}
	if !reflect.DeepEqual(source, sourceCopy) {
		t.Fatal("Display mutated the source")
	}
	got.Questions[0].Options[0].Label = "changed"
	*got.Questions[0].Options[0].Confidence = 0.1
	if source.Questions[0].Options[0].Label != " main " || *source.Questions[0].Options[0].Confidence != 0.7 {
		t.Fatal("Display result aliases the source")
	}

	exact := Bundle{Questions: []Question{{Question: "12345", Header: "abc"}}}
	if got := exact.Display(limits); got.Questions[0].Question != "12345" || got.Questions[0].Header != "abc" {
		t.Fatalf("suffix appended at the limit: %#v", got)
	}
	long := strings.Repeat("x", 10)
	unbounded := Bundle{Questions: []Question{{Question: long}}}
	if got := unbounded.Display(Limits{}); got.Questions[0].Question != long {
		t.Fatalf("zero limit truncated: %q", got.Questions[0].Question)
	}
}
