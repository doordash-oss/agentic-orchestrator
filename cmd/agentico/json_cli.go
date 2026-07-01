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

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	serverruntime "github.com/doordash-oss/agentic-orchestrator/internal/server"
)

const cliJSONSchemaVersion = 1

const (
	jsonWatchHeartbeatInterval = 30 * time.Second
	jsonWatchReconnectDelay    = 250 * time.Millisecond
	jsonWatchMaxReconnects     = 3
	jsonWatchPollInterval      = 500 * time.Millisecond
)

type jsonCommandOptions struct {
	Args                       []string
	ConfigPath                 string
	StateDir                   string
	DangerouslySkipPermissions bool
	EnabledProviders           []string
	RefreshModels              bool
	Stdout                     io.Writer
	Stderr                     io.Writer
	Deps                       jsonCommandDeps
}

type jsonCommandDeps struct {
	Connect      func(context.Context, jsonCommandOptions) (jsonCLIClient, error)
	EnsureServer func(context.Context, defaultLaunchRequest) (jsonServerEnsureResult, error)
}

type jsonCLIClient interface{}

type jsonServerEnsureResult struct {
	BaseURL      string                        `json:"base_url"`
	Runtime      serverruntime.RuntimeIdentity `json:"runtime"`
	LaunchPolicy serverruntime.LaunchPolicy    `json:"launch_policy"`
	OwnedServer  bool                          `json:"owned_server"`
	Status       string                        `json:"status"`
}

type cliJSONEnvelope struct {
	SchemaVersion int           `json:"schema_version"`
	APIVersion    string        `json:"api_version"`
	OK            bool          `json:"ok"`
	Result        any           `json:"result,omitempty"`
	Error         *cliJSONError `json:"error,omitempty"`
}

type cliJSONError struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Target  map[string]any `json:"target,omitempty"`
}

type parsedJSONCommand struct {
	parts     []string
	json      bool
	watch     bool
	serverURL string
	inputJSON string
	inputFile string
}

func isJSONAutomationCommand(args []string) bool {
	for _, arg := range args {
		if arg == "--json" {
			return true
		}
	}
	return false
}

func parseJSONLaunchArgs(args []string) (launchOptions, []string, error) {
	opts := defaultLaunchOptions()
	jsonArgs := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--config":
			if i+1 >= len(args) {
				return opts, nil, errors.New("--config requires a value")
			}
			i++
			opts.configPath = args[i]
		case "--state-dir":
			if i+1 >= len(args) {
				return opts, nil, errors.New("--state-dir requires a value")
			}
			i++
			opts.stateDir = args[i]
		case "--dangerously-skip-permissions":
			opts.dangerouslySkipPerms = true
		case "--providers":
			if i+1 >= len(args) {
				return opts, nil, errors.New("--providers requires a value")
			}
			i++
			opts.enabledProviders = strings.Split(args[i], ",")
		case "--refresh-models":
			opts.refreshModels = true
		default:
			jsonArgs = append(jsonArgs, arg)
		}
	}
	return opts, jsonArgs, nil
}

func runJSONCommand(ctx context.Context, opts jsonCommandOptions) int {
	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}
	if opts.Stderr == nil {
		opts.Stderr = io.Discard
	}
	opts.Deps = opts.Deps.withDefaults()
	parsed, err := parseJSONCommandArgs(opts.Args)
	if err != nil {
		return writeCLIJSONError(opts.Stdout, "invalid_input", err.Error(), nil)
	}
	if !parsed.json {
		return writeCLIJSONError(opts.Stdout, "invalid_input", "--json is required for automation commands", nil)
	}
	switch {
	case len(parsed.parts) == 2 && parsed.parts[0] == "server" && parsed.parts[1] == "ensure":
		req := defaultLaunchRequest{
			ConfigPath:                 opts.ConfigPath,
			StateDir:                   opts.StateDir,
			DangerouslySkipPermissions: opts.DangerouslySkipPermissions,
			EnabledProviders:           opts.EnabledProviders,
			RefreshModels:              opts.RefreshModels,
			Stdout:                     opts.Stderr,
			Stderr:                     opts.Stderr,
		}
		result, err := opts.Deps.EnsureServer(ctx, req)
		if err != nil {
			return writeCLIJSONError(opts.Stdout, classifyCLIJSONError(err), err.Error(), nil)
		}
		return writeCLIJSONResult(opts.Stdout, result)
	case len(parsed.parts) >= 2 && parsed.parts[0] == "feature":
		return runFeatureJSONCommand(ctx, opts, parsed)
	default:
		return writeCLIJSONError(opts.Stdout, "invalid_input", "unsupported JSON command", nil)
	}
}

func (deps jsonCommandDeps) withDefaults() jsonCommandDeps {
	if deps.EnsureServer == nil {
		deps.EnsureServer = ensureServerForJSON
	}
	if deps.Connect == nil {
		deps.Connect = connectJSONCommandClient
	}
	return deps
}

