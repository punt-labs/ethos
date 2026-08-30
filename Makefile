VERSION := $(or $(shell git describe --tags --always 2>/dev/null | sed 's/^v//'),dev)
LDFLAGS := -X main.version=$(VERSION)

PLUGIN_CACHE := $(HOME)/.claude/plugins/cache/punt-labs/ethos
PLUGIN_VERSION := $(shell ls -1 $(PLUGIN_CACHE) 2>/dev/null | grep -v '\.bak$$' | sort -V | tail -1)

# golangci-lint is the Go lint gate (Go Report Card successor). Pinned so
# local and CI run the same analyzer versions; keep in sync with
# .github/workflows/test.yml. Config lives in .golangci.yml.
#
# `make tools` installs the exact prebuilt release binary, not a fresh
# `go install` build. golangci-lint bundles a gofmt-compatible formatter that
# embeds whatever Go standard library (go/printer's comment-alignment
# heuristics have moved between point releases) it was compiled against. A
# `go install ...@$(GOLANGCI_LINT_VERSION)` compiles against whatever Go
# toolchain happens to be on the developer's PATH, while
# golangci-lint-action's default `install-mode: binary` downloads the
# official release artifact — built once, by the golangci-lint project, on
# its own pinned Go version. Two builds of the same source at different Go
# versions can format identical input differently, which is exactly the
# false-negative-locally / fail-in-CI split this project hit (see the
# gofmt-agnostic rewrite in internal/mission/migrate_test.go). Fetching the
# same release asset CI fetches removes the toolchain as a variable: gofmt's
# formatting logic is pure Go text processing with no OS/arch dependence, so
# even a darwin/arm64 developer and the linux/amd64 CI runner make identical
# formatting decisions once both are running a binary built from the same
# release commit at the same Go version — only the raw bytes of the two
# binaries differ, never their verdict on a given source file.
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
# need a TeX toolchain. Passing no bib-backend flag lets latexmk pick
# biber vs bibtex from the auxiliary files the first pdflatex run
# generates (a .bcf implies biber; a .aux with \bibdata implies bibtex).
# prfaq.tex is biblatex + backend=biber → latexmk emits a .bcf → biber;
# docs/*.tex cite nothing → no bib backend runs at all. `-c` after the
# build sweeps latexmk's intermediates; the explicit basename+extension
# loop below sweeps the ones `-c` keeps when a .bib is present (.bbl).
# Net: only .tex and .pdf remain on disk after docs-pdf.
LATEX_TEX := prfaq.tex $(wildcard docs/*.tex)
LATEX_INTERMEDIATE_EXTS := aux bbl bcf blg fdb_latexmk fls log out run.xml synctex.gz toc

docs-pdf: ## Rebuild prfaq.pdf + docs/*.pdf from .tex sources, then sweep intermediates
	@command -v latexmk >/dev/null || { echo "latexmk not found — install a TeX distribution (MacTeX, TeX Live)"; exit 1; }
	@for f in $(LATEX_TEX); do \
		echo "==> latexmk $$f"; \
		latexmk -pdf -interaction=nonstopmode -halt-on-error -cd "$$f" || exit 1; \
		latexmk -c -cd "$$f" >/dev/null; \
		base=$${f%.tex}; \
		for ext in $(LATEX_INTERMEDIATE_EXTS); do \
			rm -f "$$base.$$ext"; \
		done; \
	done

# Scoped to intermediates that sit next to a KNOWN .tex source in LATEX_TEX.
# A repo-wide `find -name '*.log' -delete` would also catch coverage.out,
# .beads/daemon.log, worktree checkouts under sibling repos, etc. — none of
# which are ours to remove. Keying on LATEX_TEX basenames means we only touch
# files whose provenance is a .tex we know about.
clean-latex: ## Remove LaTeX build intermediates (keeps .pdf/.tex/.bib)
	@for f in $(LATEX_TEX); do \
		base=$${f%.tex}; \
		for ext in $(LATEX_INTERMEDIATE_EXTS); do \
			rm -f "$$base.$$ext"; \
		done; \
	done

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

# Fetches golangci-lint's official prebuilt release binary directly — the
# same artifact golangci-lint-action's default install-mode=binary
# downloads for CI — instead of compiling one against whatever local Go
# toolchain happens to be on PATH. See the GOLANGCI_LINT_VERSION comment
# above for why the artifact, not a fresh build, is the thing that must
# match CI.
#
# This does NOT shell out to golangci-lint's own install.sh. That script
# downloads and executes shell logic fetched fresh on every `make tools`
# run, on every developer's persistent workstation — a workstation that,
# in this org, holds GPG signing keys and pass-resolved credentials.
# Piping curl into sh puts code nobody in this repo has read in the path
# of every future `make tools`, invisible to `git blame` and to PR
# review, and re-fetched (not reviewed once) on every run. CI already
# runs the same prebuilt artifact via golangci-lint-action, so the trust
# root below is not new; what matters is that the fetch, verify, and
# extract logic are ordinary Makefile lines, reviewed once in this diff,
# not a script this repo never sees.
#
# The checksum check is a same-origin integrity check, not a stronger
# trust root: checksums.txt is published by the same account, in the
# same release, over the same channel as the tarball it describes. It
# catches transit corruption and accidental mismatches; it does not
# defend against a compromise of golangci-lint's own release pipeline,
# since whoever could swap the tarball could swap the checksums beside
# it. Git tags are mutable — the $(GOLANGCI_LINT_VERSION) URL below
# resolves live on every fetch, with no transparency log — so treat this
# as exactly the trust root CI already uses via golangci-lint-action, not
# an "immutable pin."
tools: ## Install development tools
	@mkdir -p $(GOBIN)
	@set -e; \
	ver=$(GOLANGCI_LINT_VERSION); \
	verNum=$${ver#v}; \
	os=$$(uname -s | tr '[:upper:]' '[:lower:]'); \
	arch=$$(uname -m); \
	case "$$arch" in x86_64) arch=amd64 ;; aarch64) arch=arm64 ;; esac; \
	name=golangci-lint-$${verNum}-$${os}-$${arch}; \
	tarball=$${name}.tar.gz; \
	checksums=golangci-lint-$${verNum}-checksums.txt; \
	base=https://github.com/golangci/golangci-lint/releases/download/$${ver}; \
	tmpdir=$$(mktemp -d); \
	trap 'rm -rf "$$tmpdir"' EXIT; \
	echo "fetching $$tarball ($$ver, $$os/$$arch)"; \
	curl -sSfL -o "$$tmpdir/$$tarball" "$$base/$$tarball"; \
	curl -sSfL -o "$$tmpdir/$$checksums" "$$base/$$checksums"; \
	line=$$(grep -F -- "$$tarball" "$$tmpdir/$$checksums" || true); \
	count=$$(printf '%s\n' "$$line" | grep -c . || true); \
	if [ "$$count" -ne 1 ]; then \
	  echo "expected exactly one checksum entry for $$tarball in $$checksums, found $$count" >&2; \
	  exit 1; \
	fi; \
	if command -v sha256sum >/dev/null 2>&1; then \
	  ( cd "$$tmpdir" && echo "$$line" | sha256sum -c - ) || { echo "checksum verification failed for $$tarball" >&2; exit 1; }; \
	elif command -v shasum >/dev/null 2>&1; then \
	  ( cd "$$tmpdir" && echo "$$line" | shasum -a 256 -c - ) || { echo "checksum verification failed for $$tarball" >&2; exit 1; }; \
	else \
	  echo "neither sha256sum nor shasum found; cannot verify $$tarball" >&2; exit 1; \
	fi; \
	tar -xzf "$$tmpdir/$$tarball" -C "$$tmpdir"; \
	install "$$tmpdir/$$name/golangci-lint" "$(GOBIN)/golangci-lint"; \
	echo "installed $(GOBIN)/golangci-lint $$ver"

doctor: build ## Run ethos doctor
	./ethos doctor
