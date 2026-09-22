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

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

const problemFallbackTextLimit = 1200

// renderRootCard builds the Block Kit root card for a feature: a header
// with the feature name, a section with the server, pipeline, repositories,
// phase, and status (plus pull request links once any exist), and a context
// line with the last update rendered as a Slack date token.
func renderRootCard(
	serverName string,
	f, activeChild *feature.Feature,
	now time.Time,
	waiting ...string,
) ([]Block, string) {
	name := safePlain(f.Name, headerTextLimit)
	status := cardStatus(f, activeChild)
	fields := []textObject{
		labeledField("Server", serverName),
		labeledField("Pipeline", humanisePipeline(f.EffectivePipeline())),
		labeledField("Repositories", repoList(f)),
		labeledField("Phase", phaseWithRoadmap(f)),
		labeledField("Status", status),
	}
	if prs := prLinks(f); prs != "" {
		fields = append(fields, labeledFieldRaw("Pull requests", prs))
	}
	if len(waiting) > 0 && waiting[0] != "" {
		fields = append(fields, labeledField("Waiting on you", waiting[0]))
	}
	blocks := []Block{
		headerBlockFor(name),
		sectionBlockFor(fields),
		contextBlockFor([]textObject{{
			Type: textTypeMrkdwn,
			Text: slackDateToken(now.Unix(), now.UTC().Format("2006-01-02 15:04 MST")),
		}}),
	}
	fallback := safePlain(strings.TrimSpace(
		fmt.Sprintf("%s — %s", name, status),
	), fallbackTextLimit)
	if len(waiting) > 0 && waiting[0] != "" {
		fallback = safePlain(fallback+" — Waiting on you: "+waiting[0], fallbackTextLimit)
	}
	return blocks, fallback
}

func cardStatus(f, activeChild *feature.Feature) string {
	var blocking, needsAction string
	consider := func(record *errcat.FailureRecord) {
		if record == nil {
			return
		}
		rendered := errcat.RenderRecord(*record)
		switch rendered.Class {
		case errcat.ClassBlocking:
			if blocking == "" {
				blocking = rendered.Title
			}
		case errcat.ClassNeedsAction:
			if needsAction == "" {
				needsAction = rendered.Title
			}
		}
	}
	if task := f.FailedSetupTask(); task != nil && task.Error != nil {
		consider(task.Error)
	} else {
		consider(f.FailureRecord())
	}
	for _, repo := range f.Repos {
		if state := f.RepoStates[repo.Name]; state != nil {
			consider(state.Error)
		}
	}
	if activeChild != nil {
		consider(activeChild.IntegrationAttentionRecord())
		consider(activeChild.FailureRecord())
	}
	if blocking != "" {
		return "Failed: " + blocking
	}
	if needsAction != "" {
		return "Needs your action: " + needsAction
	}
	return humaniseStatus(f.Status.String())
}

