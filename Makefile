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
test-k8s: ## Kubernetes tests against the local cluster (needs make k8s-up and make db-up)
	@set -a; source .env; set +a; KUBE_CONTEXT=$${KUBE_CONTEXT:-colima} \
		go test -count=1 -p 1 -tags k8s -timeout 10m ./internal/kube/ ./internal/monitoring/

.PHONY: web-dev
web-dev: ## Dashboard dev server on :5173 (proxies /api to the API on :8080)
	cd web && npm run dev

.PHONY: web-check
web-check: ## Type-check, lint, test (with coverage) and build the dashboard
	cd web && npm run typecheck && npm run lint && npm run coverage && npm run build

.PHONY: cover
cover: ## Combined unit + integration coverage (needs make db-up); writes coverage.out
	@set -a; source .env; set +a; go test -count=1 -p 1 -tags integration -coverpkg=./internal/... \
		-coverprofile=coverage.out ./... >/dev/null && go tool cover -func=coverage.out | tail -1

.PHONY: fuzz
fuzz: ## Run every fuzz target (FUZZTIME=20s each by default)
	@for t in $$(grep -rhoE '^func (Fuzz[A-Za-z]+)' --include='*_test.go' internal | awk '{print $$2}' | sort -u); do \
		pkg=$$(grep -rlE "^func $$t\(" --include='*_test.go' internal | xargs dirname); \
		printf "%-40s " $$t; go test -run '^$$' -fuzz "^$$t\$$" -fuzztime $${FUZZTIME:-20s} ./$$pkg/ 2>&1 | tail -1; \
	done

.PHONY: bench
bench: ## Benchmarks for hot paths (password hashing, JWT, client IP, log sanitizing, manifests)
	go test -run '^$$' -bench . -benchmem ./internal/...

.PHONY: test-stress
test-stress: ## Integration suite 3x under the race detector, to surface flaky tests
	@set -a; source .env; set +a; go test -race -count=3 -p 1 -tags integration ./...

.PHONY: test-all
test-all: lint test test-integration web-check helm-lint terraform-check ## Everything except cluster tests (add make test-k8s)

.PHONY: vulncheck
vulncheck: ## Reachable vulnerabilities in Go code and dependencies (same as CI)
	go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...

.PHONY: fmt
fmt: ## Format code
	gofmt -s -w .

.PHONY: lint
lint: ## gofmt check, go vet and golangci-lint (same config as CI: .golangci.yml)
	@test -z "$$(gofmt -s -l .)" || (echo "unformatted files:"; gofmt -s -l .; exit 1)
	go vet ./...
	golangci-lint run ./...

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

.PHONY: perf
perf: ## Load test (k6) the API and deployment queue with profiling; results in .perf/ (needs make db-up)
	scripts/load/perf.sh

.PHONY: e2e
e2e: ## End-to-end: build images, install the chart in a separate release, deploy a real app via the API
	docker build -q -t clouddeploy:e2e-local . >/dev/null
	docker build -q -t clouddeploy-web:e2e-local web/ >/dev/null
	KUBE_CONTEXT=$(KCTX) IMAGE_REPO=clouddeploy WEB_IMAGE_REPO=clouddeploy-web IMAGE_TAG=e2e-local scripts/e2e.sh

.PHONY: helm-lint
helm-lint: ## Lint the chart with every values file and check its guards
	@for v in dev staging prod; do helm lint $(CHART) -f $(CHART)/values-$$v.yaml --quiet || exit 1; done
	@helm lint $(CHART) --quiet
	@! helm template x $(CHART) -f $(CHART)/values-prod.yaml --set dev.dockerSocket.enabled=true >/dev/null 2>&1 \
		|| (echo "guard failed: docker socket allowed in production"; exit 1)
	@helm lint $(CHART) -f $(CHART)/values-staging.yaml -f $(CHART)/values-azure.yaml -f $(CHART)/ci/azure-values.yaml --quiet
	@! helm template x $(CHART) --set worker.builder=acr >/dev/null 2>&1 \
		|| (echo "guard failed: acr builder allowed without azure settings"; exit 1)
	@! helm template x $(CHART) -f $(CHART)/values-azure.yaml >/dev/null 2>&1 \
		|| (echo "guard failed: azure mode rendered without IDs"; exit 1)
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

.PHONY: obs-up
obs-up: ## Start Prometheus (:9091), Grafana (:3000), Jaeger (:16686); opt-in, ~300 MB
	docker compose --profile observability up -d --wait
	@echo "Run the API/worker with OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 for traces."

.PHONY: obs-down
obs-down: ## Stop the observability stack
	docker compose --profile observability stop

TF_DIR   := infra/terraform
TF_ROOTS := envs/dev envs/staging envs/prod modules/platform
export TF_PLUGIN_CACHE_DIR ?= $(HOME)/.terraform.d/plugin-cache

.PHONY: terraform-check
terraform-check: ## Terraform fmt, validate and offline tests (mocked Azure; no account needed)
	@mkdir -p $(TF_PLUGIN_CACHE_DIR)
	terraform -chdir=$(TF_DIR) fmt -check -recursive
	@for d in $(TF_ROOTS); do \
		terraform -chdir=$(TF_DIR)/$$d init -backend=false -input=false >/dev/null || exit 1; \
		printf '%-20s ' $$d; terraform -chdir=$(TF_DIR)/$$d validate -no-color | head -1 || exit 1; \
	done
	terraform -chdir=$(TF_DIR)/modules/platform test

