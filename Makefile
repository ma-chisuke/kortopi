# The single entry point for people, agents, and CI. See AGENTS.md.
#
#   make setup       install the pinned tools and dependencies
#   make fmt         format every file
#   make gen         regenerate generated files
#   make check       fast checks; run this before you finish a change
#   make check-all   fast and slow checks; CI runs this
#
# Narrow any target to a set of files with CHECK_SCOPE=staged|changed or
# CHECK_FILES=<paths>. Add VERBOSE=1 to see the output of passing checks.
#
# Each area keeps its targets in make/<area>.mk and registers them in
# SETUP_TARGETS, FMT_TARGETS, GEN_TARGETS, CHECK_TARGETS, or CHECK_ALL_TARGETS.

SHELL := bash
.DEFAULT_GOAL := help

ifeq ($(shell command -v mise 2> /dev/null),)
$(error mise is not installed. Install it from https://mise.jdx.dev/installing-mise.html and run `make setup`)
endif

# Tools come from mise (mise.toml), pnpm (package.json), and uv (pyproject.toml).
export PATH := $(CURDIR)/node_modules/.bin:$(CURDIR)/.venv/bin:$(shell mise bin-paths 2> /dev/null | tr '\n' ':')$(PATH)
# Never let Go download a toolchain other than the one mise installed.
export GOTOOLCHAIN := local
# Fail when a locked dependency would change during a check.
export GOFLAGS := -mod=readonly

# Plain output: one finding per line, no colors.
export NO_COLOR := 1

# Upper bound for `make check`, in seconds. Slow checks belong in check-all.
CHECK_BUDGET ?= 60
JOBS ?= 8

TASK := tools/scripts/task.sh
FILES := tools/scripts/files.sh

SETUP_TARGETS :=
FMT_TARGETS :=
GEN_TARGETS :=
CHECK_TARGETS :=
CHECK_ALL_TARGETS :=

include make/*.mk

.PHONY: help
help: ## Show the main targets
	@grep -hE '^[a-z][a-z0-9-]*:.*## ' $(MAKEFILE_LIST) | sort | awk -F ':.*## ' '{printf "  %-16s %s\n", $$1, $$2}'

.PHONY: setup
setup: ## Install the pinned tools and dependencies, and the Git hooks
	mise trust --quiet
	mise install
	@$(MAKE) --no-print-directory setup-deps

# A second make, because PATH above is computed before mise trusts this
# directory and installs the tools; in a fresh clone it holds none of them.
.PHONY: setup-deps
setup-deps:
	pnpm install --frozen-lockfile
	uv sync --locked
	lefthook install
ifneq ($(strip $(SETUP_TARGETS)),)
	@$(MAKE) --no-print-directory $(SETUP_TARGETS)
endif

.PHONY: fmt
fmt: ## Format every file in scope
	@$(MAKE) --no-print-directory --keep-going $(FMT_TARGETS)

.PHONY: gen
gen: ## Regenerate generated files
ifneq ($(strip $(GEN_TARGETS)),)
	@$(MAKE) --no-print-directory --keep-going $(GEN_TARGETS)
endif

.PHONY: check
check: ## Fast checks. Run before you finish a change
	@MAKE="$(MAKE)" JOBS=$(JOBS) tools/scripts/check.sh check $(CHECK_BUDGET) $(CHECK_TARGETS)

.PHONY: check-all
check-all: ## Fast and slow checks. CI runs this
	@MAKE="$(MAKE)" JOBS=$(JOBS) tools/scripts/check.sh check-all 0 $(CHECK_TARGETS) $(CHECK_ALL_TARGETS)

.PHONY: tools-lock
tools-lock: ## Refresh mise.lock after editing mise.toml
	MISE_LOCKED=0 mise lock

.PHONY: clean
clean: ## Remove caches and build output
	rm -rf .cache coverage bin

.PHONY: $(SETUP_TARGETS) $(FMT_TARGETS) $(GEN_TARGETS) $(CHECK_TARGETS) $(CHECK_ALL_TARGETS)
