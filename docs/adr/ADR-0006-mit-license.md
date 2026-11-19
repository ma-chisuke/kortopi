---
status: accepted
date: 2026-11-19
---

# ADR-0006: License kortopi under MIT

## Context and Problem Statement

kortopi is a tool that teams put in their deploy pipelines and run against their production databases.
Before a company lets such a tool into a pipeline, or into an image that it builds, someone checks the license.
A license that legal reviewers and license scanners already know lets that check end quickly. An unusual license, or one with conditions on the code around the tool, can stop the adoption.

The license also decides which dependencies kortopi may compile into its binary, because the binary carries their licenses too.

Which license makes kortopi easy to adopt, and what does it require of the dependencies?

## Decision Drivers

- A company can run kortopi, and redistribute it in its own images, without a legal review beyond recognizing the license.
- Legal reviewers and license scanners recognize the license: the OSI has approved it, and it has an SPDX identifier.
- The text is short and familiar.
- The license sets no condition on the code, the pipelines, or the databases of the people who run kortopi.

## Considered Options

- MIT
- MIT-0
- Apache-2.0
- A copyleft license, such as GPL-3.0 or AGPL-3.0

## Decision Outcome

Chosen option: "MIT", because it is short, widely understood, and approved by the OSI, and it asks of the people who copy kortopi only that they keep its notice.

The rules are as follows.

- `LICENSE` holds the text of the MIT License, and the `package.json` of the repository tooling declares `"license": "MIT"`.
- A dependency under a strong copyleft license (the GPL and AGPL families, among others) or under a source-available license (SSPL, BUSL, the Commons Clause, and the Creative Commons licenses with NonCommercial) must not become part of the binary. `trivy.yaml` lists these licenses as forbidden, and the license scan fails on them only.
- A weak copyleft license, such as LGPL or MPL, does not fail the scan, because it covers the library itself and not the code that uses it.
- Third-party files kept in the repository keep their own licenses, stated in or next to the files. The Google style for Vale in `.vale/styles/Google/` is under the MIT License, and the Japanese writing norm in `.claude/skills/japanese-tech-writing/` is under the Unlicense.

### Consequences

- Good, because a legal reviewer knows the MIT License at a glance, and scanners recognize its SPDX identifier, `MIT`.
- Good, because anyone may run, change, and redistribute kortopi, in any company and in any product.
- Bad, because the MIT License has no explicit patent grant, unlike Apache-2.0.
- Bad, because anyone may also ship a changed kortopi without sharing the changes.
- Bad, because whoever redistributes the binary must keep the notice of kortopi and the notices of the dependencies compiled into it. How a release collects those notices is settled with the release, which a later record covers (ADR-0005).
- Bad, because a library under a weak copyleft license passes the scan, yet it still brings conditions of its own when it is compiled into the binary. Review weighs them when such a library is added.
- Bad, because a dependency under a forbidden license cannot be used: using one needs a new record that supersedes this one, and a change to `trivy.yaml`.

### Confirmation

- The `LICENSE` file holds the text of the license, and review confirms that `LICENSE` and `package.json` name MIT.
- `make check-all` runs `scan-deps`, which fails when Trivy finds a dependency under a license that `trivy.yaml` forbids. The license scan covers what ships in the binary: development dependencies and the `tools/` module are left out.
- `trivy.yaml` sets `license.full: false`, so Trivy reads the license that each package declares, not the license text of each file. A file copied into the repository by hand is out of its reach, and review confirms that the license of such a file is stated in or next to it.

## Pros and Cons of the Options

### MIT

- Good, because it is the most widely known permissive license, and its text is short.
- Bad, because it has no explicit patent grant.
- Bad, because every copy must carry the copyright notice and the permission notice.

### MIT-0

- Good, because it is the MIT License without the duty to keep the notice.
- Bad, because that freedom matters for code that people copy into their own products, such as sample code, and kortopi is a tool that people run, not code that they copy.
- Bad, because it is less known than MIT, so some reviewers look it up.
- Bad, because it has no patent grant either.

### Apache-2.0

- Good, because it grants a license to the patents of contributors, and ends that license for anyone who sues over those patents.
- Bad, because it is longer and has more conditions: a copy must include the license, keep the notices, mark the files it changed, and carry the `NOTICE` file if there is one.

### A copyleft license, such as GPL-3.0 or AGPL-3.0

- Good, because the changes in a version that someone distributes stay open, and under AGPL-3.0 also those in a version offered over a network.
- Bad, because company policies commonly forbid such licenses, or send them to a legal review, and kortopi is meant to sit in the deploy pipeline of any company.

## More Information

- [MIT on the SPDX License List](https://spdx.org/licenses/MIT.html)
- The license scan and the other defenses of the supply chain are in ADR-0005.