func parseJSONCommandArgs(args []string) (parsedJSONCommand, error) {
	var out parsedJSONCommand
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			out.json = true
		case "--watch":
			out.watch = true
		case "--server":
			if i+1 >= len(args) {
				return out, errors.New("--server requires a value")
			}
			i++
			out.serverURL = args[i]
		case "--input-json", "--request-json":
			if i+1 >= len(args) {
				return out, fmt.Errorf("%s requires a value", args[i])
			}
			i++
			out.inputJSON = args[i]
		case "--input-file", "--request-file":
			if i+1 >= len(args) {
				return out, fmt.Errorf("%s requires a value", args[i])
			}
			i++
			out.inputFile = args[i]
		default:
			if strings.HasPrefix(args[i], "-") {
				return out, fmt.Errorf("unknown JSON flag: %s", args[i])
			}
			out.parts = append(out.parts, args[i])
		}
	}
	return out, nil
}

func connectJSONCommandClient(ctx context.Context, opts jsonCommandOptions) (jsonCLIClient, error) {
	parsed, err := parseJSONCommandArgs(opts.Args)
	if err != nil {
		return nil, err
	}
	baseURL := strings.TrimSpace(parsed.serverURL)
	if baseURL == "" {
		req := defaultLaunchRequest{
			ConfigPath:                 opts.ConfigPath,
			StateDir:                   opts.StateDir,
			DangerouslySkipPermissions: opts.DangerouslySkipPermissions,
			EnabledProviders:           opts.EnabledProviders,
			RefreshModels:              opts.RefreshModels,
			Stdout:                     opts.Stderr,
			Stderr:                     opts.Stderr,
		}
		result, err := opts.Deps.withDefaults().EnsureServer(ctx, req)
		if err != nil {
			return nil, err
		}
		baseURL = result.BaseURL
	}
	client, err := serverruntime.NewClient(serverruntime.ClientOptions{BaseURL: baseURL})
	if err != nil {
		return nil, err
	}
	return client, nil
}

func ensureServerForJSON(ctx context.Context, req defaultLaunchRequest) (jsonServerEnsureResult, error) {
	var captured defaultClientLaunch
	err := launchDefaultClientServer(ctx, req, defaultLaunchDeps{
		LaunchClient: func(_ context.Context, launch defaultClientLaunch) error {
			captured = launch
			return nil
		},
	})
	if err != nil {
		return jsonServerEnsureResult{}, err
	}
	status := "attached"
	if captured.OwnedServer {
		status = "started"
	}
	return jsonServerEnsureResult{
		BaseURL:      captured.BaseURL,
		Runtime:      captured.Runtime,
		LaunchPolicy: captured.LaunchPolicy,
		OwnedServer:  captured.OwnedServer,
		Status:       status,
	}, nil
}

func runFeatureJSONCommand(ctx context.Context, opts jsonCommandOptions, parsed parsedJSONCommand) int {
	if len(parsed.parts) < 2 {
		return writeCLIJSONError(opts.Stdout, "invalid_input", "feature subcommand is required", nil)
	}
	client, err := opts.Deps.Connect(ctx, opts)
	if err != nil {
		return writeCLIJSONError(opts.Stdout, classifyCLIJSONError(err), err.Error(), nil)
	}
	switch parsed.parts[1] {
	case "select":
		c, ok := client.(interface {
			Features(context.Context) (serverruntime.FeatureListResponse, error)
		})
		if !ok {
			return writeCLIJSONError(opts.Stdout, "unsupported_action", "feature select is not supported by this client", nil)
		}
		resp, err := c.Features(ctx)
		return writeCLIJSONResponse(opts.Stdout, resp, err)
	case "detail":
		if len(parsed.parts) != 3 {
			return writeCLIJSONError(opts.Stdout, "invalid_input", "feature detail requires <feature-id>", nil)
		}
		c, ok := client.(interface {
			FeatureDetail(context.Context, string) (serverruntime.FeatureDetailResponse, error)
		})
		if !ok {
			return writeCLIJSONError(opts.Stdout, "unsupported_action", "feature detail is not supported by this client", nil)
		}
		resp, err := c.FeatureDetail(ctx, parsed.parts[2])
		return writeCLIJSONResponse(opts.Stdout, resp, err)
	case "create":
		c, ok := client.(interface {
			CreateFeature(context.Context, serverruntime.CreateFeatureRequest) (serverruntime.CreateFeatureResponse, error)
		})
		if !ok {
			return writeCLIJSONError(opts.Stdout, "unsupported_action", "feature create is not supported by this client", nil)
		}
		var req serverruntime.CreateFeatureRequest
		if err := decodeJSONCommandInput(parsed, &req); err != nil {
			return writeCLIJSONError(opts.Stdout, "invalid_input", err.Error(), nil)
		}
		resp, err := c.CreateFeature(ctx, req)
		return writeCLIJSONResponse(opts.Stdout, resp, err)
	case "action":
		return runFeatureActionJSONCommand(ctx, opts.Stdout, client, parsed)
	case "answer":
		return runFeatureAnswerJSONCommand(ctx, opts.Stdout, client, parsed)
	case "review":
		if len(parsed.parts) != 3 {
			return writeCLIJSONError(opts.Stdout, "invalid_input", "feature review requires <feature-id>", nil)
		}
		c, ok := client.(interface {
			ReviewDecision(context.Context, string, serverruntime.ReviewDecisionRequest) (serverruntime.ReviewDecisionResponse, error)
		})
		if !ok {
			return writeCLIJSONError(opts.Stdout, "unsupported_action", "feature review is not supported by this client", nil)
		}
		var req serverruntime.ReviewDecisionRequest
		if err := decodeJSONCommandInput(parsed, &req); err != nil {
			return writeCLIJSONError(opts.Stdout, "invalid_input", err.Error(), nil)
		}
		resp, err := c.ReviewDecision(ctx, parsed.parts[2], req)
		return writeCLIJSONResponse(opts.Stdout, resp, err)
	case "manage":
		return runFeatureManageJSONCommand(ctx, opts.Stdout, client, parsed)
	default:
		return writeCLIJSONError(opts.Stdout, "unsupported_action", "unsupported feature JSON subcommand", map[string]any{"subcommand": parsed.parts[1]})
	}
}

