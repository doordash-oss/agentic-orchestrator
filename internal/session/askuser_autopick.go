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

package session

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

const (
	askUserAutoPickThresholdNone   = 0.5
	askUserAutoPickThresholdMedium = 0.7
)

var (
	autoPickNumberedOptionRe      = regexp.MustCompile(`^\d+\.\s+(.+)$`)
	autoPickConfidenceSuffixRe    = regexp.MustCompile(`(?i)\s+\[confidence:\s*(0(?:\.\d+)?|1(?:\.0+)?)\]\s*$`)
	autoPickTrailingRecommendedRe = regexp.MustCompile(`(?i)\s+\(recommended\)\s*$`)
)

type askUserAutoPickDecisionContext struct {
	Purpose     ports.AskUserAutoPickPurpose
	Inquireness feature.Inquireness
}

type askUserAutoPickDecision struct {
	Pickable   bool
	Answers    map[string]string
	Selections []askUserAutoPickSelection
	Reason     string
}

type askUserAutoPickSelection struct {
	Question   string
	Answer     string
	Confidence float64
}

type autoPickQuestion struct {
	Question    string
	MultiSelect bool
	Options     []autoPickOption
}

type autoPickOption struct {
	Label      string
	Confidence *float64
}

type autoPickQuestionSignature struct {
	Questions []autoPickQuestionSignatureQuestion `json:"questions"`
}

type autoPickQuestionSignatureQuestion struct {
	Question    string                            `json:"question"`
	MultiSelect bool                              `json:"multiSelect"`
	Options     []autoPickQuestionSignatureOption `json:"options,omitempty"`
}

type autoPickQuestionSignatureOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

func askUserAutoPickPurposeCanPick(purpose ports.AskUserAutoPickPurpose) bool {
	switch purpose {
	case ports.AskUserAutoPickPurposeInquire,
		ports.AskUserAutoPickPurposeDesign,
		ports.AskUserAutoPickPurposeRoadmapCreator,
		ports.AskUserAutoPickPurposePhasePlanCreator:
		return true
	default:
		return false
	}
}

func decideAskUserAutoPick(input json.RawMessage, ctx askUserAutoPickDecisionContext) askUserAutoPickDecision {
	if !askUserAutoPickPurposeCanPick(ctx.Purpose) {
		return askUserAutoPickDecision{Reason: "purpose not allowlisted"}
	}
	threshold, ok := askUserAutoPickThreshold(ctx.Purpose, ctx.Inquireness)
	if !ok {
		return askUserAutoPickDecision{Reason: "inquireness disabled or invalid"}
	}

	questions, ok := parseAutoPickQuestions(input)
	if !ok || len(questions) == 0 {
		return askUserAutoPickDecision{Reason: "invalid question bundle"}
	}

	answers := make(map[string]string, len(questions))
	selections := make([]askUserAutoPickSelection, 0, len(questions))
	for _, q := range questions {
		selection, ok := selectAutoPickAnswer(q, threshold)
		if !ok {
			return askUserAutoPickDecision{Reason: "question is not pickable"}
		}
		answers[selection.Question] = selection.Answer
		selections = append(selections, selection)
	}

	return askUserAutoPickDecision{
		Pickable:   true,
		Answers:    answers,
		Selections: selections,
	}
}

func askUserAutoPickThreshold(purpose ports.AskUserAutoPickPurpose, inquireness feature.Inquireness) (float64, bool) {
	if purpose == ports.AskUserAutoPickPurposePhasePlanCreator {
		inquireness = feature.InquirenessNone
	}
	switch inquireness {
	case feature.InquirenessNone:
		return askUserAutoPickThresholdNone, true
	case feature.InquirenessMedium:
		return askUserAutoPickThresholdMedium, true
	case feature.InquirenessHigh:
		return 0, false
	default:
		return 0, false
	}
}

