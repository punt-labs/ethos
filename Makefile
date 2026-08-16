VERSION := $(or $(shell git describe --tags --always 2>/dev/null | sed 's/^v//'),dev)
LDFLAGS := -X main.version=$(VERSION)

PLUGIN_CACHE := $(HOME)/.claude/plugins/cache/punt-labs/ethos
PLUGIN_VERSION := $(shell ls -1 $(PLUGIN_CACHE) 2>/dev/null | grep -v '\.bak$$' | sort -V | tail -1)

# golangci-lint is the Go lint gate (Go Report Card successor). Pinned so
# local and CI run the same analyzer versions; keep in sync with
# .github/workflows/test.yml. Config lives in .golangci.yml.
# Resolve the install dir the way `go install` does: GOBIN if set, else
# GOPATH/bin — so `make tools` and this path agree for anyone with GOBIN set.
GOLANGCI_LINT_VERSION := v2.12.2
GOBIN := $(shell go env GOBIN)
ifeq ($(GOBIN),)
GOBIN := $(shell go env GOPATH)/bin
endif
GOLANGCI_LINT := $(GOBIN)/golangci-lint

.PHONY: help lint docs test check validate-content format build install dev clean dist tools doctor undev test-behavioral test-e2e test-e2e-smoke e2e-bin baseline-tokens calibrate-tokens

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-12s %s\n", $$1, $$2}'

lint: ## Lint (golangci-lint + shellcheck + ruff + mypy)
	@test -x $(GOLANGCI_LINT) || { echo "golangci-lint not found at $(GOLANGCI_LINT) — run 'make tools' to install $(GOLANGCI_LINT_VERSION)"; exit 1; }
	$(GOLANGCI_LINT) run ./...
	shellcheck hooks/*.sh install.sh
	cd tests/e2e && uv run ruff check .
	cd tests/e2e && uv run ruff format --check .
	cd tests/e2e && uv run mypy src/ tests/
	cd tests/e2e && uv run pytest tests/ -m "not e2e"

docs: ## Lint markdown
	npx --yes markdownlint-cli2 "**/*.md" "#node_modules"

test: ## Run tests with race detection and write coverage to coverage.out
	go test -race -count=1 -coverprofile=coverage.out ./...

validate-content: ## Validate all ethos content files
	go run ./cmd/validate-content

test-behavioral: build ## Run L4 behavioral tests (requires ANTHROPIC_API_KEY and claude CLI)
	go test -tags behavioral -timeout 10m -v ./tests/behavioral/

e2e-bin: ## Build the ethos binary a non-hermetic E2E scenario's hooks run
	@mkdir -p .tmp/e2e-bin
	CGO_ENABLED=0 go build -o .tmp/e2e-bin/ethos ./cmd/ethos/

test-e2e-smoke: e2e-bin ## Run the fast E2E (L4) smoke scenario (every push; requires litellm + claude CLI)
	cd tests/e2e && uv run pytest -m "e2e and smoke"

test-e2e: e2e-bin ## Run the full E2E (L4) scenario sweep (per-release; requires litellm + claude CLI)
	cd tests/e2e && uv run pytest -m e2e

baseline-tokens: ## Recapture E2E token baselines (operator-invoked)
	@echo "not yet implemented in initial land"

calibrate-tokens: ## Calibrate the offline tokenizer against Anthropic's API (operator-invoked)
	@echo "not yet implemented in initial land"

check: lint docs test validate-content ## Run all quality gates

format: ## Format code (applies the formatters golangci-lint gates)
	$(GOLANGCI_LINT) fmt

build: ## Build binary
	CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -o ethos ./cmd/ethos/

install: build ## Build and install to ~/.local/bin
	mkdir -p $(HOME)/.local/bin
	rm -f $(HOME)/.local/bin/ethos
	cp ethos $(HOME)/.local/bin/ethos

dev: install ## Install and symlink plugin cache for development
	@if [ -z "$(PLUGIN_VERSION)" ]; then echo "error: no plugin cache found at $(PLUGIN_CACHE)"; exit 1; fi
	@if [ -L "$(PLUGIN_CACHE)/$(PLUGIN_VERSION)" ]; then echo "plugin cache already symlinked"; exit 0; fi
	mv $(PLUGIN_CACHE)/$(PLUGIN_VERSION) $(PLUGIN_CACHE)/$(PLUGIN_VERSION).bak
	ln -s $(CURDIR) $(PLUGIN_CACHE)/$(PLUGIN_VERSION)
	@echo "symlinked $(PLUGIN_CACHE)/$(PLUGIN_VERSION) → $(CURDIR)"
	@echo "original cached at $(PLUGIN_CACHE)/$(PLUGIN_VERSION).bak"

undev: ## Restore plugin cache from backup
	@if [ ! -L "$(PLUGIN_CACHE)/$(PLUGIN_VERSION)" ]; then echo "not in dev mode"; exit 0; fi
	rm $(PLUGIN_CACHE)/$(PLUGIN_VERSION)
	mv $(PLUGIN_CACHE)/$(PLUGIN_VERSION).bak $(PLUGIN_CACHE)/$(PLUGIN_VERSION)
	@echo "restored $(PLUGIN_CACHE)/$(PLUGIN_VERSION)"

clean: ## Remove build artifacts
	rm -f ethos coverage.out
	rm -rf dist/

dist: clean ## Cross-compile for all platforms
	mkdir -p dist
	CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -ldflags="-s -w $(LDFLAGS)" -o dist/ethos-darwin-arm64 ./cmd/ethos/
	CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64 go build -ldflags="-s -w $(LDFLAGS)" -o dist/ethos-darwin-amd64 ./cmd/ethos/
	CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -ldflags="-s -w $(LDFLAGS)" -o dist/ethos-linux-arm64  ./cmd/ethos/
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -ldflags="-s -w $(LDFLAGS)" -o dist/ethos-linux-amd64  ./cmd/ethos/

tools: ## Install development tools
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

doctor: build ## Run ethos doctor
	./ethos doctor
