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
	"time"
)

type RuntimeServer struct {
	baseURL string
	srv     *http.Server
	done    chan error
}

func Start(ctx context.Context, opts Options) (*RuntimeServer, error) {
	startedAt := time.Now().UTC()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen on loopback: %w", err)
	}
	baseURL := "http://" + ln.Addr().String()
	httpServer := &http.Server{
		Handler:      NewHandler(HandlerOptions{Runtime: opts.Runtime, StartedAt: startedAt, Features: opts.Features}),
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  30 * time.Second,
	}
	s := &RuntimeServer{
		baseURL: baseURL,
		srv:     httpServer,
		done:    make(chan error, 1),
	}
	go func() {
		err := httpServer.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		s.done <- err
	}()
	if err := waitForHealth(ctx, baseURL); err != nil {
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

func (s *RuntimeServer) Close(ctx context.Context) error {
	if s == nil || s.srv == nil {
		return nil
	}
	srv := s.srv
	s.srv = nil
	shutdownErr := srv.Shutdown(ctx)
	var serveErr error
	select {
	case serveErr = <-s.done:
	case <-ctx.Done():
		serveErr = ctx.Err()
	}
	return errors.Join(shutdownErr, serveErr)
}

func waitForHealth(ctx context.Context, baseURL string) error {
	client := &http.Client{Timeout: 250 * time.Millisecond}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if ctx.Err() != nil {
			return fmt.Errorf("wait for health: %w", ctx.Err())
		}
		if discoveryHealthOK(ctx, client, baseURL) {
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
