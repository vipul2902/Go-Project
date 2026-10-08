SHELL   := /bin/bash
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BIN     := bin/api
LDFLAGS := -s -w -X main.version=$(VERSION)

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-18s\033[0m %s\n",$$1,$$2}'

.PHONY: dev
dev: ## Run the API locally (loads .env if present)
	@set -a; [ -f .env ] && source .env; set +a; go run ./cmd/api

.PHONY: build
build: ## Build the API binary into bin/
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN) ./cmd/api

.PHONY: test
test: ## Run unit tests with the race detector
	go test -race -count=1 ./...

.PHONY: cover
cover: ## Run tests with coverage report
	go test -race -coverprofile=coverage.out ./... && go tool cover -func=coverage.out | tail -1

.PHONY: fmt
fmt: ## Format code
	gofmt -s -w .

.PHONY: lint
lint: ## Check formatting and run go vet (golangci-lint added in CI phase)
	@test -z "$$(gofmt -s -l .)" || (echo "unformatted files:"; gofmt -s -l .; exit 1)
	go vet ./...

.PHONY: tidy
tidy: ## Tidy go.mod
	go mod tidy

.PHONY: docker-build
docker-build: ## Build the API container image
	docker build --build-arg VERSION=$(VERSION) -t clouddeploy-api:$(VERSION) -t clouddeploy-api:latest .

.PHONY: docker-up
docker-up: ## Start the core local stack
	docker compose --profile core up -d --build

.PHONY: docker-down
docker-down: ## Stop the local stack
	docker compose --profile core --profile observability down

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf bin coverage.out
