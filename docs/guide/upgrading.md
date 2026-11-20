# Upgrading from 0.5.x

jackstay 0.6.0 reads a 0.5.x project's `jackstay.json` as it is. You upgrade the machine first, then
each project in steps, so a project pinned to 0.5.x never meets a key it cannot read.

## 1. Install 0.6.0 on the machine

Update as in [Install](install.md#update); `jst version` then prints `jackstay 0.6.0`. Then:

```sh
jst doctor
```

It names anything left to fix on the machine.

## 2. Move each project in three pull requests

A file with `"jackstay_schema": 3` is refused by 0.5.10 and 0.5.11, with a message that names the
keys they do not know. The project's CI downloads the jackstay its `adopt.pin` names, so a project
moves in this order, each pull request landed before the next:

1. **The pin.** Move `adopt.pin`, the release the workflows download, to 0.6.0. It changes
   `jackstay.json` alone:

   ```sh
   jst adopt --apply --pin --settings-only
   ```

2. **The files.** On the merged base, rewrite the AGENTS block, the hook and the workflows:

   ```sh
   jst adopt --apply
   ```

3. **Schema 3.** Set `"jackstay_schema": 3` and add the keys you want from 0.6.0 (`$schema`,
   `brief`, `status`), then check the file:

   ```sh
   jst check
   ```

`jst adopt --plan` shows what `--apply` would write, without writing it.
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
