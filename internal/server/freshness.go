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

package server

import "github.com/doordash-oss/agentic-orchestrator/internal/feature"

type RepoFreshness string

const (
	RepoFreshnessInSync       RepoFreshness = "in sync"
	RepoFreshnessLocalChanges RepoFreshness = "local changes"
	RepoFreshnessLocalOnly    RepoFreshness = "local only"
	RepoFreshnessUnknown      RepoFreshness = "unknown"
)

type RepoFreshnessProvider interface {
	Freshness(f *feature.Feature, repo feature.FeatureRepo) RepoFreshness
}

type StaticFreshnessProvider map[string]RepoFreshness

func (p StaticFreshnessProvider) Freshness(_ *feature.Feature, repo feature.FeatureRepo) RepoFreshness {
	if status, ok := p[repo.Name]; ok {
		return status
	}
	return RepoFreshnessUnknown
}