func renderProblem(token string, problem errcat.Error, f *feature.Feature) ([]Block, string, string) {
	problem = redactedError(token, problem)
	prefix := ""
	if f != nil && f.IsChild() {
		prefix, _ = childAffix(f.Parent.Kind)
		prefix = scrub(token, prefix)
		if prefix != "" {
			prefix += ": "
		}
	}
	emoji := "🛑"
	classLabel := "blocking"
	if problem.Class == errcat.ClassNeedsAction {
		emoji = "🚧"
		classLabel = "needs your action"
	}
	title := prefix + problem.Title
	titleLine := "*" + emoji + " " + safeText(title, 500) + "*"
	summaryBudget := sectionTextLimit - len(titleLine) - 1
	blocks := []Block{
		sectionTextBlockFor(titleLine + "\n" + safeText(problem.Summary, summaryBudget)),
	}
	if problem.Remediation != nil {
		text := problem.Remediation.Hint
		if len(problem.Remediation.Actions) > 0 {
			text += "\nActions: " + strings.Join(problem.Remediation.Actions, ", ")
		}
		if strings.TrimSpace(text) != "" {
			blocks = append(blocks, sectionTextBlockFor("*What to do:* "+safeText(text, sectionTextLimit-14)))
		}
	}
	if details := problemDetails(problem.Context); details != "" {
		blocks = append(blocks, sectionTextBlockFor("*Details:* "+safeText(details, sectionTextLimit-12)))
	}
	if problem.Diagnostics != "" {
		const note = "\n_Diagnostics were shortened. Open Agentico for the full text._"
		diagnostics := strings.ReplaceAll(problem.Diagnostics, "```", "'''")
		escaped := safeText(diagnostics, 0)
		text := "```\n" + escaped + "\n```"
		cut := len(text) > sectionTextLimit
		if cut {
			bodyBudget := sectionTextLimit - len("```\n\n```") - len(note)
			escaped = truncateEscapedText(diagnostics, bodyBudget)
			text = "```\n" + escaped + "\n```" + note
		}
		blocks = append(blocks, sectionTextBlockFor(text))
	}
	blocks = append(blocks, contextBlockFor([]textObject{{
		Type: textTypeMrkdwn,
		Text: safeText(fmt.Sprintf("Code: %s · Class: %s", problem.Code, classLabel), contextTextLimit),
	}}))
	fallback := problemFallback(emoji, title, classLabel, problem)
	return blocks, fallback, string(problem.Code)
}

func problemFallback(emoji, title, classLabel string, problem errcat.Error) string {
	const (
		preferredSummaryBudget          = 300
		preferredDiagnosticsBudget      = 180
		preferredDetailsBudget          = 500
		problemDiagnosticsPointer       = "Open Agentico for the full diagnostics."
		problemFallbackPartSeparator    = " | "
		problemFallbackDiagnosticsLabel = "Diagnostics: "
	)

	titlePart := emoji + " " + safePlain(title, 0)
	recoveryPart := ""
	if problem.Remediation != nil {
		recovery := ""
		if len(problem.Remediation.Actions) > 0 {
			recovery = "Actions: " + strings.Join(problem.Remediation.Actions, ", ") + ". "
		}
		recovery += problem.Remediation.Hint
		if recovery = strings.TrimSpace(recovery); recovery != "" {
			recoveryPart = "Next: " + safePlain(recovery, 0)
		}
	}
	detailsPart := ""
	if details := problemDetails(problem.Context); details != "" {
		detailsPart = "Details: " + abbreviateFallbackDetails(
			safePlain(details, 0),
			preferredDetailsBudget-len("Details: "),
		)
	}
	codePart := "Code: " + safePlain(
		fmt.Sprintf("%s (%s)", problem.Code, classLabel),
		0,
	)

	required := compactStrings(
		titlePart,
		recoveryPart,
		detailsPart,
		codePart,
	)
	if problem.Diagnostics != "" {
		required = append(required, problemDiagnosticsPointer)
	}
	requiredLength := len(strings.Join(required, problemFallbackPartSeparator))
	secondaryBudget := problemFallbackTextLimit - requiredLength
	if secondaryBudget > 0 {
		secondaryBudget -= len(problemFallbackPartSeparator)
	}

	summaryBudget := min(preferredSummaryBudget, max(0, secondaryBudget-preferredDiagnosticsBudget))
	summaryPart := ""
	if summary := abbreviateFallbackText(problem.Summary, summaryBudget); summary != "" {
		summaryPart = "Summary: " + summary
		secondaryBudget -= len(summaryPart) + len(problemFallbackPartSeparator)
	}

	diagnosticsPart := ""
	if problem.Diagnostics != "" {
		diagnosticsBudget := max(
			0,
			secondaryBudget-len(problemFallbackDiagnosticsLabel)-len(problemDiagnosticsPointer)-1,
		)
		if diagnosticsBudget > preferredDiagnosticsBudget {
			diagnosticsBudget = preferredDiagnosticsBudget
		}
		if diagnostics := abbreviateFallbackText(problem.Diagnostics, diagnosticsBudget); diagnostics != "" {
			diagnosticsPart = problemFallbackDiagnosticsLabel + diagnostics + " " + problemDiagnosticsPointer
		} else {
			diagnosticsPart = problemDiagnosticsPointer
		}
	}

	parts := compactStrings(
		titlePart,
		summaryPart,
		recoveryPart,
		detailsPart,
		codePart,
		diagnosticsPart,
	)
	return safePlain(strings.Join(parts, problemFallbackPartSeparator), problemFallbackTextLimit)
}

