---
title: More on GitHub
# The guide's last page in the sidebar, after the integrations (content/guide/integrations/_index.md).
weight: 200000
# Its address before it joined the guide.
aliases: [/more-on-github/]
# Links are written as in the repository, from this file (layouts/_markup/render-link.html).
jackstay_source: site/content/guide/more-on-github.md
---

This site carries what you need to install jackstay, bring a project to it and use its commands.
How each command works inside, and why it was built that way, is in the repository:

- **The design.** [The implementation spec](../../../docs/design/implementation.md) describes the
  binary as a whole; its section 0 lists the feature specs (the guard, discard and the clean-copy
  gate, testing, review, releases, adoption, the journal).
- **The decision records.** [The index](../../../docs/decisions/README.md) lists each decision in
  force, why it was made and what follows from it. The guide and the design cite them by their W
  number.
- **Contributing.** [CONTRIBUTING](../../../CONTRIBUTING.md) says how to build jackstay, run its checks
  and propose a change.

Questions and bugs go to [the issues](https://github.com/zima-oss/jackstay/issues).
