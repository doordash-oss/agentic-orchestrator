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
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

type RuntimeServer struct {
	baseURL   string
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
	listenAddr, err := ResolveListenAddr(opts.ListenAddr)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", listenAddr, err)
	}
	baseURL := "http://" + ln.Addr().String()
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
	})
	httpServer := &http.Server{
		Handler: handler.routes(),
		// ReadHeaderTimeout (not ReadTimeout) is intentional: ReadTimeout
		// would cap the whole connection lifetime, killing long-lived SSE
		// streams (/api/v1/events, /sessions/{id}/output/stream). Mutation
		// bodies are still bounded — decodeMutationJSON wraps r.Body in
		// http.MaxBytesReader(MaxMutationBodyBytes) — so an
		// unbounded-body-read DoS isn't reintroduced by this tradeoff.
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       30 * time.Second,
	}
	s := &RuntimeServer{
		baseURL:   baseURL,
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
	if err := waitForHealth(ctx, baseURL, opts.AuthToken); err != nil {
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
