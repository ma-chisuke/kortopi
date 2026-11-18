# The self-test of the checks: each target in CHECK_TARGETS must fail on a
# known-bad input and pass on a known-good one. See tools/selftest/selftest.sh.

CHECK_ALL_TARGETS += selftest

# In `make check-all`, one status line like every other check; on its own,
# one line per case.
selftest: ## Prove that every check catches a bad input and passes a good one
ifdef CHECK_STATE_DIR
	@$(TASK) $@ 'MAKE="$(MAKE)" JOBS=$(JOBS) tools/selftest/selftest.sh $(CHECK_TARGETS)'
else
	@MAKE="$(MAKE)" JOBS=$(JOBS) tools/selftest/selftest.sh $(CHECK_TARGETS)
endif
