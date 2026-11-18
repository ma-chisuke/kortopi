# The Claude Code harness: the hooks in .claude/hooks, which .claude/settings.json
# registers, give agents the same checks that lefthook gives people.

CHECK_TARGETS += test-hooks

HOOK_GLOBS := ".claude/hooks/*" ".claude/settings.json" "tools/tests/hooks_test.sh" ".gitattributes"

# Feed each hook sample events and check what it blocks and allows.
test-hooks:
	@$(TASK) $@ 'if [ -n "$$($(FILES) $(HOOK_GLOBS))" ]; then tools/tests/hooks_test.sh; fi'
