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

package slack

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

type questionAnswer struct {
	answer   string
	decision string
	hint     string
}

func parseQuestionAnswer(input ports.SlackPendingInput, text string) questionAnswer {
	if len(text) > sectionTextLimit {
		return questionAnswer{hint: "This reply is too long for Slack. Answer in Agentico."}
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return questionAnswer{hint: "Reply with an answer, or answer in Agentico."}
	}
	if len(input.Options) == 0 {
		return questionAnswer{answer: text, decision: "free_text"}
	}
	if !input.MultiSelect {
		if index, ok := matchOption(input.Options, text); ok {
			label := input.Options[index].Label
			return questionAnswer{answer: label, decision: "option_selected"}
		}
		if _, err := strconv.Atoi(normalizeResponderReply(text)); err == nil {
			return questionAnswer{hint: fmt.Sprintf("Choose an option number from 1 to %d, its label, or reply with other text.", len(input.Options))}
		}
		return questionAnswer{answer: text, decision: "free_text"}
	}
	indices, valid := parseOptionList(input.Options, text)
	if !valid {
		return questionAnswer{hint: "Reply with a comma- or space-separated list of option numbers or exact labels."}
	}
	labels := make([]string, 0, len(indices))
	for _, index := range indices {
		labels = append(labels, input.Options[index].Label)
	}
	joined := strings.Join(labels, ", ")
	return questionAnswer{answer: joined, decision: "options_selected"}
}

func matchOption(options []ports.SlackPendingInputOption, value string) (int, bool) {
	normalized := normalizeResponderReply(value)
	if number, err := strconv.Atoi(normalized); err == nil {
		return number - 1, number >= 1 && number <= len(options)
	}
	for i, option := range options {
		if normalizeResponderReply(option.Label) == normalized {
			return i, true
		}
	}
	return 0, false
}

func parseOptionList(options []ports.SlackPendingInputOption, text string) ([]int, bool) {
	selected := make(map[int]bool)
	for _, part := range strings.Split(text, ",") {
		words := strings.Fields(part)
		if len(words) == 0 {
			return nil, false
		}
		for len(words) > 0 {
			matched := false
			for length := len(words); length > 0; length-- {
				if index, ok := matchOption(options, strings.Join(words[:length], " ")); ok {
					selected[index] = true
					words = words[length:]
					matched = true
					break
				}
			}
			if !matched {
				return nil, false
			}
		}
	}
	indices := make([]int, 0, len(selected))
	for index := range selected {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	return indices, len(indices) > 0
}

func keycapOption(name string) int {
	switch name {
	case "one":
		return 1
	case "two":
		return 2
	case "three":
		return 3
	case "four":
		return 4
	case "five":
		return 5
	case "six":
		return 6
	case "seven":
		return 7
	case "eight":
		return 8
	case "nine":
		return 9
	default:
		return 0
	}
}

func splitResponderTag(text string) (tag, answer string, tagged bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "#") {
		return "", text, false
	}
	end := 1
	for end < len(text) && text[end] >= '0' && text[end] <= '9' {
		end++
	}
	if end == 1 || (end < len(text) && !unicode.IsSpace(rune(text[end])) && !strings.ContainsRune(":.-", rune(text[end]))) {
		return "", text, false
	}
	tag = text[:end]
	answer = strings.TrimSpace(text[end:])
	answer = strings.TrimSpace(strings.TrimLeft(answer, ":.-"))
	return tag, answer, true
}

func taggedPostingBefore(postings []postingIndexEntry, tag, ts string) (postingIndexEntry, bool) {
	for _, posting := range postings {
		if posting.Tag == tag && compareSlackTimestamps(posting.MessageTS, ts) < 0 {
			return posting, true
		}
	}
	return postingIndexEntry{}, false
}

func pendingResponderTags(items []pendingInputRecord) []string {
	var tags []string
	for _, item := range items {
		if item.Resolution == nil && item.HeldAnswer == nil && item.Tag != "" {
			tags = append(tags, item.Tag)
		}
	}
	sort.Slice(tags, func(i, j int) bool { return tagNumber(tags[i]) < tagNumber(tags[j]) })
	return tags
}

func compactOptionLabel(label string) string {
	label = safeText(label, 180)
	if utf8.RuneCountInString(label) > 160 {
		label = string([]rune(label)[:160]) + "…"
	}
	return label
}