func runFeatureActionJSONCommand(ctx context.Context, w io.Writer, client jsonCLIClient, parsed parsedJSONCommand) int {
	if len(parsed.parts) != 4 {
		return writeCLIJSONError(w, "invalid_input", "feature action requires <feature-id> <action>", nil)
	}
	featureID, action := parsed.parts[2], parsed.parts[3]
	switch action {
	case "start":
		c, ok := client.(interface {
			StartFeature(context.Context, string) (serverruntime.FeatureStartResponse, error)
		})
		if !ok {
			return writeCLIJSONError(w, "unsupported_action", "start is not supported by this client", nil)
		}
		resp, err := c.StartFeature(ctx, featureID)
		return writeCLIJSONResponse(w, resp, err)
	case "resume":
		c, ok := client.(interface {
			ResumeFeature(context.Context, string) (serverruntime.FeatureStartResponse, error)
		})
		if !ok {
			return writeCLIJSONError(w, "unsupported_action", "resume is not supported by this client", nil)
		}
		resp, err := c.ResumeFeature(ctx, featureID)
		return writeCLIJSONResponse(w, resp, err)
	case "stop", "pause-stop":
		c, ok := client.(interface {
			StopFeature(context.Context, string) (serverruntime.FeatureStopResponse, error)
		})
		if !ok {
			return writeCLIJSONError(w, "unsupported_action", "stop is not supported by this client", nil)
		}
		resp, err := c.StopFeature(ctx, featureID)
		return writeCLIJSONResponse(w, resp, err)
	case "interrupt":
		c, ok := client.(interface {
			InterruptFeature(context.Context, string) (serverruntime.FeatureStopResponse, error)
		})
		if !ok {
			return writeCLIJSONError(w, "unsupported_action", "interrupt is not supported by this client", nil)
		}
		resp, err := c.InterruptFeature(ctx, featureID)
		return writeCLIJSONResponse(w, resp, err)
	case "restart":
		c, ok := client.(interface {
			RestartFeature(context.Context, string, serverruntime.RestartFeatureRequest) (serverruntime.FeatureRestartResponse, error)
		})
		if !ok {
			return writeCLIJSONError(w, "unsupported_action", "restart is not supported by this client", nil)
		}
		var req serverruntime.RestartFeatureRequest
		if err := decodeJSONCommandInput(parsed, &req); err != nil {
			return writeCLIJSONError(w, "invalid_input", err.Error(), nil)
		}
		resp, err := c.RestartFeature(ctx, featureID, req)
		return writeCLIJSONResponse(w, resp, err)
	case "publish":
		c, ok := client.(interface {
			PublishFeature(context.Context, string, serverruntime.PublishFeatureRequest) (serverruntime.PublishFeatureResponse, error)
		})
		if !ok {
			return writeCLIJSONError(w, "unsupported_action", "publish is not supported by this client", nil)
		}
		var req serverruntime.PublishFeatureRequest
		if err := decodeJSONCommandInput(parsed, &req); err != nil {
			return writeCLIJSONError(w, "invalid_input", err.Error(), nil)
		}
		resp, err := c.PublishFeature(ctx, featureID, req)
		return writeCLIJSONResponse(w, resp, err)
	case "merge":
		c, ok := client.(interface {
			MergeFeature(context.Context, string) (serverruntime.MergeFeatureResponse, error)
		})
		if !ok {
			return writeCLIJSONError(w, "unsupported_action", "merge is not supported by this client", nil)
		}
		resp, err := c.MergeFeature(ctx, featureID)
		return writeCLIJSONResponse(w, resp, err)
	case "rewind":
		c, ok := client.(interface {
			RewindFeature(context.Context, string, serverruntime.RewindFeatureRequest) (serverruntime.RewindFeatureResponse, error)
		})
		if !ok {
			return writeCLIJSONError(w, "unsupported_action", "rewind is not supported by this client", nil)
		}
		var req serverruntime.RewindFeatureRequest
		if err := decodeJSONCommandInput(parsed, &req); err != nil {
			return writeCLIJSONError(w, "invalid_input", err.Error(), nil)
		}
		resp, err := c.RewindFeature(ctx, featureID, req)
		return writeCLIJSONResponse(w, resp, err)
	case "retry":
		c, ok := client.(interface {
			RetryFeature(context.Context, string) (serverruntime.RetryFeatureResponse, error)
		})
		if !ok {
			return writeCLIJSONError(w, "unsupported_action", "retry is not supported by this client", nil)
		}
		resp, err := c.RetryFeature(ctx, featureID)
		return writeCLIJSONResponse(w, resp, err)
	case "rebase":
		c, ok := client.(interface {
			StartRebase(context.Context, string, serverruntime.RebaseActionRequest) (serverruntime.RebaseStartResponse, error)
		})
		if !ok {
			return writeCLIJSONError(w, "unsupported_action", "rebase is not supported by this client", nil)
		}
		var req serverruntime.RebaseActionRequest
		if err := decodeJSONCommandInput(parsed, &req); err != nil {
			return writeCLIJSONError(w, "invalid_input", err.Error(), nil)
		}
		resp, err := c.StartRebase(ctx, featureID, req)
		return writeCLIJSONResponse(w, resp, err)
	case "review-comments":
		c, ok := client.(interface {
			StartReviewComments(context.Context, string, serverruntime.ReviewCommentsActionRequest) (serverruntime.ReviewCommentsStartResponse, error)
		})
		if !ok {
			return writeCLIJSONError(w, "unsupported_action", "review-comments is not supported by this client", nil)
		}
		var req serverruntime.ReviewCommentsActionRequest
		if err := decodeJSONCommandInput(parsed, &req); err != nil {
			return writeCLIJSONError(w, "invalid_input", err.Error(), nil)
		}
		resp, err := c.StartReviewComments(ctx, featureID, req)
		return writeCLIJSONResponse(w, resp, err)
	case "review-comments-fetch":
		c, ok := client.(interface {
			FetchReviewComments(context.Context, string, serverruntime.ReviewCommentsFetchRequest) (serverruntime.ReviewCommentsFetchResponse, error)
		})
		if !ok {
			return writeCLIJSONError(w, "unsupported_action", "review-comments-fetch is not supported by this client", nil)
		}
		var req serverruntime.ReviewCommentsFetchRequest
		if err := decodeJSONCommandInput(parsed, &req); err != nil {
			return writeCLIJSONError(w, "invalid_input", err.Error(), nil)
		}
		resp, err := c.FetchReviewComments(ctx, featureID, req)
		return writeCLIJSONResponse(w, resp, err)
	case "tweak":
		c, ok := client.(interface {
			StartTweak(context.Context, string, serverruntime.TweakActionRequest) (serverruntime.TweakStartResponse, error)
		})
		if !ok {
			return writeCLIJSONError(w, "unsupported_action", "tweak is not supported by this client", nil)
		}
		resp, err := c.StartTweak(ctx, featureID, serverruntime.TweakActionRequest{})
		return writeCLIJSONResponse(w, resp, err)
	case "tweak-finish":
		c, ok := client.(interface {
			FinishTweak(context.Context, string, serverruntime.TweakFinishRequest) (serverruntime.TweakFinishResponse, error)
		})
		if !ok {
			return writeCLIJSONError(w, "unsupported_action", "tweak-finish is not supported by this client", nil)
		}
		var req serverruntime.TweakFinishRequest
		if err := decodeJSONCommandInput(parsed, &req); err != nil {
			return writeCLIJSONError(w, "invalid_input", err.Error(), nil)
		}
		resp, err := c.FinishTweak(ctx, featureID, req)
		return writeCLIJSONResponse(w, resp, err)
	case "refactor":
		c, ok := client.(interface {
			StartRefactor(context.Context, string, serverruntime.RefactorActionRequest) (serverruntime.RefactorStartResponse, error)
		})
		if !ok {
			return writeCLIJSONError(w, "unsupported_action", "refactor is not supported by this client", nil)
		}
		var req serverruntime.RefactorActionRequest
		if err := decodeJSONCommandInput(parsed, &req); err != nil {
			return writeCLIJSONError(w, "invalid_input", err.Error(), nil)
		}
		resp, err := c.StartRefactor(ctx, featureID, req)
		return writeCLIJSONResponse(w, resp, err)
	case "refactor-restart":
		c, ok := client.(interface {
			RestartRefactor(context.Context, string, serverruntime.RefactorActionRequest) (serverruntime.RefactorRestartResponse, error)
		})
		if !ok {
			return writeCLIJSONError(w, "unsupported_action", "refactor-restart is not supported by this client", nil)
		}
		var req serverruntime.RefactorActionRequest
		if err := decodeJSONCommandInput(parsed, &req); err != nil {
			return writeCLIJSONError(w, "invalid_input", err.Error(), nil)
		}
		resp, err := c.RestartRefactor(ctx, featureID, req)
		return writeCLIJSONResponse(w, resp, err)
	case "mark-done":
		c, ok := client.(interface {
			MarkDone(context.Context, string) (serverruntime.MarkDoneResponse, error)
		})
		if !ok {
			return writeCLIJSONError(w, "unsupported_action", "mark-done is not supported by this client", nil)
		}
		resp, err := c.MarkDone(ctx, featureID)
		return writeCLIJSONResponse(w, resp, err)
	case "cleanup":
		c, ok := client.(interface {
			CleanupFeature(context.Context, string, serverruntime.CleanupActionRequest) (serverruntime.CleanupFeatureResponse, error)
		})
		if !ok {
			return writeCLIJSONError(w, "unsupported_action", "cleanup is not supported by this client", nil)
		}
		var req serverruntime.CleanupActionRequest
		if err := decodeJSONCommandInput(parsed, &req); err != nil {
			return writeCLIJSONError(w, "invalid_input", err.Error(), nil)
		}
		resp, err := c.CleanupFeature(ctx, featureID, req)
		return writeCLIJSONResponse(w, resp, err)
	case "delete":
		c, ok := client.(interface {
			DeleteFeature(context.Context, string) (serverruntime.DeleteFeatureResponse, error)
		})
		if !ok {
			return writeCLIJSONError(w, "unsupported_action", "delete is not supported by this client", nil)
		}
		resp, err := c.DeleteFeature(ctx, featureID)
		return writeCLIJSONResponse(w, resp, err)
	default:
		return writeCLIJSONError(w, "unsupported_action", "unsupported feature action", map[string]any{"action": action})
	}
}

