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
	"strings"
	"time"
	"unicode"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// renderRootCard builds the Block Kit root card for a feature: a header
// with the feature name, a section with the server, pipeline, repositories,
// phase, and status (plus pull request links once any exist), and a context
// line with the last update rendered as a Slack date token.
func renderRootCard(serverName string, f *feature.Feature, now time.Time) ([]Block, string) {
	name := boundedCardText(safePlain(f.Name, headerTextLimit), headerTextLimit)
	fields := []textObject{
		labeledField("Server", serverName),
		labeledField("Pipeline", humanisePipeline(f.EffectivePipeline())),
		labeledField("Repositories", repoList(f)),
		labeledField("Phase", phaseWithRoadmap(f)),
		labeledField("Status", humaniseStatus(f.Status.String())),
	}
	if prs := prLinks(f); prs != "" {
		fields = append(fields, labeledFieldRaw("Pull requests", prs))
	}
	blocks := []Block{
		headerBlockFor(name),
		sectionBlockFor(fields),
		contextBlockFor([]textObject{{
			Type: textTypeMrkdwn,
			Text: slackDateToken(now.Unix(), now.UTC().Format("2006-01-02 15:04 MST")),
		}}),
	}
	fallback := boundDisplayText(strings.TrimSpace(
		fmt.Sprintf("%s — %s", name, humaniseStatus(f.Status.String())),
	), fallbackTextLimit)
	return blocks, safePlain(fallback, fallbackTextLimit)
}

func labeledField(label, value string) textObject {
	prefix := "*" + label + ":* "
	budget := fieldTextLimit - len(prefix)
	return textObject{
		Type: textTypeMrkdwn,
		Text: prefix + safeText(value, budget),
	}
}

// labeledFieldRaw renders a value that is already safe by construction
// (assembled link tokens) so markup survives; only bounding applies.
func labeledFieldRaw(label, value string) textObject {
	prefix := "*" + label + ":* "
	budget := fieldTextLimit - len(prefix)
	return textObject{
		Type: textTypeMrkdwn,
		Text: prefix + boundDisplayText(value, budget),
	}
}

func repoList(f *feature.Feature) string {
	names := make([]string, 0, len(f.Repos))
	for _, repo := range f.Repos {
		names = append(names, repo.Name)
	}
	list := strings.Join(names, ", ")
	return boundedCardText(boundDisplayText(list, repoFieldLimit), repoFieldLimit)
}

func boundedCardText(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	if limit <= 3 {
		return text[:limit]
	}
	return text[:limit-3] + "..."
}

func prLinks(f *feature.Feature) string {
	var links []string
	for _, repo := range f.Repos {
		state, ok := f.RepoStates[repo.Name]
		if !ok || state == nil || state.PRURL == "" {
			continue
		}
		url := boundDisplayText(state.PRURL, 200)
		if !strings.HasPrefix(url, "http") || strings.ContainsAny(url, "<>|") {
			links = append(links, safeText(repo.Name+" "+url, 200))
			continue
		}
		links = append(links, "<"+url+"|"+safeText(repo.Name, 150)+">")
	}
	return strings.Join(links, " ")
}

func phaseWithRoadmap(f *feature.Feature) string {
	phase := phaseTitle(f.CurrentPhase)
	if f.CurrentPhase == feature.PhasePlan && f.TotalRoadmapPhases > 0 && f.CurrentRoadmapPhase > 0 {
		phase += fmt.Sprintf(" (roadmap phase %d of %d)", f.CurrentRoadmapPhase, f.TotalRoadmapPhases)
	}
	return phase
}

// renderProgress builds the one-line Progress reply for a handled event.
// An empty line means the event posts no reply this phase. The line is the
// message text Slack renders as mrkdwn and shows in notifications.
func renderProgress(ev ports.Event, f *feature.Feature) string {
	var prefix, emoji, text string
	if f.IsChild() {
		prefix, emoji = childAffix(f.Parent.Kind)
	} else {
		emoji = topLevelEmoji(ev)
	}
	switch ev.Type {
	case ports.FeatureStarted:
		if !f.IsChild() {
			return ""
		}
		text = "pass started"
	case ports.PhaseStarted:
		text = phaseTitle(ev.Phase) + " started"
		if ev.Phase == feature.PhasePlan && f.TotalRoadmapPhases > 0 && f.CurrentRoadmapPhase > 0 {
			text += fmt.Sprintf(" (roadmap phase %d of %d)", f.CurrentRoadmapPhase, f.TotalRoadmapPhases)
		}
	case ports.PhaseCompleted:
		if ev.Error != nil || ev.CanonicalError != nil {
			return ""
		}
		if ev.Phase == feature.PhaseImplement && f.TotalRoadmapPhases > 0 && f.CurrentRoadmapPhase > 0 {
			key := roadmapTimingKey(f)
			text = fmt.Sprintf(
				"Roadmap phase %d of %d complete in %s (cost %s)",
				f.CurrentRoadmapPhase, f.TotalRoadmapPhases,
				formatDuration(f.PhaseRuntime(key.plan)+f.PhaseRuntime(key.implement)),
				formatCost(f.PhaseCost(key.plan)+f.PhaseCost(key.implement)),
			)
		} else {
			key := timingKeyFor(f, ev.Phase)
			text = fmt.Sprintf(
				"%s completed in %s (cost %s)",
				phaseTitle(ev.Phase),
				formatDuration(f.PhaseRuntime(key)),
				formatCost(f.PhaseCost(key)),
			)
		}
	case ports.PublishStarted:
		text = "Publishing started"
	case ports.PublishCompleted:
		if ev.Error != nil || ev.CanonicalError != nil {
			return ""
		}
		if links := prLinks(f); links != "" {
			text = "Published — " + links
		} else {
			text = "Publishing completed"
		}
	case ports.FeatureCompleted:
		if f.IsChild() {
			text = fmt.Sprintf(
				"pass completed in %s (cost %s)",
				formatDuration(f.TotalRuntime()),
				formatCost(f.TotalCost()),
			)
		} else {
			text = fmt.Sprintf(
				"Feature completed in %s (cost %s)",
				formatDuration(f.TotalRuntime()),
				formatCost(f.TotalCost()),
			)
		}
	default:
		return ""
	}
	if prefix != "" {
		text = prefix + ": " + text
	}
	// Link tokens (pull request links) are safe by construction: URLs are
	// validated and link labels were escaped at assembly, so the line only
	// needs bounding here.
	return emoji + " " + boundDisplayText(text, 2000)
}

