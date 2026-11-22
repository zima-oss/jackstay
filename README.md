# jackstay

jackstay gives you and your coding agents safe, recorded commands for work in a Git repository.
Its one binary, `jst`, refuses shell commands that destroy work, runs tests and counts them, gives
background processes a deadline, and helps finish branches, reviews and releases.

```sh
script=$(curl -q --proto '=https' --tlsv1.2 -fsSL https://raw.githubusercontent.com/zima-oss/jackstay/main/install.sh) && sh -c "$script"
```

Then, in your project:

```sh
jst doctor                            # is jackstay healthy on this machine
jst adopt --apply                     # write jackstay.json, the AGENTS block and the guard hook
jst guard check -- 'git reset --hard' # refused, with what to run instead
```

It works from any shell, with or without an agent; [Claude Code](docs/guide/claude-code.md) and
[Codex](docs/guide/codex.md) can load its guard hook and review changes. It runs on macOS (Apple
silicon, Intel) and Linux (x86-64, arm64), not Windows.

## Read on

- [Start in five minutes](docs/guide/getting-started.md): install, adopt, a counted test run, a
  guard check.
- [Install](docs/guide/install.md): read the script first, other ways, update, remove.
- [The guide](docs/guide/README.md): every task, one page each.
- [Changelog](CHANGELOG.md): what changed in each release, and how to upgrade.
- [Security notes](SECURITY.md): what the guard does and does not protect, and how to report a
  vulnerability.
- [Contributing](CONTRIBUTING.md), the [design](docs/design/implementation.md) and the
  [decisions](docs/decisions/README.md).

jackstay is distributed under the [Apache License 2.0](LICENSE). Licences of the modules it uses:
[docs/licences.md](docs/licences.md).
