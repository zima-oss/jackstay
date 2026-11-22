---
title: jackstay
description: Stops coding agents' destructive commands, counts the tests a change passed, lands it when its checks pass, and keeps parallel sessions apart.
layout: hextra-home
jackstay_source: site/content/_index.md
---

{{< jackstay-home >}}
script=$(curl -q --proto '=https' --tlsv1.2 -fsSL https://raw.githubusercontent.com/zima-oss/jackstay/main/install.sh) && sh -c "$script"
{{< /jackstay-home >}}
