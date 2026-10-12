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

package prompts

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"text/template"
)

// funcs are the helpers exposed inside templates. Keep this list small and
// boring: every helper here is something a template author should be able to
// reach for without surprises.
var funcs = template.FuncMap{
	"inc":  func(i int) int { return i + 1 },
	"base": filepath.Base,
	// blockquote rewrites a multi-line string so each line is prefixed with
	// "> " when interpolated after a leading "> " marker. Mirrors the
	// strings.ReplaceAll(desc, "\n", "\n> ") idiom used in BuildInquirePrompt.
	"blockquote": func(s string) string {
		return strings.ReplaceAll(s, "\n", "\n> ")
	},
}

// tmpl is the parsed template set. Parsing happens at package init via
// template.Must, so any malformed template (typo, bad action, missing
// partial) fails the binary at startup rather than at first phase render.
var tmpl = template.Must(
	template.New("agentic").Funcs(funcs).ParseFS(fs, "templates/*.tmpl", "partials/*.tmpl"),
)

// templateKey names a template or partial registered via {{ define "name" }}
// in the .tmpl files under this package. Every template Go code renders has
// exactly one constant below; the constant's value is the define name.
type templateKey string

const (
	TemplateAutoReviewFormatRetry        templateKey = "autoreview.format_retry"
	TemplateAutoReviewUser               templateKey = "autoreview.user"
	TemplateDesignUser                   templateKey = "design.user"
	TemplateFinalFixUser                 templateKey = "final_fix.user"
	TemplateImplementUser                templateKey = "implement.user"
	templateImplementationReviewAxisUser templateKey = "implementation_review_axis.user"
	TemplateInquireUser                  templateKey = "inquire.user"
	TemplateKBBuildUser                  templateKey = "kb_build.user"
	TemplatePhasePlanUser                templateKey = "phase_plan.user"
	TemplatePhasePlanRevisionUser        templateKey = "phase_plan_revision.user"
	TemplatePRDescriptionUser            templateKey = "pr_description.user"
	TemplateResearchFromQuestionsUser    templateKey = "research_from_questions.user"
	templateReviewUser                   templateKey = "review.user"
	TemplateRoadmapUser                  templateKey = "roadmap.user"
	TemplateRoadmapRevisionUser          templateKey = "roadmap_revision.user"
	templateScoutUser                    templateKey = "scout.user"
	templateSummaryUser                  templateKey = "summary.user"
	templateSupervisorSystem             templateKey = "supervisor.system"
	TemplateSystem                       templateKey = "system"
	TemplateValidateSpecializedUser      templateKey = "validate_specialized.user"

	// Partials rendered directly from Go.
	TemplateAutonomousInquireness templateKey = "autonomous_inquireness"
	TemplateDeferralsDue          templateKey = "deferrals_due"
	TemplateGrillMeInquireness    templateKey = "grillme_inquireness"
	TemplateVisualReferences      templateKey = "visual_references"
)

// render executes the keyed template against data and returns the result.
func render(key templateKey, data any) (string, error) {
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, string(key), data); err != nil {
		return "", fmt.Errorf("rendering template %q: %w", key, err)
	}
	return buf.String(), nil
}

// MustRender is the panicking variant. Use it only when the inputs are
// statically known to be valid (e.g. constant directives) so a failure is
// genuinely a programmer error.
func MustRender(key templateKey, data any) string {
	out, err := render(key, data)
	if err != nil {
		panic(err)
	}
	return out
}
