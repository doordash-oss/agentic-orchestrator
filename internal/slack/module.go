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
	"context"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"go.uber.org/fx"
)

// Module always provides the live Slack service. Construction performs no
// validation and makes no network request.
var Module = fx.Module("slack",
	fx.Provide(func() ports.SlackService {
		return NewService()
	}),
	fx.Provide(newNotifier),
	fx.Invoke(registerNotifierLifecycle),
)

type notifierParams struct {
	fx.In
	Settings ports.SlackSettingsSource
	Reporter DeliveryReporter              `optional:"true"`
	Pending  ports.SlackPendingInputSource `optional:"true"`
	Answer   ports.SlackAnswerPort         `optional:"true"`
	Store    *feature.Store
	StateDir string `name:"stateDir"`
	Observer *observe.Observer
}

func newNotifier(p notifierParams) *Notifier {
	return NewNotifier(NotifierOptions{
		Settings: p.Settings,
		Reporter: p.Reporter,
		Pending:  p.Pending,
		Answer:   p.Answer,
		Store:    p.Store,
		StateDir: p.StateDir,
		Observer: p.Observer,
		NewClient: func(token string) (slackClient, error) {
			return NewClient(token)
		},
	})
}

func registerNotifierLifecycle(lc fx.Lifecycle, notifier *Notifier) {
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			notifier.Start()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			notifier.Stop(ctx)
			return nil
		},
	})
}
