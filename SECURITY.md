# Security policy

## Supported versions

Fixes go into the latest release of kortopi and into `main`.
Older releases get no fixes.

| Version | Supported |
|---|---|
| Latest release | Yes |
| Older releases | No |

## Report a vulnerability

Report a vulnerability privately through GitHub, never in a public issue, pull request, or discussion:

1. Open the **Security** tab of this repository.
2. Select **Report a vulnerability**.
3. Describe the problem: what an attacker can do, the declaration files, flags, or workflows involved, and the steps to reproduce it.

GitHub keeps the report visible only to you and to the maintainers.
One person maintains kortopi in their spare time. The maintainer aims to answer in the report within two weeks, agrees on a fix and a date for disclosure with you, and credits you in the published advisory unless you ask otherwise.
GitHub explains the steps in [Privately reporting a security vulnerability](https://docs.github.com/en/code-security/security-advisories/guidance-on-reporting-and-writing-information-about-vulnerabilities/privately-reporting-a-security-vulnerability).

## Scope

This policy covers what this repository contains: the code of kortopi, its released binaries, and its workflows, scripts, and configuration.

kortopi runs with the privileges of the database role that you give it, and it runs the SQL `INSERT` statements of your declaration files in your database.
Treat a declaration file like any other code that you deploy: a person who can change it can change the rows of the tables that kortopi manages.
That is how the tool works, not a vulnerability. A way to make kortopi change a table, a row, or a column that the declaration and the settings do not cover is a vulnerability.

A vulnerability in a tool or a dependency belongs to its maintainers. If kortopi uses the affected version, report it here as well, so that kortopi can move to a fixed one.
