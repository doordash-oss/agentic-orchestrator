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
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

const (
	inputFallbackTextLimit = 1200

	permissionResponseInstructions   = "React ✅ to allow once or ❌ to deny, or reply allow, approve, yes, deny, or no. Remember rules are created only in Agentico. Anyone who can see this can respond"
	singleSelectResponseInstructions = "Reply with an option number, its label, or react with a keycap number (1️⃣–9️⃣). Any other text is treated as a free-text answer. Anyone who can see this can respond"
	multiSelectResponseInstructions  = "Reply with a comma- or space-separated list of option numbers or labels. Anyone who can see this can respond"
	freeTextResponseInstructions     = "Reply with any text. Anyone who can see this can respond"
	gateResponseInstructions         = "Resolution: In Agentico, waive the blocked checks or retry after signing in. Replies are not read here."
	reviewResponseInstructions       = "React ✅ or reply approve to approve. Requesting changes is done in Agentico. Anyone who can see this can respond"
)

func pendingInputIdentity(item ports.SlackPendingInput) string {
	switch item.Kind {
	case ports.SlackPendingQuestion:
		return fmt.Sprintf("question:%s:%d", item.RequestID, item.QuestionIndex)
	case ports.SlackPendingPermission:
		return "permission:" + item.RequestID
	case ports.SlackPendingGate:
		return fmt.Sprintf(
			"gate:%s:%s:%d:%s",
			item.FeatureID, item.GatePath, item.Iteration, item.WaitingSince.UTC().Format(timeLayout),
		)
	case ports.SlackPendingReview:
		return "review:" + item.ReviewID + ":" + item.SourceRevision
	case ports.SlackPendingHelp:
		return ports.SlackHelpEntryIdentity(item.FeatureID, item.WaitingSince, item.HelpQuestion)
	default:
		return ""
	}
}

const timeLayout = "2006-01-02T15:04:05.999999999Z07:00"

func renderPendingInput(
	token, tag string,
	item ports.SlackPendingInput,
	source *feature.Feature,
) ([]Block, string) {
	prefix := ""
	if source != nil && source.IsChild() {
		if value, _ := childAffix(source.Parent.Kind); value != "" {
			prefix = scrub(token, value) + ": "
		}
	}
	switch item.Kind {
	case ports.SlackPendingQuestion:
		return renderQuestionInput(token, tag, prefix, item)
	case ports.SlackPendingPermission:
		return renderPermissionInput(token, tag, prefix, item)
	case ports.SlackPendingHelp:
		return renderHelpInput(token, tag, prefix, item)
	case ports.SlackPendingGate:
		return renderGateInput(token, tag, prefix, item)
	case ports.SlackPendingReview:
		return renderReviewInput(token, tag, prefix, item, "")
	default:
		return nil, ""
	}
}

func renderReviewInput(
	token, tag, prefix string,
	item ports.SlackPendingInput,
	attachmentNote string,
) ([]Block, string) {
	label := reviewArtifactLabel(token, item)
	filename := filepath.Base(item.ArtifactPath)
	if filename == "." || filename == string(filepath.Separator) || filename == "" {
		filename = item.ArtifactID
	}
	label = scrub(token, label)
	filename = scrub(token, filename)
	size := formatArtifactSize(item.ArtifactSize)
	header := safePlain(tag+" · "+prefix+"Review: "+label, headerTextLimit)
	detail := fmt.Sprintf(
		"*Artifact:* %s (`%s`, %s)\n*On approval:* %s",
		safeText(label, 500),
		safeText(filename, 500),
		size,
		safeText(reviewApprovalAction(item), 1000),
	)
	if attachmentNote == "" {
		attachmentNote = "The artifact is attached above."
	}
	attachmentNote = scrub(token, attachmentNote)
	agenticoNote := "Edit the artifact or request changes in Agentico."
	blocks := []Block{
		headerBlockFor(header),
		sectionTextBlockFor(detail),
		sectionTextBlockFor(safeText(attachmentNote, sectionTextLimit)),
		sectionTextBlockFor(agenticoNote),
		contextBlockFor([]textObject{{
			Type: textTypeMrkdwn,
			Text: safeText(reviewResponseInstructions, contextTextLimit),
		}}),
	}
	return blocks, pendingInputFallback(
		tag+" "+prefix+"Review: "+label,
		[]string{
			fmt.Sprintf("Artifact: %s (%s, %s)", label, filename, size),
			"On approval: " + reviewApprovalAction(item),
			attachmentNote,
			agenticoNote,
		},
		reviewResponseInstructions,
	)
}

