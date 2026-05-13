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

// Package prompts owns the agent prompt templates and the renderer that
// produces per-phase user and system prompts.
//
// The goal is human-reviewable prompts: the literal prose lives in *.tmpl
// files and Go code only supplies the input data. A reviewer can open a
// single .tmpl file and read top-to-bottom what the model will see —
// without having to mentally execute Go code.
//
// Layout:
//   - templates/ holds the top-level templates that callers render directly
//     (one per phase user prompt, plus shared system prompts).
//   - partials/  holds reusable fragments referenced via {{ template "..." }}
//     from the top-level templates.
//   - testdata/  holds .golden snapshot fixtures.
package prompts

import "embed"

//go:embed templates/*.tmpl partials/*.tmpl
var fs embed.FS
