BINARY  := agentico
BIN_DIR := ./bin
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
# Injected explicitly because the Go toolchain does not stamp vcs.revision
# when building from a linked git worktree.
REVISION ?= $(shell git rev-parse HEAD 2>/dev/null || echo "")
LDFLAGS := -s -w -X github.com/doordash-oss/agentic-orchestrator/internal/tui.version=$(VERSION) -X github.com/doordash-oss/agentic-orchestrator/internal/tui.revision=$(REVISION)

.PHONY: build install install-system uninstall clean lint generate-openapi test-fast jaeger jaeger-stop jaeger-status

build:
	rm -f $(BIN_DIR)/$(BINARY)
	go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY) ./cmd/agentico

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/agentico
	@GOBIN=$$(go env GOBIN); \
	[ -z "$$GOBIN" ] && GOBIN=$$(go env GOPATH)/bin; \
	case ":$$PATH:" in \
	  *":$$GOBIN:"*) ;; \
	  *) echo ""; \
	     echo "WARNING: $$GOBIN is not in your PATH."; \
	     echo "Add it to your shell profile:"; \
	     echo ""; \
	     echo "  export PATH=\"$$GOBIN:\$$PATH\""; \
	     echo "" ;; \
	esac

install-system: build
	cp $(BIN_DIR)/$(BINARY) $(INSTALL_DIR)/$(BINARY)
	codesign -s - $(INSTALL_DIR)/$(BINARY)

uninstall:
	rm -f $(INSTALL_DIR)/$(BINARY)

clean:
	rm -rf $(BIN_DIR)/$(BINARY)

lint:
	go vet ./...

generate-openapi:
	go generate ./internal/server

test-fast:
	@start=$$(date +%s); \
	all_pkgs=$$(go list ./...) || exit $$?; \
	ui_test_pkgs=$$(printf '%s\n' "$$all_pkgs" | grep -E '/internal/tui($$|/)|/test/(e2e|eval|integration|testutil)($$|/)'); \
	core_pkgs=$$(printf '%s\n' "$$all_pkgs" | grep -Ev '/internal/tui($$|/)|/test/(e2e|eval|integration|testutil)($$|/)'); \
	go test $$core_pkgs -short -count=1 -parallel 32 & \
	core_pid=$$!; \
	go test $$ui_test_pkgs -short -count=1 -parallel 32 & \
	ui_pid=$$!; \
	wait $$core_pid; \
	core_status=$$?; \
	wait $$ui_pid; \
	ui_status=$$?; \
	end=$$(date +%s); \
	elapsed=$$((end - start)); \
	echo "Fast suite wall time: $${elapsed}s"; \
	exit_status=$$core_status; \
	if [ $$exit_status -eq 0 ]; then exit_status=$$ui_status; fi; \
	exit $$exit_status

# ---------- Jaeger (local OTel collector + trace UI) ----------
JAEGER_CONTAINER := agentic-jaeger
JAEGER_IMAGE     := jaegertracing/all-in-one:latest
JAEGER_UI_PORT   := 16686
JAEGER_OTLP_PORT := 4317

jaeger: ## Start Jaeger (OTLP gRPC on :4317, UI on :16686)
	@if command -v docker >/dev/null 2>&1; then \
		RUNTIME=docker; \
	elif command -v podman >/dev/null 2>&1; then \
		RUNTIME=podman; \
	else \
		echo "Error: neither docker nor podman found in PATH" >&2; exit 1; \
	fi; \
	if $$RUNTIME ps -a --format '{{.Names}}' 2>/dev/null | grep -q '^$(JAEGER_CONTAINER)$$' || \
	   $$RUNTIME ps -a --format '{{.Names}}' 2>/dev/null | grep -q '$(JAEGER_CONTAINER)'; then \
		STATE=$$($$RUNTIME inspect -f '{{.State.Running}}' $(JAEGER_CONTAINER) 2>/dev/null); \
		if [ "$$STATE" = "true" ]; then \
			echo "$(JAEGER_CONTAINER) is already running"; \
			echo "  UI:   http://localhost:$(JAEGER_UI_PORT)"; \
			echo "  OTLP: localhost:$(JAEGER_OTLP_PORT)"; \
			exit 0; \
		fi; \
		echo "Starting existing $(JAEGER_CONTAINER) container..."; \
		$$RUNTIME start $(JAEGER_CONTAINER); \
	else \
		echo "Creating $(JAEGER_CONTAINER) container..."; \
		$$RUNTIME run -d --name $(JAEGER_CONTAINER) \
			-p $(JAEGER_UI_PORT):16686 \
			-p $(JAEGER_OTLP_PORT):4317 \
			$(JAEGER_IMAGE); \
	fi; \
	echo ""; \
	echo "Jaeger is running:"; \
	echo "  UI:   http://localhost:$(JAEGER_UI_PORT)"; \
	echo "  OTLP: localhost:$(JAEGER_OTLP_PORT)"; \
	echo ""; \
	echo "Configure agentic:"; \
	echo "  export OTEL_EXPORTER_OTLP_ENDPOINT=localhost:$(JAEGER_OTLP_PORT)"; \
	echo "  Set observability.otel_enabled: true in config.yaml"

jaeger-stop: ## Stop Jaeger container
	@if command -v docker >/dev/null 2>&1; then \
		RUNTIME=docker; \
	elif command -v podman >/dev/null 2>&1; then \
		RUNTIME=podman; \
	else \
		echo "Error: neither docker nor podman found in PATH" >&2; exit 1; \
	fi; \
	$$RUNTIME stop $(JAEGER_CONTAINER) 2>/dev/null && echo "Stopped $(JAEGER_CONTAINER)" || echo "$(JAEGER_CONTAINER) is not running"

jaeger-status: ## Show Jaeger container status
	@if command -v docker >/dev/null 2>&1; then \
		RUNTIME=docker; \
	elif command -v podman >/dev/null 2>&1; then \
		RUNTIME=podman; \
	else \
		echo "Error: neither docker nor podman found in PATH" >&2; exit 1; \
	fi; \
	$$RUNTIME ps -a --filter name=$(JAEGER_CONTAINER) --format 'table {{.Names}}\t{{.Status}}\t{{.Ports}}' 2>/dev/null || \
	$$RUNTIME ps -a --filter name=$(JAEGER_CONTAINER)