type jsonAnswerRequest struct {
	Kind      string            `json:"kind"`
	RequestID string            `json:"request_id,omitempty"`
	SessionID string            `json:"session_id,omitempty"`
	Decision  string            `json:"decision,omitempty"`
	Answers   map[string]string `json:"answers,omitempty"`
	RepoName  string            `json:"repo_name,omitempty"`
	CycleType string            `json:"cycle_type,omitempty"`
}

func runFeatureAnswerJSONCommand(ctx context.Context, w io.Writer, client jsonCLIClient, parsed parsedJSONCommand) int {
	if len(parsed.parts) != 3 {
		return writeCLIJSONError(w, "invalid_input", "feature answer requires <feature-id>", nil)
	}
	var req jsonAnswerRequest
	if err := decodeJSONCommandInput(parsed, &req); err != nil {
		return writeCLIJSONError(w, "invalid_input", err.Error(), nil)
	}
	switch normalizeJSONAction(req.Kind) {
	case "need-user-input":
		c, ok := client.(interface {
			NeedUserInputDecision(context.Context, string, serverruntime.NeedUserInputDecisionRequest) (serverruntime.NeedUserInputDecisionResponse, error)
		})
		if !ok {
			return writeCLIJSONError(w, "unsupported_action", "need-user-input answers are not supported by this client", nil)
		}
		resp, err := c.NeedUserInputDecision(ctx, parsed.parts[2], serverruntime.NeedUserInputDecisionRequest{
			Decision:  req.Decision,
			RepoName:  req.RepoName,
			CycleType: req.CycleType,
		})
		return writeCLIJSONResponse(w, resp, err)
	case "need-user-input-draft":
		c, ok := client.(interface {
			DraftNeedUserInputAnswers(context.Context, string, serverruntime.NeedUserInputDraftRequest) (serverruntime.NeedUserInputDraftResponse, error)
		})
		if !ok {
			return writeCLIJSONError(w, "unsupported_action", "need-user-input draft answers are not supported by this client", nil)
		}
		resp, err := c.DraftNeedUserInputAnswers(ctx, parsed.parts[2], serverruntime.NeedUserInputDraftRequest{
			RepoName:  req.RepoName,
			CycleType: req.CycleType,
			Answers:   req.Answers,
		})
		return writeCLIJSONResponse(w, resp, err)
	case "permission":
		c, ok := client.(interface {
			AnswerPermission(context.Context, serverruntime.PermissionAnswerRequest) (serverruntime.PermissionAnswerResponse, error)
		})
		if !ok {
			return writeCLIJSONError(w, "unsupported_action", "permission answers are not supported by this client", nil)
		}
		resp, err := c.AnswerPermission(ctx, serverruntime.PermissionAnswerRequest{
			RequestID: req.RequestID,
			SessionID: req.SessionID,
			Decision:  req.Decision,
		})
		return writeCLIJSONResponse(w, resp, err)
	case "ask-user":
		c, ok := client.(interface {
			AnswerAskUser(context.Context, serverruntime.AskUserAnswerRequest) (serverruntime.AskUserAnswerResponse, error)
		})
		if !ok {
			return writeCLIJSONError(w, "unsupported_action", "ask-user answers are not supported by this client", nil)
		}
		resp, err := c.AnswerAskUser(ctx, serverruntime.AskUserAnswerRequest{
			RequestID: req.RequestID,
			SessionID: req.SessionID,
			Answers:   req.Answers,
		})
		return writeCLIJSONResponse(w, resp, err)
	default:
		return writeCLIJSONError(w, "invalid_input", "answer kind is required", nil)
	}
}

