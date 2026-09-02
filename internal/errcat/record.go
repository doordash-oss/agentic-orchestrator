// Copyright 2026 DoorDash, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package errcat

// FailureRecord is the durable canonical failure stored on a run: a catalog
// code, the typed context blocks known at failure time, and raw diagnostics.
// No rendered text is persisted; the catalog stays authoritative and the
// record is rendered through RenderRecord at read time.
type FailureRecord struct {
	Code        Code           `yaml:"code" json:"code"`
	Context     *RecordContext `yaml:"context,omitempty" json:"context,omitempty"`
	Diagnostics string         `yaml:"diagnostics,omitempty" json:"diagnostics,omitempty"`
}

// RecordContext carries the context blocks a stored failure record may hold.
// Only blocks the code's catalog entry declares survive rendering.
type RecordContext struct {
	Repositories []CodeRepository `yaml:"repositories,omitempty" json:"repositories,omitempty"`
	Phase        *CodePhase       `yaml:"phase,omitempty" json:"phase,omitempty"`
	Command      *CodeCommand     `yaml:"command,omitempty" json:"command,omitempty"`
}

// RenderRecord renders a stored failure record into the canonical error
// object through New. An unknown code falls back to the internal-error code,
// context blocks the code did not declare are dropped, and the summary
// template receives the phase and repository names from the record's blocks.
// Integration attention codes receive the full repositories block instead,
// so their summaries can name conflict-file and dirty-file counts and moved
// refs.
func RenderRecord(record FailureRecord) Error {
	opts := []Option{WithDiagnostics(record.Diagnostics)}
	params := RunFailureParams{}
	integration := IsIntegrationAttention(record.Code)
	integrationRepos := []CodeRepository{}
	if record.Context != nil {
		if record.Context.Phase != nil {
			opts = append(opts, WithPhase(*record.Context.Phase))
			params.Phase = record.Context.Phase.Name
			params.Iteration = record.Context.Phase.Iteration
		}
		if len(record.Context.Repositories) > 0 {
			repos := make([]CodeRepository, len(record.Context.Repositories))
			copy(repos, record.Context.Repositories)
			opts = append(opts, WithRepositories(repos...))
			integrationRepos = repos
			for _, repo := range record.Context.Repositories {
				if repo.Name != "" {
					params.Repositories = append(params.Repositories, repo.Name)
				}
			}
		}
		if record.Context.Command != nil {
			opts = append(opts, WithCommand(*record.Context.Command))
		}
	}
	if integration {
		opts = append(opts, WithParams(IntegrationRepoParams{Repositories: integrationRepos}))
	} else {
		opts = append(opts, WithParams(params))
	}
	return New(record.Code, opts...)
}