type roadmapKeys struct {
	plan      string
	implement string
}

func roadmapTimingKey(f *feature.Feature) roadmapKeys {
	return roadmapKeys{
		plan:      fmt.Sprintf("phase-%d-plan", f.CurrentRoadmapPhase),
		implement: fmt.Sprintf("phase-%d-impl", f.CurrentRoadmapPhase),
	}
}

// timingKeyFor resolves the feature's own timing-key scheme for a phase:
// the record's active key when it already belongs to that phase, else the
// derived default (roadmap loop phases use their per-phase keys).
func timingKeyFor(f *feature.Feature, phase feature.Phase) string {
	active := f.ActiveTimingKey
	if keyBelongsToPhase(active, phase) {
		return active
	}
	switch phase {
	case feature.PhaseResearch:
		return "research"
	case feature.PhaseDesign:
		return "design"
	case feature.PhaseInquire:
		return "inquire"
	case feature.PhasePlan:
		if f.TotalRoadmapPhases > 0 && f.CurrentRoadmapPhase > 0 {
			return fmt.Sprintf("phase-%d-plan", f.CurrentRoadmapPhase)
		}
		return "plan"
	case feature.PhaseImplement:
		if f.TotalRoadmapPhases > 0 && f.CurrentRoadmapPhase > 0 {
			return fmt.Sprintf("phase-%d-impl", f.CurrentRoadmapPhase)
		}
		return "implement"
	case feature.PhaseKnowledgeBase:
		return "knowledgebase"
	default:
		return phase.DirName()
	}
}

func keyBelongsToPhase(key string, phase feature.Phase) bool {
	if key == "" {
		return false
	}
	switch phase {
	case feature.PhaseResearch:
		return key == "research"
	case feature.PhaseDesign:
		return key == "design"
	case feature.PhaseInquire:
		return key == "inquire"
	case feature.PhasePlan:
		return key == "plan" || strings.HasSuffix(key, "-plan")
	case feature.PhaseImplement:
		return isImplementFamilyKey(key)
	case feature.PhaseKnowledgeBase:
		return key == "knowledgebase"
	default:
		return key == phase.DirName()
	}
}

func isImplementFamilyKey(key string) bool {
	return key == "implement" ||
		strings.HasPrefix(key, "rebase-") ||
		strings.HasSuffix(key, "-impl")
}

func phaseTitle(phase feature.Phase) string {
	if phase == feature.PhaseImplement {
		return "Implementation"
	}
	return phase.String()
}

// topLevelEmoji is the leading emoji for a top-level feature's line.
func topLevelEmoji(ev ports.Event) string {
	switch ev.Type {
	case ports.PublishStarted, ports.PublishCompleted:
		return "🚢"
	case ports.FeatureCompleted:
		return "🎉"
	case ports.PhaseStarted, ports.PhaseCompleted:
		switch ev.Phase {
		case feature.PhaseResearch:
			return "🔍"
		case feature.PhaseDesign:
			return "🎨"
		case feature.PhaseInquire:
			return "❓"
		case feature.PhasePlan:
			return "🗺️"
		case feature.PhaseImplement:
			return "🔧"
		case feature.PhaseKnowledgeBase:
			return "📚"
		case feature.PhaseFinalReview:
			return "🔎"
		default:
			return "⚙️"
		}
	default:
		return "🧩"
	}
}

func childAffix(kind string) (prefix, emoji string) {
	switch kind {
	case feature.ChildKindRefactor:
		return "Refactor", "♻️"
	case feature.ChildKindReviewFeedback:
		return "Review feedback", "🔁"
	case feature.ChildKindRebase:
		return "Rebase", "🔀"
	default:
		return "", "🧩"
	}
}

func childLabel(kind string) string {
	prefix, _ := childAffix(kind)
	if prefix == "" {
		return "Child"
	}
	return prefix
}

func humanisePipeline(profile feature.PipelineProfile) string {
	value := strings.TrimSpace(profile.String())
	if value == "" {
		return string(feature.PipelineMoonshot)
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

// humaniseStatus splits a CamelCase status into readable words.
func humaniseStatus(status string) string {
	var words []string
	start := 0
	for i, r := range status {
		if i > 0 && unicode.IsUpper(r) {
			words = append(words, strings.ToLower(status[start:i]))
			start = i
		}
	}
	if start < len(status) {
		words = append(words, strings.ToLower(status[start:]))
	}
	if len(words) == 0 {
		return status
	}
	first := words[0]
	if len(first) > 0 {
		first = strings.ToUpper(first[:1]) + first[1:]
	}
	return first + " " + strings.Join(words[1:], " ")
}

// formatDuration renders a duration the way a human reads it.
func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d >= time.Hour {
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
	if d >= time.Minute {
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

// formatCost renders a USD cost at cent precision.
func formatCost(cost float64) string {
	if cost > 0 && cost < 0.005 {
		return "under $0.01"
	}
	return fmt.Sprintf("$%.2f", cost)
}
