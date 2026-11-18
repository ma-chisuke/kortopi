# Markdown: structure, links, English prose (Vale), and Japanese prose (textlint).
# English lives in foo.md and its Japanese translation in foo.ja.md.
# Decision records (ADR-0001) and translations (ADR-0003) are checked by the
# Go tool in tools/cmd/docs.

FMT_TARGETS += fmt-md
GEN_TARGETS += gen-adr-index
CHECK_TARGETS += lint-md lint-prose-en lint-prose-ja lint-links lint-adr lint-i18n
CHECK_ALL_TARGETS += lint-links-remote

MD_EN := "*.md" ":(exclude)*.ja.md"
MD_JA := "*.ja.md"

DOCS := cd tools && go run ./cmd/docs -root "$(CURDIR)"
# A translation and its English file are a pair across the whole repository,
# so this list ignores CHECK_SCOPE and CHECK_FILES.
ALL_MD := env -u CHECK_SCOPE -u CHECK_FILES $(FILES) "*.md"

fmt-md:
	@$(FILES) "*.md" | xargs -r markdownlint-cli2 --fix > /dev/null 2>&1 || true

lint-md:
	@$(TASK) $@ '$(FILES) "*.md" | xargs -r markdownlint-cli2 2>&1 | { grep -Ev "^(markdownlint-cli2 v|Finding: |Linting: |Summary: )" || true; }'

lint-prose-en:
	@$(TASK) $@ '$(FILES) $(MD_EN) | xargs -r vale --output=line --no-wrap'

lint-prose-ja:
	@$(TASK) $@ '$(FILES) $(MD_JA) | xargs -r textlint --format unix | sed "s|$(CURDIR)/||"'

# Links between files in the repository, including heading anchors. No network.
lint-links:
	@$(TASK) $@ '$(FILES) "*.md" | xargs -r lychee --offline --include-fragments --no-progress --format compact'

# Every link, including the ones that leave the repository.
lint-links-remote:
	@$(TASK) $@ '$(FILES) "*.md" | xargs -r lychee --include-fragments --no-progress --format compact'

# Records: file names, front matter, titles, sections, and docs/adr/README.md.
lint-adr:
	@$(TASK) $@ '$(DOCS) adr check'

# Every English Markdown file has a Japanese translation that matches it.
lint-i18n:
	@$(TASK) $@ '$(ALL_MD) | ($(DOCS) i18n check)'

gen-adr-index:
	@$(DOCS) adr index

.PHONY: i18n-stamp
i18n-stamp: ## Mark each translation as matching its English file now
	@$(ALL_MD) | ($(DOCS) i18n stamp)
