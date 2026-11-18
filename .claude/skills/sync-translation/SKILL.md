---
name: sync-translation
description: Bring a Japanese translation foo.ja.md up to date after its English source foo.md changed. Use after you edit an English document, or when lint-i18n reports a missing or stale translation.
---

# Sync a translation

English is the source. `foo.ja.md` translates `foo.md`, and its last line records the hash of the English version that it translates.

## Steps

1. Find what changed in the English file since the translation was last brought up to date.
   Take the last commit that changed the translation, `git log -1 --format=%H -- foo.ja.md`, and read `git diff <commit> -- foo.md` together with any uncommitted change to `foo.md`.
   For a new translation, translate the whole file.
2. Read the rules and the term table in `docs/guides/translation.md`, and the skill `japanese-tech-writing`.
3. Change only the parts of `foo.ja.md` that match the changed English text. Keep the structure parallel: the same headings, lists, tables, and links, in the same order.
   Keep code, commands, paths, and identifiers as they are.
4. Translate the meaning, sentence by sentence: add nothing and leave nothing out.
   If a term is missing from the term table, add it to the table in `docs/guides/translation.md` and `docs/guides/translation.ja.md` first.
5. Run `make i18n-stamp` to update the hash line. Run it only after the text is up to date.
6. For a second look at the meaning, ask the subagent `translation-reviewer` to compare the two files.
7. Run `make check`.