# There is deliberately no apply target: applying creates billable Azure
# resources and is a reviewed, manual (later: CD-approved) step.
.PHONY: terraform-plan
terraform-plan: ## Plan an environment against Azure (ENV=dev|staging|prod; needs az login + backend.hcl)
	@command -v az >/dev/null || { echo "az CLI not installed (brew install azure-cli)"; exit 1; }
	@az account show >/dev/null 2>&1 || { echo "not logged in: run az login"; exit 1; }
	@test -f $(TF_DIR)/envs/$(or $(ENV),dev)/backend.hcl || { echo "missing backend.hcl: copy backend.hcl.example"; exit 1; }
	terraform -chdir=$(TF_DIR)/envs/$(or $(ENV),dev) init -input=false -backend-config=backend.hcl
	terraform -chdir=$(TF_DIR)/envs/$(or $(ENV),dev) plan -input=false

# ---- Azure (Phase 18). Nothing here runs terraform apply. ----------------
AZ_ENV   := $(or $(ENV),dev)
AZ_TF    := $(TF_DIR)/envs/$(AZ_ENV)
AZ_VALS  := $(CHART)/values-azure.generated.yaml
AZ_OUT    = terraform -chdir=$(AZ_TF) output -json platform
AZ_TAG   ?= $(shell git rev-parse --short=12 HEAD)
KUBELOGIN ?= $(or $(shell command -v kubelogin),$(HOME)/.local/bin/kubelogin)

.PHONY: azure-check
azure-check: ## Show the Azure account and subscription in use (never prints tokens)
	@az account show --query '{user:user.name, subscription:name, id:id}' -o table

.PHONY: azure-values
azure-values: ## Write Helm values for Azure from terraform output (IDs only, no secrets)
	@$(AZ_OUT) | jq -r '"# Generated by make azure-values from terraform output. Not secret; git-ignored.",\
		"image: {repository: \(.acr_login_server)/clouddeploy}",\
		"web: {image: {repository: \(.acr_login_server)/clouddeploy-web}}",\
		"azure:", "  tenantId: \(.tenant_id)", "  keyVaultName: \(.key_vault_name)", "  acrRegistryId: \(.acr_id)",\
		"  clientIds: {api: \(.workload_client_ids["clouddeploy-api"]), worker: \(.workload_client_ids["clouddeploy-worker"]), migrate: \(.workload_client_ids["clouddeploy-migrate"])}"' > $(AZ_VALS)
	@echo "wrote $(AZ_VALS)"

# ACR Tasks builds the images in Azure for linux/amd64 (this Mac is arm64),
# tagged with the git commit: immutable, so every deploy rolls the pods.
.PHONY: azure-images
azure-images: ## Build and push the app and web images with ACR Tasks (tag = git commit)
	@test -z "$$(git status --porcelain)" || { echo "commit your changes first: the image tag is the commit"; exit 1; }
	@reg=$$($(AZ_OUT) | jq -r .acr_id | awk -F/ '{print $$NF}'); \
	az acr build -r $$reg -t clouddeploy:$(AZ_TAG) --platform linux/amd64 . && \
	az acr build -r $$reg -t clouddeploy-web:$(AZ_TAG) --platform linux/amd64 web/

.PHONY: azure-credentials
azure-credentials: ## Fetch AKS credentials (Entra ID via kubelogin; no static admin config)
	@out=$$($(AZ_OUT)); az aks get-credentials -g $$(echo $$out | jq -r .resource_group) -n $$(echo $$out | jq -r .aks_name) --overwrite-existing
	$(KUBELOGIN) convert-kubeconfig -l azurecli

.PHONY: azure-deploy
azure-deploy: azure-values ## Install/upgrade CloudDeploy on AKS (ENV=dev; images from make azure-images)
	@ctx=$$($(AZ_OUT) | jq -r .aks_name); \
	helm upgrade --install clouddeploy $(CHART) --kube-context $$ctx -n $(KNS) --create-namespace \
		-f $(CHART)/values-$(subst dev,staging,$(AZ_ENV)).yaml -f $(CHART)/values-azure.yaml -f $(AZ_VALS) \
		--set image.tag=$(AZ_TAG) --set web.image.tag=$(AZ_TAG) --wait --timeout 10m

.PHONY: azure-forward
azure-forward: ## Open the AKS-hosted dashboard on http://localhost:8080
	kubectl --context $$($(AZ_OUT) | jq -r .aks_name) -n $(KNS) port-forward svc/clouddeploy-web 8080:80

# Cost control without deleting data: a stopped AKS cluster and PostgreSQL
# server bill only for storage. PostgreSQL restarts itself after 7 days.
.PHONY: azure-stop
azure-stop: ## Stop AKS and PostgreSQL to pause compute billing (data kept)
	@out=$$($(AZ_OUT)); rg=$$(echo $$out | jq -r .resource_group); \
	az aks stop -g $$rg -n $$(echo $$out | jq -r .aks_name); \
	az postgres flexible-server stop -g $$rg -n $$(echo $$out | jq -r .postgres_fqdn | cut -d. -f1)

.PHONY: azure-start
azure-start: ## Start AKS and PostgreSQL again
	@out=$$($(AZ_OUT)); rg=$$(echo $$out | jq -r .resource_group); \
	az postgres flexible-server start -g $$rg -n $$(echo $$out | jq -r .postgres_fqdn | cut -d. -f1); \
	az aks start -g $$rg -n $$(echo $$out | jq -r .aks_name)

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