func compactStrings(values ...string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func abbreviateFallbackText(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" || limit <= 0 {
		return ""
	}
	if len(text) <= limit {
		return text
	}

	const ellipsis = "..."
	if limit <= len(ellipsis) {
		return ellipsis[:limit]
	}
	candidate := truncateUTF8(text, limit-len(ellipsis))
	boundary := -1
	for _, marker := range []string{". ", "; ", ": ", ", "} {
		if index := strings.LastIndex(candidate, marker); index >= len(candidate)/3 {
			boundary = index + 1
			break
		}
	}
	if boundary < 0 {
		boundary = strings.LastIndexByte(candidate, ' ')
	}
	if boundary > 0 {
		candidate = candidate[:boundary]
	}
	candidate = strings.TrimSpace(candidate)
	if strings.HasSuffix(candidate, ".") {
		candidate = strings.TrimRight(candidate, ".")
	}
	return candidate + ellipsis
}

func abbreviateFallbackDetails(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" || limit <= 0 {
		return ""
	}
	if len(text) <= limit {
		return text
	}

	const listEllipsis = ", ..."
	if limit <= len(listEllipsis) {
		return listEllipsis[:limit]
	}
	candidate := truncateUTF8(text, limit-len(listEllipsis))
	if boundary := strings.LastIndex(candidate, ", "); boundary > 0 {
		return strings.TrimSpace(candidate[:boundary]) + listEllipsis
	}
	return abbreviateFallbackText(text, limit)
}

func redactedError(token string, problem errcat.Error) errcat.Error {
	problem.Title = scrub(token, problem.Title)
	problem.Summary = scrub(token, problem.Summary)
	problem.Diagnostics = scrub(token, problem.Diagnostics)
	if problem.Remediation != nil {
		copy := *problem.Remediation
		copy.Hint = scrub(token, copy.Hint)
		copy.Actions = append([]string(nil), copy.Actions...)
		for i := range copy.Actions {
			copy.Actions[i] = scrub(token, copy.Actions[i])
		}
		problem.Remediation = &copy
	}
	if problem.Context != nil {
		copy := *problem.Context
		copy.Repositories = append([]errcat.CodeRepository(nil), copy.Repositories...)
		for i := range copy.Repositories {
			copy.Repositories[i].Name = scrub(token, copy.Repositories[i].Name)
			copy.Repositories[i].Branch = scrub(token, copy.Repositories[i].Branch)
			copy.Repositories[i].RebaseTarget = scrub(token, copy.Repositories[i].RebaseTarget)
			copy.Repositories[i].ConflictFiles = redactStrings(token, copy.Repositories[i].ConflictFiles)
			copy.Repositories[i].DirtyFiles = redactStrings(token, copy.Repositories[i].DirtyFiles)
			copy.Repositories[i].ParentAnchorSHA = scrub(token, copy.Repositories[i].ParentAnchorSHA)
			copy.Repositories[i].ExpectedRefSHA = scrub(token, copy.Repositories[i].ExpectedRefSHA)
			copy.Repositories[i].ChildHeadSHA = scrub(token, copy.Repositories[i].ChildHeadSHA)
			copy.Repositories[i].CandidateSHA = scrub(token, copy.Repositories[i].CandidateSHA)
			copy.Repositories[i].MergeHEAD = scrub(token, copy.Repositories[i].MergeHEAD)
			copy.Repositories[i].ObservedSHA = scrub(token, copy.Repositories[i].ObservedSHA)
		}
		if copy.Phase != nil {
			value := *copy.Phase
			value.Name = scrub(token, value.Name)
			copy.Phase = &value
		}
		if copy.Command != nil {
			value := *copy.Command
			value.LogPaths = redactStrings(token, value.LogPaths)
			copy.Command = &value
		}
		if copy.SetupTask != nil {
			value := *copy.SetupTask
			value.Key = scrub(token, value.Key)
			value.Kind = scrub(token, value.Kind)
			value.Label = scrub(token, value.Label)
			copy.SetupTask = &value
		}
		problem.Context = &copy
	}
	return problem
}

