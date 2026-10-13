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

.PHONY: worker
worker: ## Run the deployment worker locally (dry-run executor)
	@set -a; [ -f .env ] && source .env; set +a; DEPLOY_EXECUTOR=$${DEPLOY_EXECUTOR:-dryrun} go run ./cmd/worker

.PHONY: build
build: ## Build the API and worker binaries into bin/
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN) ./cmd/api
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/worker ./cmd/worker

.PHONY: test
test: ## Run unit tests with the race detector
	go test -race -count=1 ./...

.PHONY: test-integration
test-integration: ## Run integration tests against compose Postgres + Redis (needs make db-up)
	@set -a; source .env; set +a; go test -race -count=1 -p 1 -tags integration ./...

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

.PHONY: db-up
db-up: ## Start Postgres + Redis (run the API natively with make dev)
	docker compose --profile deps up -d --wait

.PHONY: migrate
migrate: ## Apply migrations (CMD=up|down|reset|status)
	@set -a; source .env; set +a; go run ./cmd/migrate $(or $(CMD),up)

.PHONY: seed
seed: ## Load development seed data (idempotent)
	@set -a; source .env; set +a; go run ./cmd/seed

.PHONY: psql
psql: ## Open a psql shell in the Postgres container
	docker compose exec postgres psql -U clouddeploy -d clouddeploy

.PHONY: docker-build
docker-build: ## Build the API container image
	docker build --build-arg VERSION=$(VERSION) -t clouddeploy-api:$(VERSION) -t clouddeploy-api:latest .

.PHONY: docker-up
docker-up: ## Start Postgres, run migrations, start the API (all in containers)
	docker compose --profile core up -d --build --wait

.PHONY: docker-down
docker-down: ## Stop the local stack (data volume kept; add -v to wipe)
	docker compose --profile deps --profile core --profile observability down

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf bin coverage.out
