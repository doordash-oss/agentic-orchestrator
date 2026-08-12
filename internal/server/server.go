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

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

type RuntimeServer struct {
	baseURL   string
	policy    string
	wildcard  bool
	startedAt time.Time
	srv       *http.Server
	broker    *eventBroker
	done      chan error
}

func Start(ctx context.Context, opts Options) (*RuntimeServer, error) {
	if strings.TrimSpace(opts.AuthToken) == "" && !opts.AllowUnauthenticated {
		return nil, errors.New("server auth token is required")
	}
	startedAt := time.Now().UTC()
	res, err := ResolveListen(opts.ListenAddr)
	if err != nil {
		return nil, err
	}
	policy := res.Policy
	if opts.RuntimePolicy != "" {
		policy = opts.RuntimePolicy
	}
	ln, err := net.Listen("tcp", res.BindAddr)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", res.BindAddr, err)
	}
	baseURL := "http://" + ln.Addr().String()
	if policy == CompatibilityNetworkRuntimePolicy {
		// Advertise the resolved host (the primary interface address for
		// wildcard binds), never the wildcard bind address itself.
		tcpAddr, ok := ln.Addr().(*net.TCPAddr)
		if !ok {
			_ = ln.Close()
			return nil, fmt.Errorf("listen on %s: unexpected address type %T", res.BindAddr, ln.Addr())
		}
		baseURL = "http://" + net.JoinHostPort(res.AdvertiseHost, strconv.Itoa(tcpAddr.Port))
	}
	handler := newAPIHandler(HandlerOptions{
		Runtime:                     opts.Runtime,
		LaunchPolicy:                opts.LaunchPolicy,
		StartedAt:                   startedAt,
		Owner:                       opts.Owner,
		AuthToken:                   opts.AuthToken,
		Name:                        opts.Name,
		Features:                    opts.Features,
		FeatureStore:                opts.FeatureStore,
		Freshness:                   opts.Freshness,
		Config:                      opts.Config,
		Registry:                    opts.Registry,
		Sessions:                    opts.Sessions,
		Events:                      opts.Events,
		DomainEvents:                opts.DomainEvents,
		Mutations:                   opts.Mutations,
		PersistProviderModelCatalog: opts.PersistProviderModelCatalog,
		InitGitRepository:           opts.InitGitRepository,
		Worktrees:                   opts.Worktrees,
		HTTPMetrics:                 opts.HTTPMetrics,
		RuntimePolicy:               policy,
	})
	httpServer := &http.Server{
		Handler: withHTTPMetrics(handler.routes(), opts.HTTPMetrics),
		// ReadHeaderTimeout (not ReadTimeout) is intentional: ReadTimeout
		// would cap the whole connection lifetime, killing long-lived SSE
		// streams (/api/v1/events, /sessions/{id}/output/stream). Mutation
		// bodies are still bounded — decodeMutationJSON wraps r.Body in
		// http.MaxBytesReader(MaxMutationBodyBytes) and the upload route caps
		// per kind via http.MaxBytesReader — so an unbounded-body-read DoS
		// isn't reintroduced by this tradeoff.
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       30 * time.Second,
	}
	if handler.uploads != nil {
		// Reap orphaned staged uploads: once at startup, then hourly until the
		// server lifetime context ends.
		go handler.uploads.sweepLoop(ctx)
	}
	s := &RuntimeServer{
		baseURL:   baseURL,
		policy:    policy,
		wildcard:  res.Wildcard,
		startedAt: startedAt,
		srv:       httpServer,
		broker:    handler.broker,
		done:      make(chan error, 1),
	}
	go func() {
		err := httpServer.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		s.done <- err
	}()
	healthURL := baseURL
	if res.Wildcard {
		// Wait via loopback: the wildcard bind answers there regardless of
		// which interface address is advertised.
		if tcpAddr, ok := ln.Addr().(*net.TCPAddr); ok {
			healthURL = "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(tcpAddr.Port))
		}
	}
	if err := waitForHealth(ctx, healthURL, opts.AuthToken); err != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.Close(shutdownCtx)
		return nil, err
	}
	return s, nil
}

func (s *RuntimeServer) BaseURL() string {
	if s == nil {
		return ""
	}
	return s.baseURL
}

// RuntimePolicy reports the declared runtime policy selected from the
// resolved listen address (loopback or network).
func (s *RuntimeServer) RuntimePolicy() string {
	if s == nil {
		return ""
	}
	return s.policy
}

// WildcardBind reports whether the server binds all local interfaces, in
// which case BaseURL advertises the resolved primary interface address.
func (s *RuntimeServer) WildcardBind() bool {
	if s == nil {
		return false
	}
	return s.wildcard
}

func (s *RuntimeServer) StartedAt() time.Time {
	if s == nil {
		return time.Time{}
	}
	return s.startedAt
}

func (s *RuntimeServer) EventEpoch() string {
	if s == nil || s.broker == nil {
		return ""
	}
	return s.broker.currentEpoch()
}

func (s *RuntimeServer) Close(ctx context.Context) error {
	if s == nil || s.srv == nil {
		return nil
	}
	srv := s.srv
	s.srv = nil
	if s.broker != nil {
		s.broker.publish(eventDTOFromDomain(ports.Event{Type: ports.RuntimeShutdownStarted}))
	}
	shutdownErr := srv.Shutdown(ctx)
	var serveErr error
	select {
	case serveErr = <-s.done:
	case <-ctx.Done():
		serveErr = ctx.Err()
	}
	return errors.Join(shutdownErr, serveErr)
}

func waitForHealth(ctx context.Context, baseURL, token string) error {
	client := &http.Client{Timeout: 250 * time.Millisecond}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if ctx.Err() != nil {
			return fmt.Errorf("wait for health: %w", ctx.Err())
		}
		if discoveryHealthOK(ctx, client, baseURL, token) {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("wait for health: timed out")
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("wait for health: %w", ctx.Err())
		case <-timer.C:
		}
	}
}