func redactStrings(token string, values []string) []string {
	out := append([]string(nil), values...)
	for i := range out {
		out[i] = scrub(token, out[i])
	}
	return out
}

func problemDetails(ctx *errcat.Context) string {
	if ctx == nil {
		return ""
	}
	var parts []string
	for _, repo := range ctx.Repositories {
		value := repo.Name
		if repo.Branch != "" {
			value += " (" + repo.Branch + ")"
		}
		if repo.RebaseTarget != "" {
			value += "; target: " + repo.RebaseTarget
		}
		if repo.RemoteOnlyCommits > 0 {
			value += fmt.Sprintf("; remote-only commits: %d", repo.RemoteOnlyCommits)
		}
		if len(repo.ConflictFiles) > 0 {
			value += "; conflicts: " + strings.Join(repo.ConflictFiles, ", ")
		}
		if len(repo.DirtyFiles) > 0 {
			value += "; dirty: " + strings.Join(repo.DirtyFiles, ", ")
		}
		parts = append(parts, "repository "+value)
	}
	if ctx.Phase != nil {
		value := ctx.Phase.Name
		if ctx.Phase.Iteration > 0 {
			value += fmt.Sprintf(" (iteration %d)", ctx.Phase.Iteration)
		}
		parts = append(parts, "phase "+value)
	}
	if ctx.SetupTask != nil {
		parts = append(parts, "setup task "+firstNonempty(ctx.SetupTask.Label, ctx.SetupTask.Key))
	}
	if ctx.Command != nil {
		value := fmt.Sprintf("command exit %d", ctx.Command.ExitCode)
		if len(ctx.Command.LogPaths) > 0 {
			value += "; logs: " + strings.Join(ctx.Command.LogPaths, ", ")
		}
		parts = append(parts, value)
	}
	return strings.Join(parts, " · ")
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
		Text: prefix + truncateMrkdwnTokens(value, budget),
	}
}

func repoList(f *feature.Feature) string {
	names := make([]string, 0, len(f.Repos))
	for _, repo := range f.Repos {
		names = append(names, repo.Name)
	}
	list := strings.Join(names, ", ")
	return boundDisplayText(list, repoFieldLimit)
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
	if (f.CurrentPhase == feature.PhasePlan || f.CurrentPhase == feature.PhaseImplement) &&
		f.TotalRoadmapPhases > 0 && f.CurrentRoadmapPhase > 0 {
		phase += fmt.Sprintf(" (roadmap phase %d of %d)", f.CurrentRoadmapPhase, f.TotalRoadmapPhases)
	}
	return phase
}

// renderProgress builds the one-line Progress reply for a handled event.
// An empty line means the event posts no reply. The line is the
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
	case ports.FeatureInterrupted:
		text = "Interrupted. Open Agentico to review or resume the feature."
		emoji = "⏹️"
	case ports.FeatureRewound:
		text = "Rewound to " + phaseTitle(ev.Phase)
		if (ev.Phase == feature.PhasePlan || ev.Phase == feature.PhaseImplement) &&
			f.CurrentRoadmapPhase > 0 {
			text += fmt.Sprintf(" for roadmap phase %d", f.CurrentRoadmapPhase)
		}
		text += fmt.Sprintf(" in run %d", f.ActiveRun)
		emoji = "⏪"
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
