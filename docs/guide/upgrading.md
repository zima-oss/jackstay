# Upgrading from 0.5.x

jackstay 0.6.0 reads a 0.5.x project's `jackstay.json` as it is. You upgrade the machine first, then
each project in steps, so a project pinned to 0.5.x never meets a key it cannot read.

## 1. Install 0.6.0 on the machine

```sh
jst doctor
```

Install as in [Install](install.md#update); `jst version` then prints `jackstay 0.6.0`. `jst doctor`
names anything left to fix.

## 2. Move each project in three pull requests

A file with `"jackstay_schema": 3` is refused by 0.5.10 and 0.5.11, with a message that names the
keys they do not know. So a project moves in this order, each step landed before the next:

1. **The pin**: `jst adopt --apply --pin` moves `adopt.pin`, the release the project's workflows
   download, to 0.6.0.
2. **The files**: `jst adopt --apply` rewrites the AGENTS block, the hook and the workflows for
   0.6.0.
3. **Schema 3**: the new keys, `$schema` and `"jackstay_schema": 3`. `jst adopt --plan` lists
   them.

`jst adopt --plan` shows what each step would write; nothing is written until `--apply`.
[Adopting a project](adopt.md) explains the plan and its conflicts.

## What is new

0.6.0 reads every `jackstay.json` that 0.5.11 reads.
New in 0.6.0:

- **Claude Code as a reviewer**, beside Codex: `jst review --reviewer claude-code`, or `review.use`
  for the project ([Reviews](review.md)). Without either, `jst review` runs Codex, as in 0.5.x.
- **Compaction briefs**: `jst brief save` and `jst brief show` carry a task note through a Claude
  Code compaction ([Compaction briefs](context-brief.md)); `brief.max_age_hours` (default 168)
  sets how long one stays fresh.
- **Portable receipts**: `jst receipts export` and `jst receipts verify` ([Tests](tests.md)).
- **Adoption profiles as bundles**: `jst profile export` and `jst profile verify`
  ([Adopting a project](adopt.md)).
- **An editor schema**: with schema 3, `$schema` points editors at the release's
  `jackstay.schema.json`.

Landing, chat between sessions, the status overview and machine setup are new or change in 0.6.0
too; their pages say how once those features are final.

Every key, with its default: [Configuration reference](../reference/configuration.md). Why 0.6.0
works this way: [W-090](../decisions/W-090-compatibility-of-0-6-0.md).
