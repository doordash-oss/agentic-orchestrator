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

package opencode

import (
	"encoding/json"
	"reflect"
	"sort"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// discoverEffortVariants uses only the effective metadata OpenCode reports.
// Custom aliases with a standard reasoningEffort value are accepted; opaque
// custom names are not assigned an invented position on the effort scale.
func discoverEffortVariants(variants map[string]map[string]any) (map[llm.EffortLevel]map[string]any, []llm.EffortLevel) {
	result := make(map[llm.EffortLevel]map[string]any)
	names := make([]string, 0, len(variants))
	for name := range variants {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		options := variants[name]
		if options["disabled"] == true {
			continue
		}
		level := llm.EffortLevel(name)
		if !llm.IsValidExplicitEffort(level) || level == llm.EffortAuto {
			value, _ := options["reasoningEffort"].(string)
			level = llm.EffortLevel(value)
		}
		if !llm.IsValidExplicitEffort(level) || level == llm.EffortAuto {
			continue
		}
		if _, exists := result[level]; exists && name != string(level) {
			continue
		}
		copied := make(map[string]any)
		for k, v := range options {
			if k != "disabled" {
				copied[k] = v
			}
		}
		if len(copied) > 0 {
			result[level] = copied
		}
	}
	var levels []llm.EffortLevel
	for _, level := range llm.AllEffortLevels {
		options, ok := result[level]
		if !ok {
			continue
		}
		duplicate := false
		for _, previous := range levels {
			if reflect.DeepEqual(options, result[previous]) {
				duplicate = true
				break
			}
		}
		if duplicate {
			delete(result, level)
		} else {
			levels = append(levels, level)
		}
	}
	return result, levels
}

func cloneCatalog(models []llm.ModelInfo) []llm.ModelInfo {
	// Catalog metadata is JSON-shaped. Deep copies keep nested variant options
	// isolated across refreshes, callers and concurrently built sessions.
	data, err := json.Marshal(models)
	if err != nil {
		return nil
	}
	var result []llm.ModelInfo
	if json.Unmarshal(data, &result) != nil {
		return nil
	}
	return result
}

func (p *Provider) effortOptions(model string, level llm.EffortLevel) map[string]any {
	backend := BackendModel(model)
	for _, info := range p.ModelCatalog() {
		if BackendModel(info.ID) == backend {
			return info.EffortVariants[level]
		}
		for _, alias := range info.Aliases {
			if BackendModel(alias) == backend {
				return info.EffortVariants[level]
			}
		}
	}
	return nil
}