func parseAutoPickQuestions(input json.RawMessage) ([]autoPickQuestion, bool) {
	if len(input) == 0 {
		return nil, false
	}
	var parsed struct {
		Questions []struct {
			Question    string `json:"question"`
			MultiSelect bool   `json:"multiSelect"`
			Options     []struct {
				Label      string   `json:"label"`
				Confidence *float64 `json:"confidence"`
			} `json:"options"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(input, &parsed); err != nil || len(parsed.Questions) == 0 {
		return nil, false
	}

	questions := make([]autoPickQuestion, 0, len(parsed.Questions))
	for _, q := range parsed.Questions {
		question := autoPickQuestion{
			Question:    q.Question,
			MultiSelect: q.MultiSelect,
		}
		for _, opt := range q.Options {
			question.Options = append(question.Options, autoPickOption{
				Label:      opt.Label,
				Confidence: opt.Confidence,
			})
		}
		if len(question.Options) == 0 {
			cleaned, inferred, ok := inferAutoPickOptionsFromQuestionText(q.Question)
			if ok {
				question.Question = cleaned
				question.Options = inferred
			}
		}
		questions = append(questions, question)
	}
	return questions, true
}

func askUserAutoPickSignature(input json.RawMessage) (string, bool) {
	if len(input) == 0 {
		return "", false
	}
	var parsed struct {
		Questions []struct {
			Question    string `json:"question"`
			MultiSelect bool   `json:"multiSelect"`
			Options     []struct {
				Label       string `json:"label"`
				Description string `json:"description"`
			} `json:"options"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(input, &parsed); err != nil || len(parsed.Questions) == 0 {
		return "", false
	}

	signature := autoPickQuestionSignature{
		Questions: make([]autoPickQuestionSignatureQuestion, 0, len(parsed.Questions)),
	}
	for _, q := range parsed.Questions {
		sq := autoPickQuestionSignatureQuestion{
			Question:    q.Question,
			MultiSelect: q.MultiSelect,
			Options:     make([]autoPickQuestionSignatureOption, 0, len(q.Options)),
		}
		for _, opt := range q.Options {
			sq.Options = append(sq.Options, autoPickQuestionSignatureOption{
				Label:       opt.Label,
				Description: opt.Description,
			})
		}
		signature.Questions = append(signature.Questions, sq)
	}
	data, err := json.Marshal(signature)
	if err != nil {
		return "", false
	}
	return string(data), true
}

func selectAutoPickAnswer(q autoPickQuestion, threshold float64) (askUserAutoPickSelection, bool) {
	if strings.TrimSpace(q.Question) == "" || len(q.Options) == 0 {
		return askUserAutoPickSelection{}, false
	}
	if q.MultiSelect {
		return selectAutoPickMultiAnswer(q, threshold)
	}

	selectedIndex := -1
	selectedConfidence := -1.0
	for i, opt := range q.Options {
		if strings.TrimSpace(opt.Label) == "" || opt.Confidence == nil || *opt.Confidence < 0 || *opt.Confidence > 1 {
			return askUserAutoPickSelection{}, false
		}
		if *opt.Confidence >= threshold && *opt.Confidence > selectedConfidence {
			selectedIndex = i
			selectedConfidence = *opt.Confidence
		}
	}
	if selectedIndex < 0 {
		return askUserAutoPickSelection{}, false
	}
	selected := q.Options[selectedIndex]
	return askUserAutoPickSelection{
		Question:   q.Question,
		Answer:     selected.Label,
		Confidence: *selected.Confidence,
	}, true
}

func selectAutoPickMultiAnswer(q autoPickQuestion, threshold float64) (askUserAutoPickSelection, bool) {
	selectedLabels := make([]string, 0, len(q.Options))
	selectedConfidence := 1.0
	for _, opt := range q.Options {
		if strings.TrimSpace(opt.Label) == "" || opt.Confidence == nil || *opt.Confidence < 0 || *opt.Confidence > 1 {
			return askUserAutoPickSelection{}, false
		}
		if *opt.Confidence >= threshold {
			selectedLabels = append(selectedLabels, opt.Label)
			if *opt.Confidence < selectedConfidence {
				selectedConfidence = *opt.Confidence
			}
		}
	}
	if len(selectedLabels) == 0 {
		return askUserAutoPickSelection{}, false
	}
	return askUserAutoPickSelection{
		Question:   q.Question,
		Answer:     strings.Join(selectedLabels, ", "),
		Confidence: selectedConfidence,
	}, true
}

func inferAutoPickOptionsFromQuestionText(question string) (string, []autoPickOption, bool) {
	lines := strings.Split(strings.ReplaceAll(question, "\r\n", "\n"), "\n")
	stem := make([]string, 0, len(lines))
	rawOptions := make([]string, 0, 4)
	trailingStem := make([]string, 0, 2)
	inOptions := false
	inTrailingStem := false
	sawBlankAfterOptions := false

	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			if inTrailingStem {
				if len(trailingStem) > 0 && trailingStem[len(trailingStem)-1] != "" {
					trailingStem = append(trailingStem, "")
				}
				continue
			}
			if inOptions {
				if len(rawOptions) > 0 {
					sawBlankAfterOptions = true
				}
				continue
			}
			if len(stem) > 0 && stem[len(stem)-1] != "" {
				stem = append(stem, "")
			}
			continue
		}

		if !inTrailingStem {
			if matches := autoPickNumberedOptionRe.FindStringSubmatch(line); matches != nil {
				inOptions = true
				sawBlankAfterOptions = false
				rawOptions = append(rawOptions, strings.TrimSpace(matches[1]))
				continue
			}
		}

		if inTrailingStem {
			trailingStem = append(trailingStem, line)
			continue
		}

		if inOptions {
			if isAutoPickReplyInstruction(line) {
				continue
			}
			if sawBlankAfterOptions && isAutoPickTrailingQuestion(line) {
				inTrailingStem = true
				trailingStem = append(trailingStem, line)
				continue
			}
			if len(rawOptions) > 0 {
				rawOptions[len(rawOptions)-1] += " " + line
			}
			sawBlankAfterOptions = false
			continue
		}

		stem = append(stem, line)
	}

	if len(rawOptions) < 2 || looksLikeAutoPickQuestionBundle(rawOptions) {
		return "", nil, false
	}

	options := make([]autoPickOption, 0, len(rawOptions))
	for _, raw := range rawOptions {
		label, confidence := splitAutoPickOption(raw)
		if label == "" {
			return "", nil, false
		}
		options = append(options, autoPickOption{Label: label, Confidence: confidence})
	}

	cleaned := strings.TrimSpace(strings.Join(stem, "\n"))
	if len(trailingStem) > 0 {
		cleaned = strings.TrimSpace(strings.Join(trailingStem, "\n"))
	}
	if cleaned == "" {
		cleaned = question
	}
	return cleaned, options, true
}

