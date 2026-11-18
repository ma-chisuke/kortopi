# GitHub Actions workflows: syntax, security, policy, and pinning.

CHECK_TARGETS += lint-actions lint-actions-security lint-actions-policy lint-actions-pins

WORKFLOW_GLOBS := ".github/workflows/*.yml" ".github/workflows/*.yaml"
# Run a command when workflow files are in scope.
if-workflows = if [ -n "$$($(FILES) $(WORKFLOW_GLOBS))" ]; then $(1); fi

lint-actions:
	@$(TASK) $@ '$(FILES) $(WORKFLOW_GLOBS) | xargs -r actionlint -oneline'

lint-actions-security:
	@$(TASK) $@ '$(call if-workflows,zizmor --offline --no-progress --quiet --format plain .github/)'

# Least privilege and a timeout on every job.
lint-actions-policy:
	@$(TASK) $@ '$(call if-workflows,ghalint --log-color never run)'

# Every action is pinned to a full commit SHA. Without the GitHub API, so the
# check stays offline; `pinact run` finds the SHA of an unpinned action.
lint-actions-pins:
	@$(TASK) $@ '$(call if-workflows,pinact run --fix=false --no-api)'
