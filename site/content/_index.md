---
title: jackstay
layout: hextra-home
# Links are written as in the repository, from this file (layouts/_markup/render-link.html).
jackstay_source: site/content/_index.md
---

{{< hextra/hero-headline >}}
  Guard rails for AI coding agents
{{< /hextra/hero-headline >}}

{{< hextra/hero-subtitle >}}
  One Go binary, `jst`, beside the agents working in your repositories: it refuses the commands
  that destroy work, runs and records your tests, stops background processes at a deadline,
  and tidies what agents leave behind.
{{< /hextra/hero-subtitle >}}


{{< hextra/hero-section heading="h3" style="margin-top: 2rem" >}}Install{{< /hextra/hero-section >}}

On macOS or Linux, x86-64 or arm64, in sh, bash or zsh:

```sh
script=$(curl -q --proto '=https' --tlsv1.2 -fsSL https://raw.githubusercontent.com/zima-oss/jackstay/main/install.sh) && sh -c "$script"
```

The script checks the release archive against its `checksums.txt` before it runs anything, and
installs into `~/.local` without `sudo`. To read it first, or to install by hand, from source or
with `go install`, see [Install](../../docs/guide/install.md).

{{< hextra/hero-section heading="h3" style="margin-top: 1rem" >}}Then{{< /hextra/hero-section >}}

```sh
jst doctor              # is this machine ready
cd your-project
jst adopt --plan        # what jackstay would write here; `jst adopt --apply` writes it
```

{{< hextra/hero-button text="Get started" link="guide/getting-started/" style="margin: 1.5rem 0.5rem 2.5rem 0" >}}
{{< hextra/hero-button text="Read the guide" link="guide/" style="margin: 1.5rem 0 2.5rem" >}}


{{< hextra/feature-grid >}}
  {{< hextra/feature-card title="The guard" subtitle="A hook that refuses destructive git and rm commands and names the safe one instead." link="guide/guard/" >}}
  {{< hextra/feature-card title="Tests with evidence" subtitle="Only a run whose tests were counted passes; every run is recorded, with a receipt keyed by the tree." link="guide/tests/" >}}
  {{< hextra/feature-card title="Background processes" subtitle="Jobs with a deadline that stop when their session is gone, and waits without sleep loops." link="guide/background/" >}}
  {{< hextra/feature-card title="Branches and GitHub" subtitle="Tidy what is finished, after asking git, GitHub and the process table." link="guide/branches-and-github/" >}}
  {{< hextra/feature-card title="Cold reviews" subtitle="Review rounds by Codex, with findings, verdicts and a round limit." link="guide/review/" >}}
  {{< hextra/feature-card title="What happened" subtitle="A local OpenTelemetry journal of every command, queried with jst log and jst stats." link="guide/observability/" >}}
{{< /hextra/feature-grid >}}
