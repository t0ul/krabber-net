SHELL := /bin/bash

DYNAMO_ENDPOINT ?= http://localhost:8000
TABLE_NAME      ?= krabber-dev
# macOS AirPlay Receiver holds :5000, so local dev uses :5050 (Beanstalk uses :5000).
PORT            ?= 5050
GO_BUILD        := CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w"

.PHONY: help dev db db-down seed run test test-unit lint vet fmt vuln build bundle clean

help: ## Show targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-10s %s\n", $$1, $$2}'

dev: db seed run ## Start DynamoDB Local, create the table, run the site on :5050

db: ## Start DynamoDB Local
	docker compose up -d dynamodb-local

db-down: ## Stop DynamoDB Local
	docker compose down

seed: ## Create the dev table (and sample crabs) in DynamoDB Local
	DYNAMO_ENDPOINT=$(DYNAMO_ENDPOINT) TABLE_NAME=$(TABLE_NAME) go run ./cmd/devseed

run: ## Run the site against DynamoDB Local
	APP_ENV=dev PORT=$(PORT) DYNAMO_ENDPOINT=$(DYNAMO_ENDPOINT) TABLE_NAME=$(TABLE_NAME) \
	BASE_URL=http://localhost:$(PORT) go run ./cmd/web

test: ## All tests (integration tests need DynamoDB Local: make db)
	KRABBER_TEST_DYNAMO_ENDPOINT=$(DYNAMO_ENDPOINT) go test -race -count=1 ./...

test-unit: ## Tests that don't need DynamoDB Local
	go test -race -count=1 ./...

lint: ## golangci-lint
	golangci-lint run ./...

vet: ## go vet
	go vet ./...

fmt: ## gofmt + terraform fmt
	gofmt -w $$(git ls-files '*.go')
	terraform fmt -recursive infra

vuln: ## govulncheck
	govulncheck ./...

build: ## Linux arm64 binary for Elastic Beanstalk
	$(GO_BUILD) -o bin/application ./cmd/web

bundle: build ## Beanstalk source bundle (bin/application + Procfile + .platform)
	rm -f dist/krabber.zip && mkdir -p dist
	zip -r dist/krabber.zip bin/application Procfile .platform

clean: ## Remove build output
	rm -rf bin dist
