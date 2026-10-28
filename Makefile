# Developer entry points. `make help` lists everything.

SHELL := /bin/bash
.DEFAULT_GOAL := help

MODULE       := github.com/ashur/ttr
BIN_DIR      := bin
PROTO_DIR    := api/proto
PROTO_OUT    := pkg/pb
PROTO_FILES  := $(shell find $(PROTO_DIR) -name '*.proto')
VERSION      ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMPOSE      := docker compose

# Pinned so every machine and CI agent generates byte-identical code.
PROTOC_GEN_GO_VERSION      := v1.36.12
PROTOC_GEN_GO_GRPC_VERSION := v1.6.2

export PATH := $(shell go env GOPATH)/bin:$(PATH)

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'

## ---- code generation ----

.PHONY: tools
tools: ## Install the pinned protoc plugins
	go install google.golang.org/protobuf/cmd/protoc-gen-go@$(PROTOC_GEN_GO_VERSION)
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@$(PROTOC_GEN_GO_GRPC_VERSION)

.PHONY: generate
generate: tools ## Regenerate Go code from the .proto files
	rm -rf $(PROTO_OUT)
	mkdir -p $(PROTO_OUT)
	protoc \
		--proto_path=$(PROTO_DIR) \
		--go_out=$(PROTO_OUT) --go_opt=module=$(MODULE)/$(PROTO_OUT) \
		--go-grpc_out=$(PROTO_OUT) --go-grpc_opt=module=$(MODULE)/$(PROTO_OUT),require_unimplemented_servers=true \
		$(PROTO_FILES)

## ---- build & test ----

.PHONY: build
build: ## Build the server binary into ./bin
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o $(BIN_DIR)/server ./cmd/server

.PHONY: test
test: ## Run unit tests with the race detector
	go test -race -count=1 ./...

.PHONY: cover
cover: ## Run tests and open the coverage report
	# -coverpkg attributes coverage to the package under test even when the
	# test lives in another package (interceptors are driven via the server).
	go test -race -count=1 -coverpkg=./internal/...,./cmd/... -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1
	go tool cover -html=coverage.out

.PHONY: lint
lint: ## Format check, vet and staticcheck
	@test -z "$$(gofmt -l . | tee /dev/stderr)" || (echo "gofmt: files need formatting" && exit 1)
	go vet ./...
	@command -v staticcheck >/dev/null 2>&1 \
		&& staticcheck ./... \
		|| echo "staticcheck not installed; skipping (go install honnef.co/go/tools/cmd/staticcheck@latest)"

.PHONY: tidy
tidy: ## Tidy and verify module dependencies
	go mod tidy
	go mod verify

## ---- docker ----

.PHONY: up
up: ## Build and start postgres, migrations and the API
	VERSION=$(VERSION) $(COMPOSE) up --build -d
	$(COMPOSE) ps

.PHONY: down
down: ## Stop the stack, keeping data
	$(COMPOSE) down

.PHONY: clean
clean: ## Stop the stack and delete the database volume
	$(COMPOSE) down -v
	rm -rf $(BIN_DIR) coverage.out

.PHONY: logs
logs: ## Follow API logs
	$(COMPOSE) logs -f api

.PHONY: ps
ps: ## Show container status
	$(COMPOSE) ps

.PHONY: psql
psql: ## Open a psql shell on the running database
	$(COMPOSE) exec postgres psql -U $${POSTGRES_USER:-app} -d $${POSTGRES_DB:-app}

## ---- migrations ----

MIGRATE_RUN := $(COMPOSE) run --rm migrate

.PHONY: migrate-up
migrate-up: ## Apply all pending migrations
	$(MIGRATE_RUN)

.PHONY: migrate-down
migrate-down: ## Roll back the last migration
	$(MIGRATE_RUN) -path=/migrations -database="postgres://$${POSTGRES_USER:-app}:$${POSTGRES_PASSWORD:-app_local_password}@postgres:5432/$${POSTGRES_DB:-app}?sslmode=disable" down 1

.PHONY: migrate-create
migrate-create: ## Create a migration pair: make migrate-create NAME=add_something
	@test -n "$(NAME)" || (echo "usage: make migrate-create NAME=add_something" && exit 1)
	$(COMPOSE) run --rm --entrypoint migrate migrate create -ext sql -dir /migrations -seq $(NAME)

## ---- api calls ----

# grpcurl runs in a container on the Compose network, so nothing has to be
# installed locally and the API is reached by service name.
GRPCURL := docker run --rm --network $(shell basename $(CURDIR))_default -i fullstorydev/grpcurl:latest -plaintext

.PHONY: smoke
smoke: ## Create a user and list users through the running API
	$(GRPCURL) -d '{"email":"ada@example.com","full_name":"Ada Lovelace"}' \
		api:50051 user.v1.UserService/CreateUser
	$(GRPCURL) -d '{"page_size":10}' \
		api:50051 user.v1.UserService/ListUsers

.PHONY: describe
describe: ## Print the API schema from server reflection
	$(GRPCURL) api:50051 describe user.v1.UserService

.PHONY: health
health: ## Query the gRPC health endpoint
	$(GRPCURL) api:50051 grpc.health.v1.Health/Check

.PHONY: test-integration
test-integration: ## Run repository tests against the Compose database
	TEST_DATABASE_URL="postgres://$${POSTGRES_USER:-app}:$${POSTGRES_PASSWORD:-app_local_password}@127.0.0.1:$${POSTGRES_PORT:-5432}/$${POSTGRES_DB:-app}?sslmode=disable" \
		go test -race -count=1 -tags=integration ./internal/repository/...
