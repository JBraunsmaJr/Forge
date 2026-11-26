# Forge developer tasks. Run `make help` for the list.

# The CLI binary is forge.exe on Windows and forge everywhere else.
BINARY := forge
ifeq ($(OS),Windows_NT)
BINARY := forge.exe
endif

UI_DIR  := ui
UI_DIST := internal/scheduler/web/dist

.DEFAULT_GOAL := help

.PHONY: help ui build-go build dev-ui vet test test-race test-security check baseline clean

help: ## List the available targets
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*## "} {printf "  %-12s %s\n", $$1, $$2}'

ui: ## Install UI dependencies and build the Web UI
	cd $(UI_DIR) && npm install && npm run build

build-go: ## Build the forge binary (embeds the Web UI if it has been built)
	go build -o $(BINARY) ./cmd/forge

build: ui build-go ## Build the Web UI, then the forge binary

dev-ui: ## Run the Web UI dev server with hot reload
	cd $(UI_DIR) && npm run dev

vet: ## Run go vet on every package
	go vet ./...

# The integration suite needs Docker and a built image, so the default test
# targets skip it. See .forge/scripts/integration-tests.sh for the full run.
test: export INTEGRATION_SKIP = 1
test: ## Run the unit tests
	go test -count=1 ./...

test-race: export INTEGRATION_SKIP = 1
test-race: ## Run the unit tests with the race detector
	go test -count=1 -race ./...

# The security suite guards the gaps listed in docs/Threat-Model.md. It is
# part of `make test`; this target runs it on its own, with test names shown.
test-security: ## Run only the security regression suite
	go test -count=1 -v -run '^TestSecurity' ./internal/scheduler/... ./internal/proxy/...

check: vet test ## Run the checks expected to pass before a commit

baseline: ## Record build, vet, test, race, and UI results in .baseline/report.md
	./scripts/dev/baseline.sh

clean: ## Remove build output
	rm -f forge forge.exe
	rm -rf $(UI_DIST)
