# W-093 — Documentation for people on the site, for agents in help, AGENTS and the site's Markdown

Date: 2026-11-20. Amends [W-077](W-077-docs-site-for-users-only.md).

## Context

The documentation grew with the features of 0.6.0: the guide, about 6000 lines of design, a help
text of about 1500 lines and an AGENTS block that adopted projects carry. People read the site;
agents read the AGENTS block, `jst <command> --help` and the design. Both found the same facts in
several places, worded differently.

## The owner's words

> Since the site is built from our documentation, in a separate session we need to do humanisation,
> deduplication, cutting the filler and the necessary restructuring for effective interaction with
> agents, and for the site the necessary optimisations and building.

Asked what to include, the owner chose generated references from the code, `llms.txt` on the site,
the AGENTS block and the help compressed, the design documents deduplicated, and the site
restructured by task on Hextra.

## Decision

- **A style guide**, `docs/STYLE.md`, for every page, help text and AGENTS item; `just docs` checks
  what a program can.
- **One meaning in one home.** Flags and exit codes live in `--help`; keys in `internal/config`.
  The command and configuration references are generated from them (`just reference`) and checked
  against the code in CI.
- **The site is organised by task**: start, daily work, working with several agents, configuration,
  reference, upgrading, contributing. The guide's index orders it.
- **Agents may read the site too.** It publishes `/llms.txt`, `/llms-full.txt` and each page's
  Markdown (the llmstxt.org convention), made from the same pages. This amends W-077's "agents learn
  from the AGENTS block and `--help`": those stay first, the site is an addition.
- **The contributors' pages are published**: CONTRIBUTING and the style guide, under
  guide/contributing. This replaces W-077's single page pointing to GitHub. The design and the
  decision records stay unpublished, as W-077 says.

## Consequences

- A command, flag or key changed without its reference fails CI.
- The guide's addresses moved under their groups; the old addresses redirect.
- Chat, landing, the review pool, the status overview and setup get their pages after their
  second wave of work; until then the site says so.
