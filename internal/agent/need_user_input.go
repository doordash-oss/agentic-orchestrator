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

package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// NeedUserInputRecord is the persisted gate artifact written under an
// implement iteration directory when an iteration emits
// `## Iteration State: NEED_USER_INPUT`. Gate scope lives in feature/cycle
// state; this artifact only stores the user-facing questionnaire.
type NeedUserInputRecord struct {
	Summary   string                  `yaml:"summary"`
	Questions []NeedUserInputQuestion `yaml:"questions"`
	Iteration int                     `yaml:"iteration"`
}

// NeedUserInputQuestion is one prompt-and-answer pair the user fills in
// before resuming.
type NeedUserInputQuestion struct {
	Index  int    `yaml:"index"`
	Prompt string `yaml:"prompt"`
	Answer string `yaml:"answer"`
}

// NeedUserInputArtifactName is the canonical filename for the gate artifact
// inside an iteration directory.
const NeedUserInputArtifactName = "need-user-input.yaml"

// reconcileNeedUserInputGate returns the gate questionnaire to persist for a
// paused NEED_USER_INPUT iteration. The implementer-authored agentRec is
// authoritative, but each empty field falls back to the validated progress.md
// handoff (state note for the summary, numbered questions for the prompts) so a
// blank stub never surfaces as an empty gate. Surviving questions are
// re-indexed 1-based.
func reconcileNeedUserInputGate(agentRec *NeedUserInputRecord, progress *ParsedProgress, iteration int) NeedUserInputRecord {
	var rec NeedUserInputRecord
	if agentRec != nil {
		rec = *agentRec
	}
	rec.Iteration = iteration
	rec.Summary = strings.TrimSpace(rec.Summary)

	questions := make([]NeedUserInputQuestion, 0, len(rec.Questions))
	for _, q := range rec.Questions {
		prompt := strings.TrimSpace(q.Prompt)
		if prompt == "" {
			continue
		}
		questions = append(questions, NeedUserInputQuestion{
			Index:  len(questions) + 1,
			Prompt: prompt,
			Answer: q.Answer,
		})
	}

	if progress != nil {
		if rec.Summary == "" {
			rec.Summary = strings.TrimSpace(progress.StateNote)
		}
		if len(questions) == 0 {
			for _, p := range progress.Questions {
				prompt := strings.TrimSpace(p)
				if prompt == "" {
					continue
				}
				questions = append(questions, NeedUserInputQuestion{
					Index:  len(questions) + 1,
					Prompt: prompt,
				})
			}
		}
	}

	rec.Questions = questions
	return rec
}

// NeedUserInputPath returns the absolute path of the gate artifact for the
// supplied iteration directory.
func NeedUserInputPath(iterDir string) string {
	return filepath.Join(iterDir, NeedUserInputArtifactName)
}

// WriteNeedUserInputRecord serialises rec as YAML and writes it to path.
func WriteNeedUserInputRecord(path string, rec NeedUserInputRecord) error {
	data, err := yaml.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal need-user-input: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir for need-user-input: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}

// ReadNeedUserInputRecord loads the gate artifact at path.
func ReadNeedUserInputRecord(path string) (NeedUserInputRecord, error) {
	var rec NeedUserInputRecord
	data, err := os.ReadFile(path)
	if err != nil {
		return rec, err
	}
	if err := yaml.Unmarshal(data, &rec); err != nil {
		return rec, fmt.Errorf("parse need-user-input: %w", err)
	}
	return rec, nil
}

// AllAnswered reports whether every question has a non-empty answer.
// Returns false on a record with no questions to keep resume safe — the
// gate must collect at least one structured answer before resume.
func (r NeedUserInputRecord) AllAnswered() bool {
	if len(r.Questions) == 0 {
		return false
	}
	for _, q := range r.Questions {
		if q.Answer == "" {
			return false
		}
	}
	return true
}

// buildPriorUserInputAnswers walks artifactDir for `iteration-*` directories
// in deterministic ascending order, loads each iteration's
// `need-user-input.yaml` (when present), and renders the answered gates as
// a single prompt-ready section. Unanswered or unreadable artifacts are
// silently skipped — only fully resolved gates are surfaced to the next
// iteration so the resumed agent sees what the user committed to, not what
// is still outstanding. Returns "" when no resolved gates exist.
func buildPriorUserInputAnswers(artifactDir string) string {
	entries, err := os.ReadDir(artifactDir)
	if err != nil {
		return ""
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	var sections []string
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "iteration-") {
			continue
		}
		rec, err := ReadNeedUserInputRecord(filepath.Join(artifactDir, entry.Name(), NeedUserInputArtifactName))
		if err != nil || !rec.AllAnswered() {
			continue
		}

		var b strings.Builder
		fmt.Fprintf(&b, "### Iteration %d\nSummary: %s\n", rec.Iteration, strings.TrimSpace(rec.Summary))
		for _, q := range rec.Questions {
			fmt.Fprintf(&b, "Q%d: %s\nA%d: %s\n", q.Index, strings.TrimSpace(q.Prompt), q.Index, strings.TrimSpace(q.Answer))
		}
		sections = append(sections, strings.TrimRight(b.String(), "\n"))
	}
	if len(sections) == 0 {
		return ""
	}
	return strings.Join(sections, "\n\n")
}
