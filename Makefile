VERSION := $(or $(shell git describe --tags --always 2>/dev/null | sed 's/^v//'),dev)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: help lint docs test check format build install clean dist cover tools doctor

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-12s %s\n", $$1, $$2}'

lint: ## Lint (go vet + staticcheck + shellcheck)
	go vet ./...
	$(shell go env GOPATH)/bin/staticcheck ./...
	shellcheck hooks/*.sh install.sh

docs: ## Lint markdown
	npx --yes markdownlint-cli2 "**/*.md" "#node_modules"

test: ## Run tests with race detection
	go test -race -count=1 ./...

check: lint docs test ## Run all quality gates

format: ## Format code
	gofmt -w .

build: ## Build binary
	CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -o ethos ./cmd/ethos/

install: build ## Build and install to ~/.local/bin
	mkdir -p $(HOME)/.local/bin
	rm -f $(HOME)/.local/bin/ethos
	cp ethos $(HOME)/.local/bin/ethos

clean: ## Remove build artifacts
	rm -f ethos coverage.out
	rm -rf dist/

dist: clean ## Cross-compile for all platforms
	mkdir -p dist
	CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -ldflags="-s -w $(LDFLAGS)" -o dist/ethos-darwin-arm64 ./cmd/ethos/
	CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64 go build -ldflags="-s -w $(LDFLAGS)" -o dist/ethos-darwin-amd64 ./cmd/ethos/
	CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -ldflags="-s -w $(LDFLAGS)" -o dist/ethos-linux-arm64  ./cmd/ethos/
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -ldflags="-s -w $(LDFLAGS)" -o dist/ethos-linux-amd64  ./cmd/ethos/

cover: ## Test with coverage report
	go test -cover -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

tools: ## Install development tools
	go install honnef.co/go/tools/cmd/staticcheck@latest

doctor: build ## Run ethos doctor
	./ethos doctor