func isAutoPickTrailingQuestion(line string) bool {
	return strings.HasSuffix(strings.TrimSpace(line), "?")
}

func looksLikeAutoPickQuestionBundle(rawOptions []string) bool {
	questionCount := 0
	for _, raw := range rawOptions {
		if strings.Contains(strings.TrimSpace(raw), "?") {
			questionCount++
		}
	}
	return questionCount == len(rawOptions)
}

func splitAutoPickOption(raw string) (string, *float64) {
	raw, confidence, trailingRecommended := splitAutoPickOptionConfidence(raw)
	if raw == "" {
		return "", confidence
	}
	label := raw
	if idx := strings.Index(raw, ":"); idx >= 0 {
		label = strings.TrimSpace(raw[:idx])
	}
	label = strings.Trim(label, "`")
	label = strings.TrimSpace(label)
	if trailingRecommended && !strings.Contains(strings.ToLower(label), "(recommended)") {
		label += " (Recommended)"
	}
	return label, confidence
}

func splitAutoPickOptionConfidence(raw string) (string, *float64, bool) {
	raw = strings.TrimSpace(raw)
	raw, trailingRecommended := trimAutoPickTrailingRecommended(raw)
	matches := autoPickConfidenceSuffixRe.FindStringSubmatch(raw)
	if matches == nil {
		return raw, nil, trailingRecommended
	}
	confidence, err := strconv.ParseFloat(matches[1], 64)
	if err != nil {
		return raw, nil, trailingRecommended
	}
	trimmed := strings.TrimSpace(raw[:len(raw)-len(matches[0])])
	return trimmed, &confidence, trailingRecommended
}

func trimAutoPickTrailingRecommended(raw string) (string, bool) {
	matches := autoPickTrailingRecommendedRe.FindStringSubmatch(raw)
	if matches == nil {
		return raw, false
	}
	return strings.TrimSpace(raw[:len(raw)-len(matches[0])]), true
}

func isAutoPickReplyInstruction(line string) bool {
	lower := strings.ToLower(strings.TrimSpace(line))
	return strings.HasPrefix(lower, "reply with ") ||
		strings.HasPrefix(lower, "respond with ") ||
		strings.HasPrefix(lower, "answer with ")
}