func runFeatureManageJSONCommand(ctx context.Context, w io.Writer, client jsonCLIClient, parsed parsedJSONCommand) int {
	if len(parsed.parts) != 3 {
		return writeCLIJSONError(w, "invalid_input", "feature manage requires <feature-id>", nil)
	}
	c, ok := client.(interface {
		FeatureDetail(context.Context, string) (serverruntime.FeatureDetailResponse, error)
	})
	if !ok {
		return writeCLIJSONError(w, "unsupported_action", "feature manage is not supported by this client", nil)
	}
	resp, err := c.FeatureDetail(ctx, parsed.parts[2])
	if err != nil {
		return writeCLIJSONError(w, classifyCLIJSONError(err), err.Error(), cliJSONErrorTarget(err))
	}
	if !parsed.watch {
		return writeCLIJSONResult(w, resp)
	}
	enc := json.NewEncoder(w)
	watch := &jsonFeatureWatch{
		featureID: parsed.parts[2],
		detailer:  c,
		enc:       enc,
	}
	if snapshotter, ok := client.(jsonWatchSnapshotter); ok {
		watch.snapshotter = snapshotter
	}
	if stop, err := watch.emitInitial(resp.Feature); err != nil {
		return 1
	} else if stop {
		return 0
	}
	if subscriber, ok := client.(interface {
		SubscribeEvents(context.Context, serverruntime.EventSubscriptionOptions) (<-chan serverruntime.RefreshSignal, <-chan error)
	}); ok {
		if err := watch.watchEvents(ctx, subscriber); err == nil {
			return 0
		}
	}
	if err := watch.poll(ctx); err != nil && ctx.Err() == nil {
		return 1
	}
	return 0
}

