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

// Command opencode-min-bin fetches the OpenCode CLI release that matches the
// OpenCode provider's enforced MinVersion() into the repo cache, so the live
// child-session spike can run against the pinned minimum without touching the
// CLI installed on PATH. The binary comes from OpenCode's tagged GitHub
// release for the host platform and lands at
// .cache/opencode/<version>/opencode.
package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm/clirun"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/opencode"
)

const releaseBase = "https://github.com/anomalyco/opencode/releases/download"

func main() {
	cacheDir := flag.String("cache", ".cache/opencode", "cache root; the binary lands at <cache>/<version>/opencode")
	flag.Parse()
	if err := run(*cacheDir); err != nil {
		fmt.Fprintln(os.Stderr, "opencode-min-bin:", err)
		os.Exit(1)
	}
}

func run(cacheRoot string) error {
	v := (&opencode.Provider{}).MinVersion()
	version := fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2])
	dest := filepath.Join(cacheRoot, version, "opencode")
	if got, err := binaryVersion(dest); err == nil && got == version {
		fmt.Printf("OpenCode %s already cached at %s\n", version, dest)
		return nil
	}
	asset, err := assetName(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/v%s/%s", releaseBase, version, asset)
	fmt.Printf("Fetching %s\n", url)
	archive, err := download(url)
	if err != nil {
		return err
	}
	bin, err := extractBinary(asset, archive)
	if err != nil {
		return fmt.Errorf("%s: %w", asset, err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, bin, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		return err
	}
	got, err := binaryVersion(dest)
	if err != nil {
		return fmt.Errorf("fetched binary does not run: %w", err)
	}
	if got != version {
		return fmt.Errorf("fetched binary reports %s, want %s", got, version)
	}
	fmt.Printf("OpenCode %s cached at %s\n", version, dest)
	return nil
}

func assetName(goos, goarch string) (string, error) {
	arch := map[string]string{"amd64": "x64", "arm64": "arm64"}[goarch]
	if arch == "" {
		return "", fmt.Errorf("no OpenCode release for architecture %s", goarch)
	}
	switch goos {
	case "darwin":
		return "opencode-darwin-" + arch + ".zip", nil
	case "linux":
		return "opencode-linux-" + arch + ".tar.gz", nil
	}
	return "", fmt.Errorf("no OpenCode release for OS %s", goos)
}

func download(url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

func extractBinary(asset string, data []byte) ([]byte, error) {
	if strings.HasSuffix(asset, ".zip") {
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if path.Base(f.Name) != "opencode" || f.FileInfo().IsDir() {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(rc)
		}
		return nil, errors.New("archive has no opencode binary")
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("archive has no opencode binary")
		}
		if err != nil {
			return nil, err
		}
		if hdr.Typeflag == tar.TypeReg && path.Base(hdr.Name) == "opencode" {
			return io.ReadAll(tr)
		}
	}
}

func binaryVersion(bin string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil {
		return "", err
	}
	return clirun.ParseVersionOutput(out)
}
