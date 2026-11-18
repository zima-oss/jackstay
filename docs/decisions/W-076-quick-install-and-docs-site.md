# W-076 — A quick install script and a documentation site on GitHub Pages; no unfilled placeholders

Date: 2026-11-18.

## Context

A stranger who finds jackstay should be able to install it in one step and read its documentation
without cloning the repository. A tree published with template fields left open (the licence's
example notice, stub descriptions) reads as unfinished.

## Decision

- **No placeholders.** The licence's appendix carries its notice (copyright 2026 Max Zima). The
  public tree holds no unfilled placeholder or stub text, and a docs check keeps it so.
- **A quick install.** An `install.sh` script, served from the repository and the site, downloads the
  newest release for the platform, verifies it against `checksums.txt` before running anything, and
  installs with `jst install --binary`. The README and the site open with the one-line install,
  followed by what to run first.
- **The documentation site.** It is built with Hugo and the Hextra theme from the same Markdown as
  `docs/guide`, `docs/design` and these decision records, so there is one source. The generator's
  configuration lives in a folder of its own, and a workflow publishes the site to GitHub Pages on
  changes to `main`.

## Consequences

- A change to the guide or the design changes the site with no second copy to keep in step.
- The install script is part of what a release must keep working.
