# lotsman — security-first OpenAPI → MCP runtime.
# Every target must keep main green (Constitution X).

GO              ?= go
BIN             ?= bin/lotsman
PKG             := ./...
VERSION         ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT          ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE            ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
BUILDINFO       := github.com/razrabotchik/lotsman/internal/buildinfo
LDFLAGS         := -s -w \
                   -X '$(BUILDINFO).version=$(VERSION)' \
                   -X '$(BUILDINFO).commit=$(COMMIT)' \
                   -X '$(BUILDINFO).date=$(DATE)'
GOLANGCI        ?= golangci-lint
FUZZTIME        ?= 30s
# SPEC/ARGS feed `make run`: make run SPEC=testdata/mini/basic.yaml
SPEC            ?=
ARGS            ?=

.DEFAULT_GOAL := help

.PHONY: help
help: ## List targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
	  | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the binary into bin/
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/lotsman

.PHONY: run
run: ## Run the stdio MCP server (make run SPEC=path/to/openapi.yaml)
	$(GO) run -ldflags "$(LDFLAGS)" ./cmd/lotsman serve $(SPEC) $(ARGS)

.PHONY: test
test: ## Run tests with the race detector
	$(GO) test -race -shuffle=on -count=1 $(PKG)

.PHONY: cover
cover: ## Run tests and write coverage.out
	$(GO) test -race -count=1 -coverprofile=coverage.out -covermode=atomic $(PKG)
	$(GO) tool cover -func=coverage.out | tail -1

.PHONY: fuzz
fuzz: ## Run every fuzz target briefly (FUZZTIME=30s)
	@set -e; for pkg in $$($(GO) list $(PKG)); do \
	  for target in $$($(GO) test -list '^Fuzz' $$pkg 2>/dev/null | grep '^Fuzz' || true); do \
	    echo "==> $$pkg $$target ($(FUZZTIME))"; \
	    $(GO) test $$pkg -run '^$$' -fuzz "^$$target$$" -fuzztime $(FUZZTIME); \
	  done; \
	done

.PHONY: lint
lint: ## Run golangci-lint
	$(GOLANGCI) run

.PHONY: fmt
fmt: ## Format and tidy imports
	$(GOLANGCI) fmt

.PHONY: tidy
tidy: ## Tidy go.mod/go.sum
	$(GO) mod tidy
	$(GO) mod verify

.PHONY: check
check: lint test ## Lint + test (what CI gates on)

.PHONY: clean
clean: ## Remove build and coverage artifacts
	rm -rf bin coverage.out
