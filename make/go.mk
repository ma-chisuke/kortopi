# Go: the root module (kortopi itself) and tools/ (the checks of this
# repository) are each checked as their own module.
# golangci-lint runs the formatters (gofmt, goimports) and the linters,
# including go vet, so each concern has one tool. See .golangci.yml.

SETUP_TARGETS += setup-go
FMT_TARGETS += fmt-go
CHECK_TARGETS += fmt-check-go lint-go test-go
CHECK_ALL_TARGETS += vuln-go test-go-integration

GO_MODULES := . $(patsubst %/go.mod,%,$(wildcard */go.mod))
GO_GLOBS := "*.go" "*go.mod" "*go.sum" ".golangci.yml"
GOLANGCI := golangci-lint --config $(CURDIR)/.golangci.yml

# Run a command in every Go module that has Go files, when Go files are in scope.
go-each = if [ -n "$$($(FILES) $(GO_GLOBS))" ]; then for m in $(GO_MODULES); do if [ -n "$$(find "$$m" -name "*.go" -print -quit)" ]; then (cd "$$m" && $(1)); fi; done; fi

# Download the modules and compile once, so that `make check` needs no network
# and its first run is as fast as the next ones. The binaries go to the cache:
# without -o, `go build ./...` writes one into the module when the pattern
# matches a single command.
setup-go:
	@mkdir -p .cache/build
	@for m in $(GO_MODULES); do (cd "$$m" && go mod download && go build -o "$(CURDIR)/.cache/build/" ./... && go test -race -run "^$$" ./... > /dev/null); done

# The SQL parser runs as WebAssembly (ADR-0015). With cgo on, the package of
# its node types would also compile and link the C parser, which kortopi never
# calls, so the binary is built without cgo.
.PHONY: build
build: ## Build the kortopi binary into bin/
	@CGO_ENABLED=0 go build -trimpath -o bin/kortopi ./cmd/kortopi

fmt-go:
	@$(call go-each,$(GOLANGCI) fmt ./...)

fmt-check-go:
	@$(TASK) $@ '$(call go-each,$(GOLANGCI) fmt --diff ./...)'

# golangci-lint lets one instance run on a machine at a time and fails when
# another one holds its lock. --allow-serial-runners makes it wait instead, so
# a second check on the same machine (a hook, an agent, the self-test) does
# not fail this one.
lint-go:
	@$(TASK) $@ '$(call go-each,$(GOLANGCI) run --allow-serial-runners ./...)'

test-go:
	@$(TASK) $@ '$(call go-each,go test -race -shuffle=on ./...)'

# The tests with the build tag integration start PostgreSQL 15 to 18 in
# containers, so they need Docker, and they fail without it (ADR-0019). They
# take a minute or more, so they run in check-all. There, other checks run at
# the same time and slow them down, so the time limit of `go test` is raised.
# The packages run one at a time (-p 1): each package with integration tests
# starts four servers of its own and runs its tests in parallel, and two such
# packages at once made a machine of 8 GB swap until runs of the command took
# longer than the bounds of the tests (ADR-0052).
GO_INTEGRATION := -tags integration -timeout 20m -p 1

test-go-integration:
	@$(TASK) $@ '$(call go-each,go test $(GO_INTEGRATION) -race -shuffle=on ./...)'

# Known vulnerabilities in code that the module actually calls.
vuln-go:
	@$(TASK) $@ '$(call go-each,go tool -modfile=$(CURDIR)/tools/go.mod govulncheck ./...)'

# Coverage: one profile per module in coverage/go/, merged into coverage/go.out,
# which .octocov.yml reads. `make coverage` runs this target (make/coverage.mk).
# It includes the integration tests, which cover the code that talks to
# PostgreSQL, so it needs Docker.
COVERAGE_TARGETS += coverage-go
.PHONY: coverage-go

coverage-go:
	@rm -rf coverage/go coverage/go.out && mkdir -p coverage/go
	@$(call go-each,go test $(GO_INTEGRATION) -race -shuffle=on -covermode=atomic -coverprofile="$(CURDIR)/coverage/go/$$(basename "$$PWD").out" ./...)
	@if [ -n "$$(find coverage/go -name '*.out' -print -quit)" ]; then \
		{ echo "mode: atomic"; for f in coverage/go/*.out; do tail -n +2 "$$f"; done; } > coverage/go.out; \
	fi
