# AGENTS.md stays short. It holds commands, places, and rules; reasons belong in
# decision records and procedures in skills and guides (ADR-0002).

CHECK_TARGETS += lint-agents-md

AGENTS_MD_MAX_LINES := 60

lint-agents-md:
	@$(TASK) $@ '$(FILES) "*AGENTS.md" "*AGENTS.ja.md" | while IFS= read -r f; do n=$$(wc -l < "$$f" | tr -d " "); if [ "$$n" -gt $(AGENTS_MD_MAX_LINES) ]; then echo "$$f:$$n: [agents-md/too-long] $$n lines, and the limit is $(AGENTS_MD_MAX_LINES). Fix: move reasons to a decision record and procedures to a skill or a guide. See ADR-0002."; exit 1; fi; done'