func reviewArtifactLabel(token string, item ports.SlackPendingInput) string {
	switch {
	case item.ReviewMode == "rewind":
		return fmt.Sprintf("Run %d rewind", item.RunNumber)
	case item.Roadmap:
		return "Roadmap"
	case item.PhasePlan:
		if item.RoadmapPhase > 0 {
			return fmt.Sprintf("Phase %d plan", item.RoadmapPhase)
		}
		return "Phase plan"
	}
	artifactID := scrub(token, item.ArtifactID)
	switch artifactID {
	case "prompt":
		return "Prompt"
	case "inquire":
		return "Inquiry"
	case "research":
		return "Research"
	case "design":
		return "Design"
	case "plan":
		return "Plan"
	case "description-review":
		return "Description"
	default:
		if artifactID != "" {
			return strings.ReplaceAll(strings.Title(strings.ReplaceAll(artifactID, "-", " ")), " Md", "")
		}
		return "Artifact"
	}
}

func reviewApprovalAction(item ports.SlackPendingInput) string {
	if item.ReviewMode == "rewind" {
		return fmt.Sprintf("restart the rewind from the attached artifact for run %d", item.RunNumber)
	}
	if item.Roadmap {
		if item.TotalRoadmapPhases > 0 {
			return fmt.Sprintf("start planning roadmap phase 1 of %d", item.TotalRoadmapPhases)
		}
		return "start planning roadmap phase 1"
	}
	if item.PhasePlan {
		if item.RoadmapPhase > 0 && item.TotalRoadmapPhases > 0 {
			return fmt.Sprintf(
				"start implementation of roadmap phase %d of %d",
				item.RoadmapPhase,
				item.TotalRoadmapPhases,
			)
		}
		return "start implementation"
	}
	if strings.EqualFold(item.TargetPhase, feature.PhasePlan.DirName()) {
		return "start the Plan phase"
	}
	if item.TargetPhase != "" {
		return "start the " + strings.Title(item.TargetPhase) + " phase"
	}
	return "continue the feature"
}

func formatArtifactSize(size int64) string {
	if size < 1024 {
		return fmt.Sprintf("%d bytes", size)
	}
	if size < 1024*1024 {
		return fmt.Sprintf("%.1f KiB", float64(size)/1024)
	}
	return fmt.Sprintf("%.1f MiB", float64(size)/(1024*1024))
}

func renderPermissionInput(token, tag, prefix string, item ports.SlackPendingInput) ([]Block, string) {
	tool := scrub(token, firstNonempty(item.ToolName, "Tool"))
	header := safePlain(tag+" · "+prefix+"Permission: "+tool, headerTextLimit)
	repo := scrub(token, firstNonempty(item.RepoName, "feature"))
	phase := scrub(token, firstNonempty(item.Phase, "unknown"))
	detail := fmt.Sprintf("*Repository:* %s\n*Phase:* %s", safeText(repo, 500), safeText(phase, 200))
	input := permissionInputText(item)
	input = scrub(token, input)
	const note = "\n_Input was shortened. Open Agentico for the full input._"
	bodyBudget := sectionTextLimit - len("```\n\n```") - len(note)
	escaped := strings.ReplaceAll(input, "```", "'''")
	rendered := safeText(escaped, 0)
	shortened := len("```\n"+rendered+"\n```") > sectionTextLimit
	if shortened {
		rendered = truncateEscapedText(escaped, bodyBudget)
	}
	code := "```\n" + rendered + "\n```"
	if shortened {
		code += note
	}
	blocks := []Block{
		headerBlockFor(header),
		sectionTextBlockFor(detail),
		sectionTextBlockFor(code),
		contextBlockFor([]textObject{{
			Type: textTypeMrkdwn,
			Text: safeText(permissionResponseInstructions, contextTextLimit),
		}}),
	}
	fallback := pendingInputFallback(
		tag+" "+prefix+"Permission: "+tool,
		[]string{
			"Repository: " + repo,
			"Phase: " + phase,
			"Input: " + input,
		},
		permissionResponseInstructions,
	)
	return blocks, fallback
}

