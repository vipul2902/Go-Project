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
worker: ## Run the worker locally (Docker builds; Kubernetes deploys to the colima context)
	@set -a; [ -f .env ] && source .env; set +a; \
	WORKER_BUILDER=$${WORKER_BUILDER:-docker} WORKER_DEPLOYER=$${WORKER_DEPLOYER:-kubernetes} \
	KUBE_CONTEXT=$${KUBE_CONTEXT:-colima} go run ./cmd/worker

.PHONY: build
build: ## Build the API and worker binaries into bin/
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN) ./cmd/api
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/worker ./cmd/worker

.PHONY: test
test: ## Run unit tests with the race detector
	go test -race -count=1 ./...

.PHONY: test-integration
test-integration: ## Integration tests: compose Postgres + Redis, local git + Docker (needs make db-up)
	@set -a; source .env; set +a; go test -race -count=1 -p 1 -tags integration ./...

.PHONY: test-k8s
test-k8s: ## Kubernetes deployer tests against the local cluster (needs make k8s-up)
	KUBE_CONTEXT=$${KUBE_CONTEXT:-colima} go test -count=1 -tags k8s -timeout 10m ./internal/kube/

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

.PHONY: k8s-up
k8s-up: ## Start Colima with Kubernetes (k3s) enabled
	colima start --kubernetes
	kubectl --context colima wait --for=condition=Ready node --all --timeout=180s

.PHONY: k8s-down
k8s-down: ## Disable Kubernetes to free ~1 GB of RAM (Colima keeps running)
	colima kubernetes stop

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
