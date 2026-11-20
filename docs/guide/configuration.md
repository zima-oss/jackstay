# Configuration

Everything that differs between projects is in one file, `jackstay.json`, at the root of the
repository. `jst adopt` writes a first one; you add a section when you start using a feature.

```sh
jst check
```

`jst check` says whether the file is usable, and names each problem with the key it is in. It exits
0 when the file is usable and 2 when it is not, so a project's gate can run it.

## A small file

```json
{
  "$schema": "https://raw.githubusercontent.com/zima-oss/jackstay/v0.6.0/schemas/jackstay.schema.json",
  "jackstay_schema": 3,
  "project": "my-service",
  "test": {
    "judge": "steps",
    "timeout": 1800,
    "scopes": {
      "quick": {"run": ["python3", "-m", "pytest", "-q"], "evidence": "([0-9]+) passed", "when": "while working"},
      "gate": {"run": ["just", "gate"], "evidence": "Ran ([0-9]+) tests", "when": "before a pull request"}
    }
  },
  "review": {"use": ["codex"]}
}
```

- `jackstay_schema` says which jackstay the file is written for. 0.6.0 reads 1, 2 and 3; use 3 for
  the keys added in 0.6.0 ([Upgrading from 0.5.x](upgrading.md)).
- `$schema` lets an editor complete keys and show what each does.
- Every other section belongs to one feature. A section left out turns its feature off or leaves
  its defaults.

## The sections

| Section | What it configures | Guide |
|---|---|---|
| `test`, `receipts`, `lock` | test scopes, their judges, receipts and the machine-wide lock | [Tests](tests.md) |
| `guard`, `trash` | the guard's rules and levels, how long the trash keeps things | [The guard](guard.md) |
| `procs` | the deadlines of background processes | [Background processes](background.md) |
| `github` | the account and remote jackstay uses on GitHub | [Branches and GitHub](branches-and-github.md) |
| `review` | reviewers, severities and rounds | [Reviews](review.md) |
| `ids`, `records`, `cards`, `changes`, `release` | numbers, records, cards, changelogs and releases | [Records](records.md), [Releases](releases.md) |
| `telemetry` | what the local journal keeps | [What jackstay did](observability.md) |
| `adopt` | the workflows' pin and the organisation's profile | [Adopting a project](adopt.md) |
| `brief` | how long a compaction brief stays fresh | [Compaction briefs](context-brief.md) |
| `baseline` | what `jst baseline` counts | `jst baseline --help` |
| `chat`, `queue`, `status` | chat between sessions, the local merge queue, the status overview | being reworked for 0.6.0 |

Every section and key, with its type, default and meaning: [Configuration
reference](../reference/configuration.md).

## Rules for many projects: profiles

An organisation that adopts many projects the same way keeps its defaults and rules in a profile,
not in each file. `jst adopt --profile` applies one and records it in `adopt.profile`:
[Adopting a project](adopt.md).
