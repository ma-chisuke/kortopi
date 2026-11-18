# Test coverage. Each language measures its tests into coverage/ with a target
# that it adds to COVERAGE_TARGETS (Go: coverage-go in make/go.mk). octocov
# reads the profiles that .octocov.yml names and reports them. No external
# service is involved.

.PHONY: coverage coverage-report

coverage: ## Measure test coverage into coverage/
	@targets="$(strip $(COVERAGE_TARGETS))"; \
	if [ -n "$$targets" ]; then $(MAKE) --no-print-directory $$targets; fi

# In a terminal, octocov prints the report. On GitHub Actions it also writes the
# job summary, and comments on a pull request when the token may do so.
coverage-report: ## Report the coverage that `make coverage` measured
	@if [ -n "$$(find coverage -name '*.out' -print -quit 2> /dev/null)" ]; then \
		octocov; \
	else \
		echo "coverage-report: no coverage profile in coverage/; nothing to report"; \
	fi