type jsonWatchSnapshotter interface {
	FetchRefreshSnapshot(context.Context, serverruntime.RefreshSignal) (serverruntime.RefreshSnapshot, error)
}

type jsonFeatureWatch struct {
	featureID string
	detailer  interface {
		FeatureDetail(context.Context, string) (serverruntime.FeatureDetailResponse, error)
	}
	snapshotter     jsonWatchSnapshotter
	enc             *json.Encoder
	previous        serverruntime.FeatureDetailDTO
	havePrevious    bool
	terminalEmitted bool
}

type normalizedFeatureEvent struct {
	Type      string         `json:"type"`
	FeatureID string         `json:"feature_id"`
	Status    string         `json:"status,omitempty"`
	Kind      string         `json:"kind,omitempty"`
	From      string         `json:"from,omitempty"`
	To        string         `json:"to,omitempty"`
	Detail    map[string]any `json:"detail,omitempty"`
}

func (w *jsonFeatureWatch) emitInitial(feature serverruntime.FeatureDetailDTO) (bool, error) {
	events := normalizedFeatureEvents(feature)
	if err := w.write(events); err != nil {
		return false, err
	}
	w.updateState(feature, events)
	return shouldStopJSONWatch(feature), nil
}

func (w *jsonFeatureWatch) watchEvents(ctx context.Context, subscriber interface {
	SubscribeEvents(context.Context, serverruntime.EventSubscriptionOptions) (<-chan serverruntime.RefreshSignal, <-chan error)
}) error {
	signals, errs := subscriber.SubscribeEvents(ctx, serverruntime.EventSubscriptionOptions{
		HeartbeatInterval: jsonWatchHeartbeatInterval,
		ReconnectDelay:    jsonWatchReconnectDelay,
		MaxReconnects:     jsonWatchMaxReconnects,
	})
	signalsClosed := false
	errsClosed := false
	for {
		select {
		case signal, ok := <-signals:
			if !ok {
				signals = nil
				signalsClosed = true
				if err := readWatchStreamError(errs); err != nil {
					return err
				}
				if errsClosed {
					return errors.New("watch event stream closed")
				}
				continue
			}
			if !signalMatchesJSONFeature(signal, w.featureID) {
				continue
			}
			stop, err := w.refreshForSignal(ctx, signal)
			if err != nil {
				return err
			}
			if stop {
				return nil
			}
		case err, ok := <-errs:
			if ok && err != nil {
				return err
			}
			if !ok {
				errs = nil
				errsClosed = true
				if signalsClosed {
					return errors.New("watch event stream closed")
				}
				continue
			}
		case <-ctx.Done():
			return nil
		}
	}
}

func (w *jsonFeatureWatch) refreshForSignal(ctx context.Context, signal serverruntime.RefreshSignal) (bool, error) {
	if w.snapshotter == nil {
		return w.refresh(ctx)
	}
	snapshot, err := w.snapshotter.FetchRefreshSnapshot(ctx, signal)
	if err != nil {
		return false, err
	}
	stop, handled, err := w.emitRefreshSnapshot(snapshot)
	if err != nil || stop {
		return stop, err
	}
	if handled {
		return false, nil
	}
	return w.refresh(ctx)
}

