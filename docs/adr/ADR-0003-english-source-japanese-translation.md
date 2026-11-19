---
status: accepted
date: 2026-11-19
---

# ADR-0003: Write documents in English, with a Japanese translation next to each

## Context and Problem Statement

kortopi is a public tool, and most of the people who use it, report issues about it, or compare it with other tools read English.
Its maintainer reviews every document, and reviews in Japanese.
The documents of kortopi tell people what it does to the rows of their databases, so a mistake that review misses can lead a user to a wrong apply.

Documents in two languages bring a risk of their own: two texts that disagree, with no way to tell which one is right or which one is out of date.

Which language is the source, where does the other language live, and how does a reader know that the two texts still agree?

## Decision Drivers

- The users of a public tool can read every document.
- The maintainer can review every document in Japanese.
- When the two texts differ, one of them is the authority.
- A check, not a reader, finds a translation that is out of date.
- Code, the messages of kortopi, and instructions for tools have one language, so that nothing there needs a translation.

## Considered Options

- English as the source, with a Japanese translation next to each document
- English only
- Japanese only
- Japanese as the source, with an English translation next to each document
- A separate tree for the translations (`docs/ja/`)
- Machine translation on demand, with nothing committed

## Decision Outcome

Chosen option: "English as the source, with a Japanese translation next to each document", because users read the source rather than a translation while the maintainer reviews in Japanese, the documents share the language of the code, and a check can tell when the two texts drift apart.

The rules are those of the [translation guide](../guides/translation.md).

- `foo.md` is English and is the source. `foo.ja.md` is its Japanese translation and sits in the same directory.
- Both files change in the same commit.
- The last line of a translation records the SHA-256 hash of the English file that it translates. `make i18n-stamp` writes that line.
- Code, comments, commit messages, and the messages that kortopi prints are in English only.
- Files under `.claude/` and `.github/` are instructions for tools, and are in English only. `.i18nignore` lists them, together with the other paths whose Markdown needs no translation, such as `CLAUDE.md`, which only imports `AGENTS.md`.
- Third-party files keep the language in which they were published, such as the Japanese writing norm in `.claude/skills/japanese-tech-writing/`.
- Vale checks English prose against the Google developer documentation style, and textlint checks Japanese prose. Translations follow the Japanese writing norm and the table of terms in the guide.

### Consequences

- Good, because a reader of either language reads the whole document, not a summary.
- Good, because the English text decides when the two texts disagree.
- Good, because the two files sit side by side: relative links work the same in both, and a change to both shows in one diff.
- Good, because tools and agents read instructions in English only, so no instruction has two versions that can disagree.
- Bad, because every change to a document is written twice, and its review takes longer.
- Bad, because a contributor who does not write Japanese cannot update a translation alone, and needs the skill `sync-translation` or the help of a maintainer. `make i18n-stamp` would make `make check` pass, but the translation would stay out of date.
- Bad, because the hash proves only that someone ran `make i18n-stamp` after the source changed, not that the translation was updated or that the meaning matches. The subagent `translation-reviewer` compares the meaning, and it can miss a difference.
- Bad, because the maintainer reviews the translation rather than the source, so a mistake that exists only in the English text can pass review.
- Bad, because a user who reads only Japanese gets the documents in Japanese, but the messages of kortopi in English.

### Confirmation

- `make check` runs `lint-i18n`, which fails when a document has no translation, when a translation has no English file, when the last line of a translation is missing or names another file, or when the hash in it differs from the hash of the English file. Each finding names this record.
- `make check` runs Vale on English Markdown (`lint-prose-en`) and textlint on Japanese Markdown (`lint-prose-ja`).
- The subagent `translation-reviewer` compares the meaning of a translation with its source, and review covers what it misses.
- Review confirms that code, comments, commit messages, and the messages of kortopi are in English. No check enforces the language.

## Pros and Cons of the Options

### English as the source, with a Japanese translation next to each document

- Good, because both readers get the whole text.
- Good, because it is clear which text is the authority.
- Bad, because the work of writing doubles.

### English only

- Good, because there is one text and nothing to keep in step.
- Bad, because the maintainer cannot review in Japanese.

### Japanese only

- Good, because the text that the maintainer reviews is the text that users read.
- Bad, because most users of a public tool cannot read it.

### Japanese as the source, with an English translation next to each document

- Good, because the maintainer writes and reviews the text that is the authority.
- Bad, because users read a translation, and wherever the translation is weak, the public text is weak.
- Bad, because code, comments, and commit messages are in English anyway, so the documents would be in a different language from the code they describe.

### A separate tree for the translations (`docs/ja/`)

- Good, because the English tree holds nothing but English.
- Bad, because a translation lives far from its source, so a change to one is easy to make without the other, and relative links differ between the two trees.
- Bad, because documents outside `docs/`, such as the README and `AGENTS.md`, would need a second scheme.

### Machine translation on demand, with nothing committed

- Good, because nothing is written twice.
- Bad, because nobody reviews what the reader reads, and the terms change from one run to the next.
- Bad, because the maintainer's review of a translation leaves no trace in the repository.

## More Information

- [Translate a document](../guides/translation.md) holds the rules and the table of terms.
- [Vale](https://vale.sh/), the [Google developer documentation style guide](https://developers.google.com/style), and [textlint](https://textlint.org/)
- The skill `sync-translation` and the subagent `translation-reviewer` are part of the harness in ADR-0002.
