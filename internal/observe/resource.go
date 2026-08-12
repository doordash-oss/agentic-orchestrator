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

package observe

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/buildinfo"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

const telemetrySchemaVersion = 1

func telemetryResource(stateDir, serviceName string) (*resource.Resource, string) {
	if serviceName == "" {
		serviceName = "agentico"
	}
	installationID, _ := loadInstallationID(filepath.Join(filepath.Dir(stateDir), "telemetry"))
	instanceID := uuid.NewString()
	attrs := filteredEnvironmentResourceAttributes()
	attrs = append(attrs,
		semconv.ServiceNameKey.String(serviceName),
		semconv.ServiceVersionKey.String(buildinfo.Version()),
		semconv.ServiceInstanceIDKey.String(instanceID),
		attribute.String("agentico.build.revision", buildinfo.Revision()),
		attribute.String("agentico.installation.id", installationID),
		attribute.Int("agentico.telemetry.schema.version", telemetrySchemaVersion),
	)
	res := resource.NewSchemaless(attrs...)
	return res, installationID
}

func filteredEnvironmentResourceAttributes() []attribute.KeyValue {
	res, err := resource.New(context.Background(), resource.WithFromEnv(), resource.WithTelemetrySDK())
	if err != nil {
		return nil
	}
	attrs := make([]attribute.KeyValue, 0, res.Len())
	iter := res.Iter()
	for iter.Next() {
		kv := iter.Attribute()
		key := strings.ToLower(string(kv.Key))
		value := kv.Value.Emit()
		if forbiddenResourceAttribute(key, value) {
			continue
		}
		attrs = append(attrs, kv)
	}
	return attrs
}

func forbiddenResourceAttribute(key, value string) bool {
	for _, token := range []string{"user", "employee", "team", "host", "machine", "repository", "repo", "workspace", "config", "path", "directory"} {
		if strings.Contains(key, token) {
			return true
		}
	}
	return absolutePathPattern.MatchString(value)
}

func loadInstallationID(telemetryDir string) (string, error) {
	if err := os.MkdirAll(telemetryDir, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(telemetryDir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(telemetryDir, "installation-id")
	replaceInvalid := false
	if data, err := os.ReadFile(path); err == nil {
		id := strings.TrimSpace(string(data))
		if _, parseErr := uuid.Parse(id); parseErr == nil {
			_ = os.Chmod(path, 0o600)
			return id, nil
		}
		replaceInvalid = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	id := uuid.NewString()
	tmp, err := os.CreateTemp(telemetryDir, ".installation-id-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err == nil {
		_, err = tmp.WriteString(id + "\n")
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		if replaceInvalid {
			err = os.Rename(tmpName, path)
		} else {
			err = os.Link(tmpName, path)
		}
		if errors.Is(err, os.ErrExist) {
			if data, readErr := os.ReadFile(path); readErr == nil {
				candidate := strings.TrimSpace(string(data))
				if _, parseErr := uuid.Parse(candidate); parseErr == nil {
					id, err = candidate, nil
				}
			}
		}
	}
	return id, err
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return strings.Repeat("0", n*2)
	}
	return hex.EncodeToString(b)
}
