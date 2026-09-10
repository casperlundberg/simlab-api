BINARY := bin/simlab-api
PKG    := ./...

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

# A scratch Postgres for the database tests. Nothing here is worth testing
# against a fake: the value is in the schema constraints, the JSONB round-trips
# and the cascade behaviour, and a fake would assert none of them.
TEST_DB_URL ?= postgres://simlab:simlab@127.0.0.1:15432/simlab_test?sslmode=disable
TEST_DB_CONTAINER ?= simlab-test-pg

.PHONY: test
test: ## Run unit tests (no database required)
	go test $(PKG)

.PHONY: test-db
test-db: ## Run every test, including the ones that need a database
	SIMLAB_TEST_DATABASE_URL="$(TEST_DB_URL)" go test $(PKG)

.PHONY: db-up
db-up: ## Start a scratch Postgres for the database tests
	docker run -d --rm --name $(TEST_DB_CONTAINER) \
		-e POSTGRES_PASSWORD=simlab -e POSTGRES_USER=simlab -e POSTGRES_DB=simlab_test \
		-p 15432:5432 postgres:16-alpine
	@until docker exec $(TEST_DB_CONTAINER) pg_isready -U simlab >/dev/null 2>&1; do sleep 0.3; done
	@echo "postgres ready on 127.0.0.1:15432"

.PHONY: db-down
db-down: ## Stop the scratch Postgres
	-docker rm -f $(TEST_DB_CONTAINER)

.PHONY: test-race
test-race: ## Run unit tests with the race detector
	go test -race $(PKG)

.PHONY: cover
cover: ## Run tests and report total coverage
	go test -coverprofile=coverage.out $(PKG)
	go tool cover -func=coverage.out | tail -1

.PHONY: vet
vet: ## Run go vet
	go vet $(PKG)

.PHONY: fmt
fmt: ## Format all Go source
	gofmt -l -w .

.PHONY: fmt-check
fmt-check: ## Fail if any file is not gofmt-clean
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "not gofmt-clean:"; echo "$$out"; exit 1; fi

.PHONY: build
build: ## Build the service binary
	go build -o $(BINARY) ./cmd/simlab-api

.PHONY: run
run: build ## Run the service locally
	./$(BINARY)

.PHONY: check
check: fmt-check vet test-db ## Everything CI runs

.PHONY: clean
clean: ## Remove build output
	rm -rf bin dist coverage.out
