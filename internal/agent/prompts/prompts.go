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

// TemplateKey names a template or partial registered via {{ define "name" }}
// in the .tmpl files under this package. Every template Go code renders has
// exactly one constant below; the constant's value is the define name.
type TemplateKey string

const (
	TemplateAutoReviewFormatRetry        TemplateKey = "autoreview.format_retry"
	TemplateAutoReviewUser               TemplateKey = "autoreview.user"
	TemplateDesignUser                   TemplateKey = "design.user"
	TemplateFinalFixUser                 TemplateKey = "final_fix.user"
	TemplateImplementUser                TemplateKey = "implement.user"
	TemplateImplementationReviewAxisUser TemplateKey = "implementation_review_axis.user"
	TemplateInquireUser                  TemplateKey = "inquire.user"
	TemplateKBBuildUser                  TemplateKey = "kb_build.user"
	TemplatePhasePlanUser                TemplateKey = "phase_plan.user"
	TemplatePhasePlanRevisionUser        TemplateKey = "phase_plan_revision.user"
	TemplatePRDescriptionUser            TemplateKey = "pr_description.user"
	TemplateResearchFromQuestionsUser    TemplateKey = "research_from_questions.user"
	TemplateReviewUser                   TemplateKey = "review.user"
	TemplateRoadmapUser                  TemplateKey = "roadmap.user"
	TemplateRoadmapRevisionUser          TemplateKey = "roadmap_revision.user"
	TemplateScoutUser                    TemplateKey = "scout.user"
	TemplateSummaryUser                  TemplateKey = "summary.user"
	TemplateSupervisorSystem             TemplateKey = "supervisor.system"
	TemplateSystem                       TemplateKey = "system"
	TemplateValidateSpecializedUser      TemplateKey = "validate_specialized.user"

	// Partials rendered directly from Go.
	TemplateAutonomousInquireness TemplateKey = "autonomous_inquireness"
	TemplateDeferralsDue          TemplateKey = "deferrals_due"
	TemplateGrillMeInquireness    TemplateKey = "grillme_inquireness"
	TemplateVisualReferences      TemplateKey = "visual_references"
)

// Render executes the keyed template against data and returns the result.
func Render(key TemplateKey, data any) (string, error) {
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, string(key), data); err != nil {
		return "", fmt.Errorf("rendering template %q: %w", key, err)
	}
	return buf.String(), nil
}

// MustRender is the panicking variant. Use it only when the inputs are
// statically known to be valid (e.g. constant directives) so a failure is
// genuinely a programmer error.
func MustRender(key TemplateKey, data any) string {
	out, err := Render(key, data)
	if err != nil {
		panic(err)
	}
	return out
}
