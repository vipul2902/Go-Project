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

.PHONY: web-dev
web-dev: ## Dashboard dev server on :5173 (proxies /api to the API on :8080)
	cd web && npm run dev

.PHONY: web-check
web-check: ## Type-check, lint, test and build the dashboard
	cd web && npm run typecheck && npm run lint && npm test && npm run build

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

CHART     := deploy/helm/clouddeploy
KCTX      := $(or $(KUBE_CONTEXT),colima)
KNS       := clouddeploy
KDEPS_NS  := clouddeploy-deps

.PHONY: helm-lint
helm-lint: ## Lint the chart with every values file and check its guards
	@for v in dev staging prod; do helm lint $(CHART) -f $(CHART)/values-$$v.yaml --quiet || exit 1; done
	@helm lint $(CHART) --quiet
	@! helm template x $(CHART) -f $(CHART)/values-prod.yaml --set dev.dockerSocket.enabled=true >/dev/null 2>&1 \
		|| (echo "guard failed: docker socket allowed in production"; exit 1)
	@echo "helm-lint: ok"

.PHONY: k8s-images
k8s-images: ## Build the app and dev worker-builder images, tagged by content digest
	@# Immutable tags: a mutable tag like "dev" would leave the Deployment spec
	@# unchanged after a rebuild, so Kubernetes would never roll the pods.
	@id=$$(docker build -q -t clouddeploy:dev .) && tag=dev-$$(echo $$id | cut -c8-19) && \
		docker tag clouddeploy:dev clouddeploy:$$tag && echo $$tag > .k8s-image-tag && echo "app image: clouddeploy:$$tag"
	@id=$$(docker build -q --target worker-builder -t clouddeploy-worker-builder:dev .) && tag=dev-$$(echo $$id | cut -c8-19) && \
		docker tag clouddeploy-worker-builder:dev clouddeploy-worker-builder:$$tag && echo $$tag > .k8s-builder-tag && echo "builder image: clouddeploy-worker-builder:$$tag"
	@id=$$(docker build -q -t clouddeploy-web:dev web/) && tag=dev-$$(echo $$id | cut -c8-19) && \
		docker tag clouddeploy-web:dev clouddeploy-web:$$tag && echo $$tag > .k8s-web-tag && echo "web image: clouddeploy-web:$$tag"

.PHONY: k8s-deps
k8s-deps: ## DEV ONLY: Postgres + Redis inside the cluster (production uses managed services)
	@set -a; source .env; set +a; \
	kubectl --context $(KCTX) create namespace $(KDEPS_NS) --dry-run=client -o yaml | kubectl --context $(KCTX) apply -f - >/dev/null; \
	printf 'password=%s\n' "$$POSTGRES_PASSWORD" | kubectl --context $(KCTX) -n $(KDEPS_NS) create secret generic postgres-credentials \
		--from-env-file=/dev/stdin --dry-run=client -o yaml | kubectl --context $(KCTX) apply -f - >/dev/null
	kubectl --context $(KCTX) apply -f deploy/kubernetes/dev-deps/
	kubectl --context $(KCTX) -n $(KDEPS_NS) rollout status statefulset/postgres --timeout=180s
	kubectl --context $(KCTX) -n $(KDEPS_NS) rollout status deployment/redis --timeout=120s

.PHONY: k8s-secrets
k8s-secrets: ## DEV ONLY: create the app Secret from .env (values never on the command line)
	@set -a; source .env; set +a; \
	kubectl --context $(KCTX) create namespace $(KNS) --dry-run=client -o yaml | kubectl --context $(KCTX) apply -f - >/dev/null; \
	kubectl --context $(KCTX) label namespace $(KNS) --overwrite \
		pod-security.kubernetes.io/enforce=privileged pod-security.kubernetes.io/warn=restricted >/dev/null; \
	{ printf 'DATABASE_URL=postgres://clouddeploy:%s@postgres.$(KDEPS_NS).svc.cluster.local:5432/clouddeploy?sslmode=disable\n' "$$POSTGRES_PASSWORD"; \
	  printf 'REDIS_URL=redis://redis.$(KDEPS_NS).svc.cluster.local:6379/0\n'; \
	  printf 'JWT_SECRET=%s\nSEED_PASSWORD=%s\n' "$$JWT_SECRET" "$$SEED_PASSWORD"; } \
	| kubectl --context $(KCTX) -n $(KNS) create secret generic clouddeploy-secrets --from-env-file=/dev/stdin \
		--dry-run=client -o yaml | kubectl --context $(KCTX) apply -f - >/dev/null
	@echo "secret $(KNS)/clouddeploy-secrets applied"

.PHONY: k8s-deploy
k8s-deploy: k8s-images k8s-deps k8s-secrets ## Install/upgrade CloudDeploy in the local cluster (dev values)
	helm upgrade --install clouddeploy $(CHART) --kube-context $(KCTX) -n $(KNS) \
		-f $(CHART)/values-dev.yaml --set image.tag=$$(cat .k8s-image-tag) \
		--set dev.builderImage.tag=$$(cat .k8s-builder-tag) --set web.image.tag=$$(cat .k8s-web-tag) --wait --timeout 5m

.PHONY: k8s-forward
k8s-forward: ## Open the in-cluster dashboard (and /api) on http://localhost:8080
	kubectl --context $(KCTX) -n $(KNS) port-forward svc/clouddeploy-web 8080:80

.PHONY: k8s-uninstall
k8s-uninstall: ## Remove the CloudDeploy release (keeps the dev database)
	helm uninstall clouddeploy --kube-context $(KCTX) -n $(KNS)

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
