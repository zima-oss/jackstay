---
title: Integrations
# After the guide's pages, which the guide's README orders (layouts/_partials/jackstay/pages-from-docs.html).
weight: 100000
# Links are written as in the repository, from this file (layouts/_markup/render-link.html).
jackstay_source: site/content/guide/integrations/_index.md
---

jackstay works on its own; these pages say how it fits the agents that use it. Both are optional.

- [Claude Code](../../../../docs/guide/claude-code.md): the guard as a `PreToolUse` hook, the
  sandbox, warnings that reach the agent, the block `jst adopt` writes into `AGENTS.md`.
- [Codex](../../../../docs/guide/codex.md): the reviewer `jst review` runs, and the guard as a
  Codex hook.
