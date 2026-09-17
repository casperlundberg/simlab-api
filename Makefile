BINARY := bin/simlab-api
PKG    := ./...

# What a build is stamped with, so the binary — and every run it records — can
# say which code it is. See internal/buildinfo, and scripts/version.sh for how a
# version is derived.
MODULE   := github.com/casperlundberg/simlab-api
VERSION  ?= $(shell scripts/version.sh 2>/dev/null)
COMMIT   ?= $(shell git rev-parse HEAD 2>/dev/null)
MODIFIED ?= $(shell test -z "$$(git status --porcelain 2>/dev/null)" && echo false || echo true)
LDFLAGS  := -X $(MODULE)/internal/buildinfo.version=$(VERSION) \
            -X $(MODULE)/internal/buildinfo.commit=$(COMMIT) \
            -X $(MODULE)/internal/buildinfo.modified=$(MODIFIED)

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

# -p 1 runs one package at a time. Go tests packages in parallel by default,
# and two suites that both clear the shared database will delete each other's
# rows mid-test. Isolating by schema would be the alternative; serialising a
# suite that takes about a second is the cheaper answer.
.PHONY: test-db
test-db: ## Run every test, including the ones that need a database
	SIMLAB_TEST_DATABASE_URL="$(TEST_DB_URL)" go test -p 1 $(PKG)

.PHONY: test-db-race
test-db-race: ## Run every test with the race detector
	SIMLAB_TEST_DATABASE_URL="$(TEST_DB_URL)" go test -p 1 -race $(PKG)

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
build: ## Build the service binary, stamped with its version and commit
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/simlab-api

.PHONY: run
run: build ## Run the service locally
	./$(BINARY)

.PHONY: version
version: ## Print this checkout's semantic version
	@scripts/version.sh

.PHONY: scripts-test
scripts-test: ## Test the version and release scripts against real repositories
	scripts/version_test.sh
	scripts/release_test.sh

.PHONY: release
release: ## Tag and push a release: make release VERSION=1.3.0 (needs make db-up)
	scripts/release.sh $(VERSION)

.PHONY: check
check: fmt-check vet test-db scripts-test ## Everything CI runs

.PHONY: clean
clean: ## Remove build output
	rm -rf bin dist coverage.out
