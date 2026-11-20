# Trivy: vulnerable dependencies and dependency licenses. Settings live in
# trivy.yaml. mise installs Trivy itself with a pinned version and checksum;
# see ADR-0005.

CHECK_ALL_TARGETS += scan-deps

export TRIVY_DISABLE_TELEMETRY := true
export TRIVY_SKIP_VERSION_CHECK := true

# Trivy reads public registries only. An empty Docker configuration keeps it
# from asking a credential helper, which can block without a terminal.
TRIVY_ENV := DOCKER_CONFIG=$(CURDIR)/.cache/trivy-docker-config
TRIVY_REPORT := --quiet --exit-code 1 --format template --template @tools/trivy/findings.tpl
# Directories that hold no source of this repository: dependencies, caches,
# and the release archives (dist/).
TRIVY_SKIP := --skip-dirs .venv --skip-dirs .cache --skip-dirs .claude/worktrees --skip-dirs tmp --skip-dirs dist

# Dependencies in every lockfile. Downloads the vulnerability database, so it
# runs in check-all.
#   Vulnerabilities: every dependency, including development tools, because
#     they run on laptops and in CI.
#   Licenses: only the licenses that trivy.yaml forbids fail (Trivy rates them
#     CRITICAL), and only in what ships in the binary, so development
#     dependencies and the tools/ module are left out. Trivy reads the license
#     of an npm package from node_modules and that of a Go module from the
#     module cache, so node_modules is not skipped and the Go modules are
#     downloaded.
scan-deps:
	@$(TASK) $@ 'mkdir -p .cache/trivy-docker-config; \
		for m in $(GO_MODULES); do (cd "$$m" && go mod download); done; \
		$(TRIVY_ENV) trivy fs $(TRIVY_REPORT) --scanners vuln --include-dev-deps $(TRIVY_SKIP) .; \
		$(TRIVY_ENV) trivy fs $(TRIVY_REPORT) --scanners license --severity CRITICAL $(TRIVY_SKIP) --skip-dirs tools .'
