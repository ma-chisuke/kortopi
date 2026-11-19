---
status: accepted
date: 2026-11-19
---

# ADR-0002: Prefer deterministic checks in the harness for coding agents, and keep the instructions minimal

## Context and Problem Statement

Coding agents write much of the code and many of the documents of kortopi.
An agent can break a rule that exists only in prose: it may not read the rule, may lose it during a long task, or may misread it.
Nothing then tells the agent, or anyone else, that the rule was broken.

The usual answer is an instruction file that describes the repository: its architecture, its conventions, and every rule.
An agent reads such a file on every task, so the file costs tokens every time. It goes stale as the code changes, and it gives no signal when an agent breaks a rule in it.
A 2026 study from ETH Zurich, "Evaluating AGENTS.md: Are Repository-Level Context Files Helpful for Coding Agents?", reported that repository overview files did not raise the rate at which agents completed tasks, and raised the cost by more than 20 percent.

kortopi is also a public project.
Its contributors use different agent products, or none, and the rules must hold for all of them alike.

How do we make agents follow the rules of this repository, and what belongs in the instructions that every agent reads?

## Decision Drivers

- A rule that matters holds even when an agent did not read it or did not understand it.
- An agent can correct its own mistake from the output of a check, without a person.
- Every agent reads the instructions on every task, so each line must be worth its cost.
- The same rules hold for every agent product and for people.
- A change to the instructions rests on a failure that was seen, not on a guess.

## Considered Options

- Deterministic checks first, and minimal instructions that grow only from observed failures
- A long instruction file that describes the architecture and every rule
- No harness, with agents working from the code and the README alone
- A separate rule file for each agent product

## Decision Outcome

Chosen option: "Deterministic checks first, and minimal instructions that grow only from observed failures", because a check holds a rule whatever the agent read, and a short instruction file costs little and stays true.

The rules are as follows.

- A rule that matters is enforced by a check in `make check` or `make check-all` (ADR-0004). A finding is one line that gives the file, the line, and the rule. The checks that this repository writes itself also say how to fix the problem and name the record that holds the reason.
- `AGENTS.md` holds only the commands, where things go, the instruction to run `make check` before finishing, and the rules that differ from common defaults. Reasons live in the records, and procedures live in skills and guides.
- `CLAUDE.md` only imports `AGENTS.md`, so every agent reads the same file.
- An instruction is added to `AGENTS.md` only after a failure has been seen that no check can catch. There is no suite that evaluates agents against the instructions, so review decides whether a new instruction is needed and whether a check could replace it.

The harness has the following parts.

- Hooks for Claude Code in `.claude/hooks/`, registered in `.claude/settings.json`. After an agent edits a file, a hook formats it. Before an edit, a hook blocks changes to the generated, vendored, and lock files that `.gitattributes` marks, and says what to run instead.
- Other hooks block reading `.env` files, force pushes, pushes to `main`, and commands that skip the Git hooks, and keep the reviewer subagents read-only. At the end of a turn, a hook runs `make check` on the changed files and hands the findings back to the agent; after three failures in a row, it lets the turn end and says that the checks still fail.
- Permissions in `.claude/settings.json` let `make` and read-only Git commands run without asking, and deny reading `.env` files, force pushes, and pushes to `main`, as a second layer behind the hooks.
- Skills for recurring procedures, in `.claude/skills/`: `write-adr` writes a record, `sync-translation` brings a translation up to date, and `japanese-tech-writing` holds the norm for Japanese prose (ADR-0003).
- Subagents in `.claude/agents/`: `design-reviewer` reviews a change against the records, and `translation-reviewer` compares a translation with its source; both only read. `implementer` builds one large piece of work from a written brief and verifies it before it reports.
- lefthook gives people the same checks (`lefthook.yml`): `make fmt` and `make check` on the staged files before a commit, the check of the commit message, and `make check` before a push.

### Consequences

- Good, because a rule holds whether or not the agent read about it, and the agent learns the rule at the moment it breaks it.
- Good, because people, agents, and CI share one `make check`, so no agent product needs rules of its own.
- Good, because `AGENTS.md` stays short, so it costs little on every task and has little that can go stale.
- Bad, because some rules cannot be checked, such as naming, whether a test tests the right thing, or whether kortopi behaves as its records say. They depend on review, and on the subagent that reviews.
- Bad, because few findings name their record: those of `lint-adr`, `lint-i18n`, and `lint-agents-md`, and the message of the time budget, do. The other tools give the rule and sometimes a fix or a link, and the agent has to find the reason from the name of the rule.
- Bad, because nothing measures whether the short `AGENTS.md` is enough. The choice rests on the study and on what review sees.
- Bad, because the hooks work only in Claude Code. Other agents get the checks through `make check`, lefthook, and CI alone.
- Bad, because the hooks guard against mistakes, not against an agent that sets out to get around them: they split a shell command with a simple parser, and they are not a sandbox.

### Confirmation

- `make check` is the gate. People, agents, and CI run it, and the hooks run it at the end of a turn.
- `make check` runs `lint-agents-md`, which fails when an `AGENTS.md` or its translation is longer than 60 lines.
- `make check` runs `test-hooks`, which feeds each hook sample events and checks what it blocks and what it allows (`tools/tests/hooks_test.sh`).
- `make check-all` validates `.claude/settings.json` against its published schema (`lint-schema-remote`).
- Review confirms that a new instruction in `AGENTS.md` answers a failure that was seen and that no check can replace.

## Pros and Cons of the Options

### Deterministic checks first, and minimal instructions that grow only from observed failures

- Good, because a check is the same for every agent, every model, and every person.
- Good, because the output of a failed check tells the agent what to fix, so it can fix the problem without a person.
- Bad, because writing a check, and keeping its output useful, costs more than writing a sentence.
- Bad, because an agent may miss context that a long file would have given it, and spend a turn on a failed check to learn it.

### A long instruction file that describes the architecture and every rule

- Good, because everything is in one place, and a new agent or person reads it once.
- Bad, because every agent reads it on every task, and the study cited in this record found that such overviews raised the cost without raising success.
- Bad, because nothing tells the agent when it breaks a rule in the file, and nothing tells anyone when the file goes stale.

### No harness, with agents working from the code and the README alone

- Good, because there is nothing to build or maintain.
- Bad, because each agent rediscovers the commands and the rules, breaks the ones it does not find, and leaves those to the reviewer.

### A separate rule file for each agent product

- Good, because each file can use the features of its product.
- Bad, because the files drift apart, and a rule changed in one file stays unchanged in the others.

## More Information

- [Evaluating AGENTS.md: Are Repository-Level Context Files Helpful for Coding Agents?](https://arxiv.org/abs/2602.11988), Thibaud Gloaguen and others, ETH Zurich and LogicStar.ai, 2026.
- [AGENTS.md](https://agents.md/)
- The tools behind `make check` are chosen in ADR-0004, and the languages of the documents in ADR-0003.
