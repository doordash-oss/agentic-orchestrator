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
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const desktopLaunchTimeout = 10 * time.Second

// openRegisteredDesktop asks the operating system to focus or launch the
// installed Agentico application. Electron's single-instance lock handles an
// already-running instance.
func openRegisteredDesktop() error {
	ctx, cancel := context.WithTimeout(context.Background(), desktopLaunchTimeout)
	defer cancel()

	name, args, err := desktopLaunchCommand(runtime.GOOS)
	if err != nil {
		return err
	}
	return runDesktopLaunchCommand(ctx, name, args)
}

func runDesktopLaunchCommand(ctx context.Context, name string, args []string) error {
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err == nil {
		return nil
	}
	detail := strings.TrimSpace(string(output))
	if detail == "" {
		return fmt.Errorf("launching desktop application: %w", err)
	}
	return fmt.Errorf("launching desktop application: %w: %s", err, detail)
}

func desktopLaunchCommand(goos string) (string, []string, error) {
	switch goos {
	case "darwin":
		return "open", []string{"-b", "com.doordash.agentico"}, nil
	case "linux":
		return "xdg-open", []string{"agentico://"}, nil
	default:
		return "", nil, fmt.Errorf("desktop launch unsupported on %s", goos)
	}
}
