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
	"encoding/json"
	"path/filepath"
	"slices"
)

// creationDisplayIndex recognizes the legacy observer/native pair without
// altering durable tool history. It is rebuilt during the existing startup
// scan and extended on append, so display pages need no preceding-page reads.
// Evidence is deliberately local: an exact pending Write result immediately
// follows an observed creation in the same turn and generation.
type creationDisplayIndex struct {
	generation int64
	turn       string
	pending    map[string][]string // tool-use ID -> exact absolute targets
	observed   map[string]bool     // creations in the immediately preceding record
	hidden     map[int64][]int     // native record seq -> duplicate FileChanges indices
}

func (d *creationDisplayIndex) observe(rec Record) {
	if rec.Generation != d.generation || rec.TurnID != d.turn {
		d.generation, d.turn = rec.Generation, rec.TurnID
		d.pending, d.observed = nil, nil
	}
	previous := d.observed
	d.observed = nil
	if rec.TurnID == "" || (rec.Kind != KindToolUse && rec.Kind != KindToolResult) {
		return
	}
	var data ContentData
	if json.Unmarshal(rec.Data, &data) != nil {
		return
	}
	if rec.Kind == KindToolUse {
		for _, block := range data.Content {
			if !block.IsToolUse() || block.ID == "" {
				continue
			}
			delete(d.pending, block.ID)
			var input struct {
				FilePath string   `json:"file_path"`
				Paths    []string `json:"paths"`
			}
			if block.Name != "Write" || json.Unmarshal(block.Input, &input) != nil {
				continue
			}
			for _, target := range append(input.Paths, input.FilePath) {
				if !filepath.IsAbs(target) {
					continue
				}
				if d.pending == nil {
					d.pending = make(map[string][]string)
				}
				d.pending[block.ID] = append(d.pending[block.ID], target)
			}
		}
		return
	}
	if data.ObservedFiles {
		for _, change := range data.FileChanges {
			if (change.Operation == "add" || change.Operation == "create") && change.OldPath == "" && filepath.IsAbs(change.Path) {
				if d.observed == nil {
					d.observed = make(map[string]bool)
				}
				d.observed[change.Path] = true
			}
		}
		return
	}
	completed := make(map[string]bool)
	for _, block := range data.Content {
		if !block.IsToolResult() {
			continue
		}
		for _, target := range d.pending[block.ToolUseID] {
			if !block.IsError && previous[target] {
				completed[target] = true
			}
		}
		delete(d.pending, block.ToolUseID)
	}
	for index, change := range data.FileChanges {
		if completed[change.Path] && change.OldPath == "" && (change.Operation == "write" || change.Operation == "add" || change.Operation == "create") {
			if d.hidden == nil {
				d.hidden = make(map[int64][]int)
			}
			d.hidden[rec.Seq] = append(d.hidden[rec.Seq], index)
		}
	}
}

// project changes only a display copy's metadata. Content blocks, identities,
// visibility and sequence positions remain intact; raw reads stay unmodified.
func (d *creationDisplayIndex) project(rec Record) Record {
	hidden := d.hidden[rec.Seq]
	if len(hidden) == 0 {
		return rec
	}
	var data ContentData
	if json.Unmarshal(rec.Data, &data) != nil {
		return rec
	}
	changes := data.FileChanges[:0]
	for index, change := range data.FileChanges {
		if !slices.Contains(hidden, index) {
			changes = append(changes, change)
		}
	}
	data.FileChanges = changes
	if encoded, err := json.Marshal(data); err == nil {
		rec.Data = encoded
	}
	return rec
}

func (s *transcriptStore) displayRecord(rec Record) Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.creationDisplay.project(rec)
}

func (s *transcriptStore) displayPage(q PageQuery) (Page, error) {
	page, err := s.rangedPage(q)
	if err != nil {
		return Page{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range page.Items {
		page.Items[i] = s.creationDisplay.project(page.Items[i])
	}
	return page, nil
}
