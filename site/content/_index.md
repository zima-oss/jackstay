---
title: jackstay
description: Protect edits, verify tests and trace delivery across coding agent sessions.
layout: hextra-home
jackstay_source: site/content/_index.md
---

{{< jackstay-home >}}
script=$(curl -q --proto '=https' --tlsv1.2 -fsSL https://raw.githubusercontent.com/zima-oss/jackstay/main/install.sh) && sh -c "$script"
jst doctor
cd your-project
jst adopt --plan
jst adopt --apply
{{< /jackstay-home >}}
