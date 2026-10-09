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
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/askuser"
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
	// autoPickProseConfidenceRe matches a confidence the agent wrote as prose
	// inside an option ("Confidence 0.70.", "confidence: 0.7") instead of the
	// structured field.
	autoPickProseConfidenceRe = regexp.MustCompile(`(?i)\bconfidence\s*[:=]?\s*(0(?:\.\d+)?|1(?:\.0+)?)\b`)
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
	// Parse returns an empty bundle for a rejected input, which declines.
	bundle, _ := askuser.Parse(input)
	return decideAskUserAutoPickBundle(bundle, ctx)
}

// decideAskUserAutoPickBundle runs the auto-pick heuristics over a parsed
// ask-user turn. An empty bundle, as for a rejected input, is not pickable.
func decideAskUserAutoPickBundle(bundle askuser.Bundle, ctx askUserAutoPickDecisionContext) askUserAutoPickDecision {
	if !askUserAutoPickPurposeCanPick(ctx.Purpose) {
		return askUserAutoPickDecision{Reason: "purpose not allowlisted"}
	}
	threshold, ok := askUserAutoPickThreshold(ctx.Purpose, ctx.Inquireness)
	if !ok {
		return askUserAutoPickDecision{Reason: "inquireness disabled or invalid"}
	}
	if len(bundle.Questions) == 0 {
		return askUserAutoPickDecision{Reason: "invalid question bundle"}
	}

	answers := make(map[string]string, len(bundle.Questions))
	selections := make([]askUserAutoPickSelection, 0, len(bundle.Questions))
	for _, q := range bundle.Questions {
		selection, ok := selectAutoPickAnswer(normalizeAutoPickQuestion(q), threshold)
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

// normalizeAutoPickQuestion returns a copy of q with confidence recovered
// from option prose where the structured field is absent and, when q carries
// no options, with options inferred from numbered lines in its text.
func normalizeAutoPickQuestion(q askuser.Question) askuser.Question {
	out := askuser.Question{Question: q.Question, MultiSelect: q.MultiSelect}
	for _, opt := range q.Options {
		label, confidence := opt.Label, opt.Confidence
		if confidence == nil {
			label, confidence = inferAutoPickOptionConfidence(label, opt.Description)
		}
		out.Options = append(out.Options, askuser.Option{Label: label, Confidence: confidence})
	}
	if len(out.Options) == 0 {
		if cleaned, inferred, ok := inferAutoPickOptionsFromQuestionText(q.Question); ok {
			out.Question = cleaned
			out.Options = inferred
		}
	}
	return out
}

func selectAutoPickAnswer(q askuser.Question, threshold float64) (askUserAutoPickSelection, bool) {
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

func selectAutoPickMultiAnswer(q askuser.Question, threshold float64) (askUserAutoPickSelection, bool) {
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

func inferAutoPickOptionsFromQuestionText(question string) (string, []askuser.Option, bool) {
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

	options := make([]askuser.Option, 0, len(rawOptions))
	for _, raw := range rawOptions {
		label, confidence := splitAutoPickOption(raw)
		if label == "" {
			return "", nil, false
		}
		options = append(options, askuser.Option{Label: label, Confidence: confidence})
	}

	cleaned := strings.TrimSpace(strings.Join(stem, "\n"))
	if len(trailingStem) > 0 {
		cleaned = strings.TrimSpace(strings.Join(trailingStem, "\n"))
	}
	return cleaned, options, true
}

// inferAutoPickOptionConfidence recovers a confidence the agent expressed in
// prose when the structured field is absent. A label suffix is stripped so
// the recorded answer stays clean; a description match leaves the label
// untouched.
func inferAutoPickOptionConfidence(label, description string) (string, *float64) {
	if trimmed, confidence, trailingRecommended := splitAutoPickOptionConfidence(label); confidence != nil {
		if trailingRecommended && !strings.Contains(strings.ToLower(trimmed), "(recommended)") {
			trimmed += " (Recommended)"
		}
		return trimmed, confidence
	}
	if matches := autoPickProseConfidenceRe.FindStringSubmatch(description); matches != nil {
		if confidence, err := strconv.ParseFloat(matches[1], 64); err == nil {
			return label, &confidence
		}
	}
	return label, nil
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
