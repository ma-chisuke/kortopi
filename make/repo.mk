# Checks that apply to every file, and the formats without a home of their own:
# YAML, JSON, TOML, and shell.

FMT_TARGETS += fmt-prettier fmt-toml fmt-shell
CHECK_TARGETS += fmt-check-prettier fmt-check-toml fmt-check-shell \
	lint-yaml lint-toml lint-schema lint-shell \
	lint-typos lint-editorconfig lint-secrets
CHECK_ALL_TARGETS += lint-schema-remote

PRETTIER_GLOBS := "*.yaml" "*.yml" "*.json" "*.jsonc" "*.json5"
SHELL_GLOBS := "*.sh"

fmt-prettier:
	@$(FILES) $(PRETTIER_GLOBS) | xargs -r prettier --write --log-level warn

fmt-check-prettier:
	@$(TASK) $@ '$(FILES) $(PRETTIER_GLOBS) | xargs -r prettier --check --log-level warn'

fmt-toml:
	@$(FILES) "*.toml" | RUST_LOG=warn xargs -r taplo format

# taplo names the file only in a log line, which not every build prints, so
# the finding is written here.
fmt-check-toml:
	@$(TASK) $@ 'code=0; for f in $$($(FILES) "*.toml"); do taplo format --check "$$f" > /dev/null 2>&1 || { echo "$$f:1: [taplo/format] the file is not formatted. Fix: run make fmt"; code=1; }; done; exit $$code'

fmt-shell:
	@$(FILES) $(SHELL_GLOBS) | xargs -r shfmt --write

fmt-check-shell:
	@$(TASK) $@ '$(FILES) $(SHELL_GLOBS) | xargs -r shfmt --diff'

lint-yaml:
	@$(TASK) $@ '$(FILES) "*.yaml" "*.yml" | xargs -r yamllint --strict --format parsable'

lint-toml:
	@$(TASK) $@ '$(FILES) "*.toml" | RUST_LOG=warn xargs -r taplo lint --no-schema'

# Configuration files validated against the schemas bundled with
# check-jsonschema, so this check needs no network.
lint-schema:
	@$(TASK) $@ '\
		$(FILES) "renovate.json" "renovate.json5" ".github/renovate.json" ".github/renovate.json5" | xargs -r check-jsonschema --builtin-schema vendor.renovate'

# Configuration files whose schema is fetched from its publisher.
lint-schema-remote:
	@$(TASK) $@ '\
		$(FILES) "lefthook.yml" | xargs -r check-jsonschema --schemafile https://raw.githubusercontent.com/evilmartians/lefthook/v2.2.2/schema.json; \
		$(FILES) "mise.toml" | xargs -r check-jsonschema --schemafile https://mise.jdx.dev/schema/mise.json; \
		$(FILES) ".claude/settings.json" | xargs -r check-jsonschema --schemafile https://json.schemastore.org/claude-code-settings.json'

lint-shell:
	@$(TASK) $@ '$(FILES) $(SHELL_GLOBS) | xargs -r shellcheck --format=gcc --external-sources'

lint-typos:
	@$(TASK) $@ '$(FILES) | xargs -r typos --format brief --force-exclude'

lint-editorconfig:
	@$(TASK) $@ '$(FILES) | xargs -r editorconfig-checker -no-color -format gcc'

# Secrets: the staged changes before a commit, otherwise the working tree.
# The secret itself is never printed.
GITLEAKS_REPORT := --no-banner --redact --log-level error --report-format template --report-template tools/gitleaks/findings.tpl --report-path -
lint-secrets:
ifeq ($(CHECK_SCOPE),staged)
	@$(TASK) $@ 'gitleaks git --staged $(GITLEAKS_REPORT)'
else
	@$(TASK) $@ 'gitleaks dir $(GITLEAKS_REPORT) .'
endif
