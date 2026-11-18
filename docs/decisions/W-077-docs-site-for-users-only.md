# W-077 — The documentation site carries only what a user needs; design and decisions stay in the repository

Date: 2026-11-18.

## Context

The site W-076 added published the guide, every design document and every decision record. The
design documents and the records are written for people working on jackstay; to someone choosing,
installing and using it they are noise, and they bury the guide.

## Decision

- **Who the site is for.** A person deciding whether to use jackstay, installing it and bringing a
  project to it. Agents learn the tools from the block in `AGENTS.md` and `jst <command> --help`.
- **What it publishes.** A home page that opens with the one-line install, a short getting-started
  page, the guide by task, the integrations (Claude Code, Codex), and one page pointing to the
  design, the decision records and CONTRIBUTING in the repository.
- **What it does not publish.** `docs/design` and the decision records; links to them lead to the
  repository.
- **The guide stays a guide.** Each page keeps what a user needs to do the task; internals live in
  the matching design document.

## Consequences

- The site is small enough to read through; the details are one link away on GitHub.
- A new design document or decision record does not change the site.
