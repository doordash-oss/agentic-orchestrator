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
	"strconv"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// WriteQAFile writes accumulated Q&A pairs to a markdown file in dir.
// Returns the file path, or "" if there are no Q&A pairs.
func WriteQAFile(qaLog []ports.QAPair, dir string) (string, error) {
	if len(qaLog) == 0 {
		return "", nil
	}

	var b strings.Builder
	b.WriteString("# User Q&A — Phase Clarifications\n\n")

	for _, pair := range qaLog {
		fmt.Fprintf(&b, "## Q: %s\n\n", pair.Question)
		fmt.Fprintf(&b, "**A:** %s\n\n", pair.Answer)
		if pair.Notes != "" {
			fmt.Fprintf(&b, "**Notes:** %s\n\n", pair.Notes)
		}
		if pair.AutoPicked {
			fmt.Fprintf(&b, "_(auto-picked, confidence: %.2f)_\n\n", pair.Confidence)
		}
	}

	path := filepath.Join(dir, "qa-answers.md")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return "", fmt.Errorf("writing qa-answers.md: %w", err)
	}
	return path, nil
}

// ReadQAFile parses the harness-owned qa-answers.md format written by
// WriteQAFile. It is intentionally scoped to that deterministic format.
func ReadQAFile(path string) ([]ports.QAPair, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading qa-answers.md: %w", err)
	}
	body := strings.ReplaceAll(string(data), "\r\n", "\n")
	idx := strings.Index(body, "## Q: ")
	if idx < 0 {
		return nil, nil
	}
	blocks := strings.Split(body[idx:], "\n## Q: ")
	pairs := make([]ports.QAPair, 0, len(blocks))
	for _, block := range blocks {
		block = strings.TrimPrefix(block, "## Q: ")
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		question, rest, found := strings.Cut(block, "\n")
		if !found {
			continue
		}
		answer, ok := qaMarkdownField(rest, "**A:** ")
		if !ok {
			continue
		}
		pair := ports.QAPair{
			Question: strings.TrimSpace(question),
			Answer:   answer,
		}
		if notes, ok := qaMarkdownField(rest, "**Notes:** "); ok {
			pair.Notes = notes
		}
		if confidence, ok := qaMarkdownConfidence(rest); ok {
			pair.AutoPicked = true
			pair.Confidence = confidence
		}
		pairs = append(pairs, pair)
	}
	return pairs, nil
}

func qaMarkdownField(body, prefix string) (string, bool) {
	idx := strings.Index(body, prefix)
	if idx < 0 {
		return "", false
	}
	start := idx + len(prefix)
	value := body[start:]
	if end := qaMarkdownFieldEnd(value); end >= 0 {
		value = value[:end]
	}
	return strings.TrimSpace(value), true
}

func qaMarkdownFieldEnd(value string) int {
	markers := []string{
		"\n\n**A:** ",
		"\n\n**Notes:** ",
		"\n\n_(auto-picked, confidence: ",
	}
	end := -1
	for _, marker := range markers {
		if idx := strings.Index(value, marker); idx >= 0 && (end < 0 || idx < end) {
			end = idx
		}
	}
	return end
}

func qaMarkdownConfidence(body string) (float64, bool) {
	const prefix = "_(auto-picked, confidence: "
	idx := strings.Index(body, prefix)
	if idx < 0 {
		return 0, false
	}
	start := idx + len(prefix)
	rest := body[start:]
	end := strings.Index(rest, ")_")
	if end < 0 {
		return 0, false
	}
	confidence, err := strconv.ParseFloat(strings.TrimSpace(rest[:end]), 64)
	if err != nil {
		return 0, false
	}
	return confidence, true
}
