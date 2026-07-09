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

package config

const defaultExitCriteria = `- Feature fully implemented per plan
- Unit tests added/updated as needed
- Integration tests added/updated as needed
- Code formatted per project standards
- Relevant tests pass
- No linting errors`

// NewDefaultRateLimitRetry returns the built-in rate-limit backoff defaults:
// enabled, a 6-attempt budget, 15s base delay doubling up to a 5m cap, with
// 20% jitter.
func NewDefaultRateLimitRetry() RateLimitRetryConfig {
	return RateLimitRetryConfig{
		Enabled:    true,
		MaxRetries: 6,
		BaseDelay:  "15s",
		MaxDelay:   "5m",
		Multiplier: 2.0,
		Jitter:     0.2,
	}
}

func NewDefault() *Config {
	return &Config{
		Defaults: DefaultsConfig{
			Models: ModelConfig{
				Inquiry:        "sonnet[200K]",
				Research:       "sonnet[200K]",
				Planning:       "sonnet[200K]",
				Implementation: "sonnet[200K]",
				Review:         "gpt-5.4[272K]",
				Utilities:      "sonnet[200K]",
				KBBuild:        "sonnet[200K]",
			},
			Checkpoints: Checkpoints{
				InquiryReview:   true,
				ResearchReview:  true,
				DesignReview:    true,
				RoadmapReview:   true,
				PhasePlanReview: true,
				ManualPublish:   true,
			},
			ExitCriteria:             defaultExitCriteria,
			Inquireness:              "high",
			Pipeline:                 "large",
			MaxIterations:            10,
			MaxConsecutiveFailures:   3,
			MaxConsecutiveNoProgress: 3,
			RateLimitRetry:           NewDefaultRateLimitRetry(),
		},
		Repos: make(map[string]RepoConfig),
		Observability: ObservabilityConfig{
			Events:          true,
			OTelServiceName: "agentico",
		},
	}
}