func permissionInputText(item ports.SlackPendingInput) string {
	if strings.EqualFold(item.ToolName, "Bash") {
		if command, ok := item.Input["command"].(string); ok {
			return command
		}
	}
	data, err := json.Marshal(item.Input)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func renderQuestionInput(token, tag, prefix string, item ports.SlackPendingInput) ([]Block, string) {
	headerText := firstNonempty(item.Header, "Question")
	header := safePlain(tag+" · "+prefix+scrub(token, headerText), headerTextLimit)
	var sections []Block
	sections = append(sections, headerBlockFor(header))
	question := scrub(token, item.Question)
	if item.QuestionCount > 1 {
		question = fmt.Sprintf("Question %d of %d\n%s", item.QuestionIndex+1, item.QuestionCount, question)
	}
	sections = append(sections, sectionTextBlockFor(safeText(question, sectionTextLimit)))
	if len(item.Options) > 0 {
		recommended := recommendedOption(item)
		var lines []string
		for i, option := range item.Options {
			label := scrub(token, option.Label)
			if i == recommended && !strings.Contains(strings.ToLower(label), "recommended") {
				label += " (recommended)"
			}
			line := fmt.Sprintf("*%d. %s*", i+1, safeText(label, 500))
			if option.Description != "" {
				line += " — " + safeText(scrub(token, option.Description), 1000)
			}
			if option.HasConfidence {
				line += fmt.Sprintf(" · %d%% confidence", int(math.Round(option.Confidence*100)))
			}
			lines = append(lines, line)
		}
		sections = append(sections, sectionTextBlockFor(
			truncateMrkdwnTokens(strings.Join(lines, "\n"), sectionTextLimit),
		))
	}
	context := freeTextResponseInstructions
	if len(item.Options) > 0 && item.MultiSelect {
		context = multiSelectResponseInstructions
	} else if len(item.Options) > 0 {
		context = singleSelectResponseInstructions
	}
	sections = append(sections, contextBlockFor([]textObject{{
		Type: textTypeMrkdwn, Text: safeText(context, contextTextLimit),
	}}))

	fallbackDetails := make([]string, 0, len(item.Options)+1)
	if item.QuestionCount > 1 {
		fallbackDetails = append(
			fallbackDetails,
			fmt.Sprintf("Question %d of %d: %s", item.QuestionIndex+1, item.QuestionCount, scrub(token, item.Question)),
		)
	} else {
		fallbackDetails = append(fallbackDetails, "Question: "+scrub(token, item.Question))
	}
	recommended := recommendedOption(item)
	for i, option := range item.Options {
		label := scrub(token, option.Label)
		if i == recommended && !strings.Contains(strings.ToLower(label), "recommended") {
			label += " (recommended)"
		}
		optionParts := []string{fmt.Sprintf("%d. %s", i+1, label)}
		if option.Description != "" {
			optionParts = append(optionParts, scrub(token, option.Description))
		}
		if option.HasConfidence {
			optionParts = append(
				optionParts,
				fmt.Sprintf("%d%% confidence", int(math.Round(option.Confidence*100))),
			)
		}
		fallbackDetails = append(fallbackDetails, strings.Join(optionParts, " — "))
	}
	return sections, pendingInputFallback(
		tag+" "+prefix+scrub(token, headerText),
		fallbackDetails,
		context,
	)
}

func recommendedOption(item ports.SlackPendingInput) int {
	if item.MultiSelect || len(item.Options) == 0 {
		return -1
	}
	best := -1
	var confidence float64
	for i, option := range item.Options {
		if option.HasConfidence && (best < 0 || option.Confidence > confidence) {
			best = i
			confidence = option.Confidence
		}
	}
	return best
}

func renderHelpInput(token, tag, prefix string, item ports.SlackPendingInput) ([]Block, string) {
	question := scrub(token, item.HelpQuestion)
	blocks := []Block{
		headerBlockFor(safePlain(tag+" · "+prefix+"Help request", headerTextLimit)),
		sectionTextBlockFor(safeText(question, sectionTextLimit)),
		contextBlockFor([]textObject{{
			Type: textTypeMrkdwn,
			Text: safeText(freeTextResponseInstructions, contextTextLimit),
		}}),
	}
	return blocks, pendingInputFallback(
		tag+" "+prefix+"Help request",
		[]string{"Question: " + question},
		freeTextResponseInstructions,
	)
}

func renderGateInput(token, tag, prefix string, item ports.SlackPendingInput) ([]Block, string) {
	blocks := []Block{
		headerBlockFor(safePlain(tag+" · "+prefix+"Verification needs your input", headerTextLimit)),
	}
	summary := scrub(token, item.GateSummary)
	if summary != "" {
		blocks = append(blocks, sectionTextBlockFor("*Summary:*\n"+safeText(summary, sectionTextLimit-12)))
	}
	trailingBlockCount := 2
	if len(item.GateQuestions) > 0 {
		trailingBlockCount++
	}
	blockerLimit := max(0, messageBlockLimit-len(blocks)-trailingBlockCount)
	displayedBlockers := min(len(item.GateBlockers), blockerLimit)
	overflowCount := len(item.GateBlockers) - displayedBlockers
	for i, blocker := range item.GateBlockers[:displayedBlockers] {
		blockOverflow := 0
		if i == displayedBlockers-1 {
			blockOverflow = overflowCount
		}
		blocks = append(blocks, renderGateBlocker(token, blocker, blockOverflow))
	}
	blocks = append(blocks, renderGateQuestions(token, item.GateQuestions)...)
	note := "Resolve this gate in Agentico by waiving the blocked checks or retrying after signing in."
	blocks = append(blocks,
		sectionTextBlockFor(safeText(note, sectionTextLimit)),
		contextBlockFor([]textObject{{
			Type: textTypeMrkdwn,
			Text: safeText("Replies are not read here. Resolve this verification gate in Agentico.", contextTextLimit),
		}}),
	)

	fallbackDetails := make([]string, 0, len(item.GateBlockers)+len(item.GateQuestions)+1)
	if summary != "" {
		fallbackDetails = append(fallbackDetails, "Summary: "+summary)
	}
	for i, blocker := range item.GateBlockers {
		blockerParts := []string{
			fmt.Sprintf(
				"Blocker %d: %s",
				i+1,
				scrub(token, firstNonempty(blocker.Name, "Blocked check")),
			),
		}
		if blocker.RepoName != "" {
			blockerParts = append(blockerParts, "Repository: "+scrub(token, blocker.RepoName))
		}
		if blocker.Command != "" {
			blockerParts = append(blockerParts, "Command: "+scrub(token, blocker.Command))
		}
		if blocker.Reason != "" {
			blockerParts = append(blockerParts, "Reason: "+scrub(token, blocker.Reason))
		}
		if blocker.Remediation != "" {
			blockerParts = append(blockerParts, "Remediation: "+scrub(token, blocker.Remediation))
		}
		fallbackDetails = append(fallbackDetails, strings.Join(blockerParts, "; "))
	}
	for i, question := range item.GateQuestions {
		fallbackDetails = append(
			fallbackDetails,
			fmt.Sprintf("Question %d: %s", i+1, scrub(token, question)),
		)
	}
	return blocks, pendingInputFallback(
		tag+" "+prefix+"Verification needs your input",
		fallbackDetails,
		gateResponseInstructions,
	)
}

func renderGateBlocker(
	token string,
	blocker ports.SlackPendingInputBlocker,
	overflowCount int,
) Block {
	parts := []string{"*" + safeText(scrub(token, firstNonempty(blocker.Name, "Blocked check")), 500) + "*"}
	if blocker.RepoName != "" {
		parts = append(parts, "*Repository:* "+safeText(scrub(token, blocker.RepoName), 500))
	}
	if blocker.Command != "" {
		parts = append(parts, "*Command:* `"+safeText(scrub(token, blocker.Command), 1200)+"`")
	}
	if blocker.Reason != "" {
		parts = append(parts, "*Reason:* "+safeText(scrub(token, blocker.Reason), 800))
	}
	if blocker.Remediation != "" {
		parts = append(parts, "*Remediation:* "+safeText(scrub(token, blocker.Remediation), 800))
	}

	text := strings.Join(parts, "\n")
	if overflowCount == 0 {
		return sectionTextBlockFor(truncateMrkdwnTokens(text, sectionTextLimit))
	}
	blockedCheckLabel := "checks"
	if overflowCount == 1 {
		blockedCheckLabel = "check"
	}
	overflowNote := fmt.Sprintf(
		"%d additional blocked %s not shown. Open Agentico for full details.",
		overflowCount,
		blockedCheckLabel,
	)
	const separator = "\n\n"
	text = truncateMrkdwnTokens(text, sectionTextLimit-len(separator)-len(overflowNote))
	return sectionTextBlockFor(text + separator + overflowNote)
}

func renderGateQuestions(token string, questions []string) []Block {
	if len(questions) == 0 {
		return nil
	}
	rendered := make([]string, 0, len(questions))
	for i, question := range questions {
		rendered = append(rendered, fmt.Sprintf("%d. %s", i+1, safeText(scrub(token, question), 1200)))
	}
	return []Block{sectionTextBlockFor(
		"*Questions:*\n" + truncateMrkdwnTokens(strings.Join(rendered, "\n"), sectionTextLimit-13),
	)}
}

func pendingInputFallback(title string, details []string, instructions string) string {
	const (
		partSeparator     = " | "
		fullDetailPointer = "Open Agentico for full details."
		preferredTitleMax = 240
	)

	title = strings.Join(strings.Fields(title), " ")
	instructions = strings.Join(strings.Fields(instructions), " ")
	normalizedDetails := make([]string, 0, len(details))
	for _, detail := range details {
		if detail = strings.Join(strings.Fields(detail), " "); detail != "" {
			normalizedDetails = append(normalizedDetails, detail)
		}
	}

	parts := make([]string, 0, len(normalizedDetails)+2)
	parts = append(parts, title)
	parts = append(parts, normalizedDetails...)
	parts = append(parts, instructions)
	if fallback := strings.Join(compactStrings(parts...), partSeparator); len(fallback) <= inputFallbackTextLimit {
		return fallback
	}

	title = abbreviateFallbackText(title, preferredTitleMax)
	required := compactStrings(title, instructions, fullDetailPointer)
	detailBudget := inputFallbackTextLimit - len(strings.Join(required, partSeparator))
	if len(normalizedDetails) > 0 {
		detailBudget -= len(partSeparator)
	}
	detail := abbreviateFallbackText(strings.Join(normalizedDetails, partSeparator), max(0, detailBudget))
	return safePlain(
		strings.Join(compactStrings(title, detail, instructions, fullDetailPointer), partSeparator),
		inputFallbackTextLimit,
	)
}

func waitingSummary(pending []pendingInputRecord, repliesEnabled bool) string {
	if len(pending) == 0 {
		return ""
	}
	if !repliesEnabled {
		counts := map[string]int{}
		for _, item := range pending {
			counts[item.Kind]++
		}
		var parts []string
		for _, kind := range []string{
			string(ports.SlackPendingPermission),
			string(ports.SlackPendingQuestion),
			string(ports.SlackPendingGate),
			string(ports.SlackPendingHelp),
			string(ports.SlackPendingReview),
		} {
			if count := counts[kind]; count > 0 {
				label := pendingKindLabel(kind)
				if count != 1 {
					label += "s"
				}
				parts = append(parts, fmt.Sprintf("%d %s", count, label))
			}
		}
		return strings.Join(parts, ", ") + " · Needs input replies are off"
	}
	tagged := append([]pendingInputRecord(nil), pending...)
	sort.SliceStable(tagged, func(i, j int) bool {
		return tagNumber(tagged[i].Tag) < tagNumber(tagged[j].Tag)
	})
	var parts []string
	for _, item := range tagged {
		if item.Tag != "" {
			parts = append(parts, item.Tag+" "+pendingKindLabel(item.Kind))
		}
	}
	return strings.Join(parts, " · ")
}

func pendingKindLabel(kind string) string {
	if kind == string(ports.SlackPendingGate) {
		return "verification gate"
	}
	return kind
}

func tagNumber(tag string) int {
	var value int
	_, _ = fmt.Sscanf(tag, "#%d", &value)
	return value
}
