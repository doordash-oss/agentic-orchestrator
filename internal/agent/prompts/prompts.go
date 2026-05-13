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

// Render executes the named template against data and returns the result.
// Templates are referenced by the name registered via {{ define "name" }};
// see the .tmpl files under this package.
func Render(name string, data any) (string, error) {
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		return "", fmt.Errorf("rendering template %q: %w", name, err)
	}
	return buf.String(), nil
}

// MustRender is the panicking variant. Use it only when the inputs are
// statically known to be valid (e.g. constant directives) so a failure is
// genuinely a programmer error.
func MustRender(name string, data any) string {
	out, err := Render(name, data)
	if err != nil {
		panic(err)
	}
	return out
}