func (w *jsonFeatureWatch) poll(ctx context.Context) error {
	for {
		stop, err := w.refresh(ctx)
		if err != nil {
			return err
		}
		if stop {
			return nil
		}
		timer := time.NewTimer(jsonWatchPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func (w *jsonFeatureWatch) refresh(ctx context.Context) (bool, error) {
	resp, err := w.detailer.FeatureDetail(ctx, w.featureID)
	if err != nil {
		return false, err
	}
	events := normalizedFeatureTransitionEvents(w.previous, resp.Feature, w.havePrevious, w.terminalEmitted)
	if err := w.write(events); err != nil {
		return false, err
	}
	w.updateState(resp.Feature, events)
	return shouldStopJSONWatch(resp.Feature), nil
}

func (w *jsonFeatureWatch) emitRefreshSnapshot(snapshot serverruntime.RefreshSnapshot) (bool, bool, error) {
	var events []normalizedFeatureEvent
	handled := false
	var feature *serverruntime.FeatureDetailDTO
	if snapshot.Feature != nil && snapshot.Feature.Feature.ID == w.featureID {
		handled = true
		feature = &snapshot.Feature.Feature
		events = append(events, normalizedFeatureTransitionEvents(w.previous, snapshot.Feature.Feature, w.havePrevious, w.terminalEmitted)...)
	}
	if attention := w.normalizedSnapshotAttentionEvents(snapshot); len(attention) > 0 {
		handled = true
		events = append(events, attention...)
	}
	if err := w.write(events); err != nil {
		return false, handled, err
	}
	if feature != nil {
		w.updateState(*feature, events)
	} else {
		w.updateEmittedEvents(events)
	}
	if feature != nil && shouldStopJSONWatch(*feature) {
		return true, handled, nil
	}
	return shouldStopAfterWatchEvents(events), handled, nil
}

func (w *jsonFeatureWatch) write(events []normalizedFeatureEvent) error {
	for _, evt := range events {
		if err := w.enc.Encode(evt); err != nil {
			return fmt.Errorf("write watch event: %w", err)
		}
	}
	return nil
}

func (w *jsonFeatureWatch) updateState(feature serverruntime.FeatureDetailDTO, events []normalizedFeatureEvent) {
	w.updateEmittedEvents(events)
	w.previous = feature
	w.havePrevious = true
}

func (w *jsonFeatureWatch) updateEmittedEvents(events []normalizedFeatureEvent) {
	for _, evt := range events {
		if evt.Type == "terminal" {
			w.terminalEmitted = true
			break
		}
	}
}

func normalizedFeatureEvents(feature serverruntime.FeatureDetailDTO) []normalizedFeatureEvent {
	events := []normalizedFeatureEvent{{
		Type:      "snapshot",
		FeatureID: feature.ID,
		Status:    feature.Status,
	}}
	if feature.NeedUserInput != nil && feature.NeedUserInput.Open {
		events = append(events, normalizedFeatureEvent{
			Type:      "attention_required",
			FeatureID: feature.ID,
			Kind:      "need_user_input",
			Detail: map[string]any{
				"scope":     feature.NeedUserInput.Scope,
				"iteration": feature.NeedUserInput.Iteration,
			},
		})
	}
	if feature.ReviewGate.ReviewingGate || feature.ReviewGate.ValidatingPlan {
		events = append(events, normalizedFeatureEvent{
			Type:      "attention_required",
			FeatureID: feature.ID,
			Kind:      "review_gate",
		})
	}
	if isJSONTerminalStatus(feature.Status) {
		events = append(events, normalizedFeatureEvent{
			Type:      "terminal",
			FeatureID: feature.ID,
			Status:    feature.Status,
		})
	}
	return events
}

func normalizedFeatureTransitionEvents(previous, current serverruntime.FeatureDetailDTO, havePrevious, terminalEmitted bool) []normalizedFeatureEvent {
	if !havePrevious {
		return normalizedFeatureEvents(current)
	}
	var events []normalizedFeatureEvent
	if previous.Status != current.Status {
		events = append(events, normalizedFeatureEvent{
			Type:      "state_changed",
			FeatureID: current.ID,
			From:      previous.Status,
			To:        current.Status,
		})
	}
	events = append(events, normalizedFeatureAttentionEvents(current)...)
	if isJSONTerminalStatus(current.Status) && !terminalEmitted {
		events = append(events, normalizedFeatureEvent{
			Type:      "terminal",
			FeatureID: current.ID,
			Status:    current.Status,
		})
	}
	return events
}

func normalizedFeatureAttentionEvents(feature serverruntime.FeatureDetailDTO) []normalizedFeatureEvent {
	var events []normalizedFeatureEvent
	if feature.NeedUserInput != nil && feature.NeedUserInput.Open {
		events = append(events, normalizedFeatureEvent{
			Type:      "attention_required",
			FeatureID: feature.ID,
			Kind:      "need_user_input",
			Detail: map[string]any{
				"scope":     feature.NeedUserInput.Scope,
				"iteration": feature.NeedUserInput.Iteration,
			},
		})
	}
	if feature.ReviewGate.ReviewingGate || feature.ReviewGate.ValidatingPlan {
		events = append(events, normalizedFeatureEvent{
			Type:      "attention_required",
			FeatureID: feature.ID,
			Kind:      "review_gate",
		})
	}
	return events
}

func (w *jsonFeatureWatch) normalizedSnapshotAttentionEvents(snapshot serverruntime.RefreshSnapshot) []normalizedFeatureEvent {
	var events []normalizedFeatureEvent
	if snapshot.Prompts != nil {
		for _, gate := range snapshot.Prompts.NeedUserInputs {
			if gate.FeatureID != w.featureID || !gate.Open {
				continue
			}
			events = append(events, normalizedFeatureEvent{
				Type:      "attention_required",
				FeatureID: w.featureID,
				Kind:      "need_user_input",
				Detail: map[string]any{
					"scope":     gate.Scope,
					"iteration": gate.Iteration,
				},
			})
		}
		for _, req := range snapshot.Prompts.AskUserQuestions {
			if !controlRequestMatchesFeature(req, w.featureID) || !controlRequestPending(req) {
				continue
			}
			events = append(events, normalizedFeatureEvent{
				Type:      "attention_required",
				FeatureID: w.featureID,
				Kind:      "ask_user",
				Detail:    controlRequestEventDetail(req),
			})
		}
	}
	if snapshot.Permissions != nil {
		for _, req := range snapshot.Permissions.Requests {
			if !controlRequestMatchesFeature(req, w.featureID) || !controlRequestPending(req) {
				continue
			}
			events = append(events, normalizedFeatureEvent{
				Type:      "attention_required",
				FeatureID: w.featureID,
				Kind:      "permission",
				Detail:    controlRequestEventDetail(req),
			})
		}
	}
	return events
}

func controlRequestMatchesFeature(req serverruntime.ControlRequestDTO, featureID string) bool {
	return req.FeatureID == featureID
}

func controlRequestPending(req serverruntime.ControlRequestDTO) bool {
	status := strings.TrimSpace(req.Status)
	return status == "" || strings.EqualFold(status, "pending")
}

func controlRequestEventDetail(req serverruntime.ControlRequestDTO) map[string]any {
	detail := map[string]any{
		"request_id": req.RequestID,
	}
	if req.SessionID != "" {
		detail["session_id"] = req.SessionID
	}
	if req.Phase != "" {
		detail["phase"] = req.Phase
	}
	if req.ToolName != "" {
		detail["tool_name"] = req.ToolName
	}
	if req.Status != "" {
		detail["status"] = req.Status
	}
	if req.Summary != "" {
		detail["summary"] = req.Summary
	}
	if len(req.Input) > 0 {
		detail["input"] = req.Input
	}
	if len(req.Questions) > 0 {
		detail["questions"] = req.Questions
	}
	return detail
}

func shouldStopJSONWatch(feature serverruntime.FeatureDetailDTO) bool {
	return isJSONTerminalStatus(feature.Status) || isJSONParkedStatus(feature.Status) || len(normalizedFeatureAttentionEvents(feature)) > 0
}

func shouldStopAfterWatchEvents(events []normalizedFeatureEvent) bool {
	for _, evt := range events {
		if evt.Type == "attention_required" || evt.Type == "terminal" {
			return true
		}
	}
	return false
}

func signalMatchesJSONFeature(signal serverruntime.RefreshSignal, featureID string) bool {
	resource := signal.Resource
	if resource.Type == "" {
		resource = signal.Event.Resource
	}
	if resource.FeatureID != "" {
		return resource.FeatureID == featureID
	}
	return signal.SnapshotRequired && (signal.Event.Kind == "connected" || signal.Event.Kind == "heartbeat" || signal.Event.Kind == "backpressure.coalesced")
}

func readWatchStreamError(errs <-chan error) error {
	select {
	case err, ok := <-errs:
		if ok {
			return err
		}
	default:
	}
	return nil
}

func isJSONTerminalStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "published", "done", "failed":
		return true
	default:
		return false
	}
}

func isJSONParkedStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "codeready", "prready", "interrupted", "stopped":
		return true
	default:
		return false
	}
}

