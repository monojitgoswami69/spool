BINARY      := spool
MODULE      := github.com/monojitgoswami69/spool
VERSION     ?= v0.1.0
BUILD_TIME  ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS     := -s -w -X $(MODULE)/cmd.Version=$(VERSION) -X $(MODULE)/cmd.BuildTime=$(BUILD_TIME)

.PHONY: all build test race vet fmt tidy install clean run help

all: build

build: ## Build the spool binary
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) .

test: ## Run unit tests
	go test ./...

race: ## Run tests with the race detector
	go test -race ./...

vet: ## Run go vet
	go vet ./...

fmt: ## Format all Go files
	gofmt -w .

tidy: ## Tidy module dependencies
	go mod tidy

install: ## Install spool into GOBIN
	go install -trimpath -ldflags "$(LDFLAGS)" .

clean: ## Remove build artifacts
	rm -f $(BINARY)
	rm -rf dist bin

run: build ## Build and show help
	./$(BINARY) --help

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-12s\033[0m %s\n", $$1, $$2}'