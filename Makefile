VERSION := $(or $(shell git describe --tags --always 2>/dev/null | sed 's/^v//'),dev)
LDFLAGS := -X main.version=$(VERSION)

PLUGIN_CACHE := $(HOME)/.claude/plugins/cache/punt-labs/ethos
PLUGIN_VERSION := $(shell ls -1 $(PLUGIN_CACHE) 2>/dev/null | grep -v '\.bak$$' | sort -V | tail -1)

# golangci-lint is the Go lint gate (Go Report Card successor). Pinned so
# local and CI run the same analyzer versions; keep in sync with
# .github/workflows/test.yml. Config lives in .golangci.yml.
# Resolve the install dir the way `go install` does: GOBIN if set, else
# GOPATH/bin — so `make tools` and this path agree for anyone with GOBIN set.
GOLANGCI_LINT_VERSION := v2.13.1
GOBIN := $(shell go env GOBIN)
ifeq ($(GOBIN),)
GOBIN := $(shell go env GOPATH)/bin
endif
GOLANGCI_LINT := $(GOBIN)/golangci-lint

.PHONY: help lint docs docs-pdf test check validate-content sync-embed format build install dev clean clean-latex dist tools doctor undev test-behavioral test-e2e test-e2e-smoke e2e-bin baseline-tokens calibrate-tokens

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-12s %s\n", $$1, $$2}'

lint: ## Lint (golangci-lint + shellcheck + ruff + mypy)
	@test -x $(GOLANGCI_LINT) || { echo "golangci-lint not found at $(GOLANGCI_LINT) — run 'make tools' to install $(GOLANGCI_LINT_VERSION)"; exit 1; }
	$(GOLANGCI_LINT) run ./...
	shellcheck plugin/hooks/*.sh install.sh
	cd tests/e2e && uv run ruff check .
	cd tests/e2e && uv run ruff format --check .
	cd tests/e2e && uv run mypy src/ tests/
	cd tests/e2e && uv run pytest tests/ -m "not e2e"

docs: ## Lint markdown
	npx --yes markdownlint-cli2 "**/*.md" "#node_modules"

# LaTeX PDFs are checked in (prfaq.pdf, docs/*.pdf). Regenerating them
# is opt-in — `make docs-pdf` — not part of `make check`, so CI doesn't
# need a TeX toolchain. latexmk handles biber/rerun-until-stable and
# `-c` sweeps the aux/log/bbl/bcf/blg/out/run.xml/toc intermediates the
# build produces, so `.tex` and `.pdf` are the only artifacts left on
# disk after a build.
LATEX_TEX := prfaq.tex $(wildcard docs/*.tex)

docs-pdf: ## Rebuild prfaq.pdf + docs/*.pdf from .tex sources, then sweep intermediates
	@command -v latexmk >/dev/null || { echo "latexmk not found — install a TeX distribution (MacTeX, TeX Live)"; exit 1; }
	@for f in $(LATEX_TEX); do \
		echo "==> latexmk $$f"; \
		latexmk -pdf -bibtex -interaction=nonstopmode -halt-on-error -cd "$$f" || exit 1; \
		latexmk -c -cd "$$f" >/dev/null; \
	done

clean-latex: ## Remove LaTeX build intermediates (keeps .pdf/.tex/.bib)
	@find . -maxdepth 3 \( -name '*.aux' -o -name '*.bbl' -o -name '*.bcf' -o -name '*.blg' \
		-o -name '*.fdb_latexmk' -o -name '*.fls' -o -name '*.log' -o -name '*.out' \
		-o -name '*.run.xml' -o -name '*.synctex.gz' -o -name '*.toc' \) \
		-not -path './.git/*' -not -path './.tmp/*' -not -path './node_modules/*' \
		-not -path './.claude/worktrees/*' -delete

test: ## Run tests with race detection and write coverage to coverage.out
	go test -race -count=1 -coverprofile=coverage.out ./...

validate-content: ## Validate all ethos content files
	go run ./cmd/validate-content

sync-embed: ## Copy docs/ETHOS-SETUP.md to its DES-071 tier-C embed source
	cp docs/ETHOS-SETUP.md internal/enable/setup/ETHOS-SETUP.md

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

# The symlink target is $(CURDIR)/plugin, not $(CURDIR): since DES-072 the
# shippable plugin surface lives in plugin/, and CLAUDE_PLUGIN_ROOT must be
# the same directory the git-subdir marketplace source checks out. Pointing
# it at the repo root would resolve ${CLAUDE_PLUGIN_ROOT}/hooks/*.sh to a
# path that no longer exists.
#
# Both recipes are ONE shell each. There is no .ONESHELL here, so a guard on
# its own line ends only that line's shell and make runs the next line
# regardless — the previous `@if ...; exit 0; fi` guards printed their message
# and then fell straight through to the work they were meant to skip. A second
# `make dev` moved the symlink *into* the existing .bak directory (leaving a
# stray .bak/$(PLUGIN_VERSION) link behind; the real backup survived) and
# `make undev` outside dev mode ran `rm` against the real cache directory,
# failing the target instead of no-opping.
#
# dev handles three states, because after the move a stale link is actively
# broken rather than merely redundant: a link left over from before DES-072
# points at the repo root, where there is no hooks/, so every
# ${CLAUDE_PLUGIN_ROOT}/hooks/*.sh silently fails. Only a genuine directory
# is ever backed up; retargeting a link leaves the existing .bak alone.
dev: install ## Install and symlink plugin cache for development
	@set -e; \
	if [ -z "$(PLUGIN_VERSION)" ]; then \
	  echo "error: no plugin cache found at $(PLUGIN_CACHE)" >&2; exit 1; \
	fi; \
	link="$(PLUGIN_CACHE)/$(PLUGIN_VERSION)"; want="$(CURDIR)/plugin"; \
	if [ -L "$$link" ]; then \
	  have=$$(readlink "$$link"); \
	  if [ "$$have" = "$$want" ]; then \
	    echo "plugin cache already symlinked → $$want"; exit 0; \
	  fi; \
	  echo "retargeting stale symlink ($$have → $$want)"; \
	  rm "$$link"; \
	else \
	  [ -e "$$link.bak" ] && { echo "error: $$link.bak already exists; refusing to overwrite" >&2; exit 1; }; \
	  mv "$$link" "$$link.bak"; \
	  echo "original cached at $$link.bak"; \
	fi; \
	ln -s "$$want" "$$link"; \
	echo "symlinked $$link → $$want"

undev: ## Restore plugin cache from backup
	@set -e; \
	if [ -z "$(PLUGIN_VERSION)" ]; then \
	  echo "error: no plugin cache found at $(PLUGIN_CACHE)" >&2; exit 1; \
	fi; \
	link="$(PLUGIN_CACHE)/$(PLUGIN_VERSION)"; \
	if [ ! -L "$$link" ]; then echo "not in dev mode"; exit 0; fi; \
	if [ ! -d "$$link.bak" ]; then \
	  echo "error: no backup at $$link.bak; leaving the symlink in place — reinstall with 'claude plugin install ethos@punt-labs' to repopulate the cache" >&2; \
	  exit 1; \
	fi; \
	rm "$$link"; \
	mv "$$link.bak" "$$link"; \
	echo "restored $$link"

clean: clean-latex ## Remove build artifacts
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