func normalizeJSONAction(in string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(in)), "_", "-")
}

func decodeJSONCommandInput(parsed parsedJSONCommand, out any) error {
	body := strings.TrimSpace(parsed.inputJSON)
	if body == "" && parsed.inputFile != "" {
		data, err := os.ReadFile(parsed.inputFile)
		if err != nil {
			return fmt.Errorf("read input file: %w", err)
		}
		body = strings.TrimSpace(string(data))
	}
	if body == "" {
		body = "{}"
	}
	if err := json.Unmarshal([]byte(body), out); err != nil {
		return fmt.Errorf("decode input JSON: %w", err)
	}
	return nil
}

func writeCLIJSONResponse(w io.Writer, result any, err error) int {
	if err != nil {
		return writeCLIJSONError(w, classifyCLIJSONError(err), err.Error(), cliJSONErrorTarget(err))
	}
	return writeCLIJSONResult(w, result)
}

func writeCLIJSONResult(w io.Writer, result any) int {
	return writeCLIJSON(w, cliJSONEnvelope{OK: true, Result: result})
}

func writeCLIJSONError(w io.Writer, code, message string, target map[string]any) int {
	if code == "" {
		code = "internal_error"
	}
	if strings.TrimSpace(message) == "" {
		message = code
	}
	_ = writeCLIJSON(w, cliJSONEnvelope{OK: false, Error: &cliJSONError{Code: code, Message: message, Target: target}})
	return 1
}

func writeCLIJSON(w io.Writer, envelope cliJSONEnvelope) int {
	envelope.SchemaVersion = cliJSONSchemaVersion
	envelope.APIVersion = serverruntime.APIVersion
	enc := json.NewEncoder(w)
	if err := enc.Encode(envelope); err != nil {
		return 1
	}
	if envelope.OK {
		return 0
	}
	return 1
}

func classifyCLIJSONError(err error) string {
	if err == nil {
		return ""
	}
	var apiErr *serverruntime.APIError
	if errors.As(err, &apiErr) {
		if apiErr.Code != "" {
			return apiErr.Code
		}
		if apiErr.Status == http.StatusConflict {
			return "feature_state_conflict"
		}
		return "rest_api_error"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "startup_failure"
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "discovery"):
		return "discovery_failure"
	case strings.Contains(msg, "server boot") || strings.Contains(msg, "readiness"):
		return "startup_failure"
	case strings.Contains(msg, "send request") || strings.Contains(msg, "connection refused"):
		return "rest_transport_failure"
	default:
		return "internal_error"
	}
}

func cliJSONErrorTarget(err error) map[string]any {
	var apiErr *serverruntime.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Target
	}
	return nil
}

func defaultJSONCommandOptions(args []string, stdout, stderr io.Writer, opts launchOptions) jsonCommandOptions {
	return jsonCommandOptions{
		Args:                       args,
		ConfigPath:                 opts.configPath,
		StateDir:                   opts.stateDir,
		DangerouslySkipPermissions: opts.dangerouslySkipPerms,
		EnabledProviders:           opts.enabledProviders,
		RefreshModels:              opts.refreshModels,
		Stdout:                     stdout,
		Stderr:                     stderr,
	}
}

func jsonCommandContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}
