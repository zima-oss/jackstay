# Install

jackstay is one static binary, `jst`, for macOS and Linux on x86-64 or arm64. It installs into
`~/.local` by default, without `sudo`, and keeps its state apart, in `~/.jackstay`.

## The quick install

In sh, bash or zsh (fish and other shells read `script=$(…)` differently; run `sh` first there):

```sh
script=$(curl -q --proto '=https' --tlsv1.2 -fsSL https://raw.githubusercontent.com/zima-oss/jackstay/main/install.sh) && sh -c "$script"
```

The script ([install.sh](https://github.com/zima-oss/jackstay/blob/main/install.sh), also served
beside this site as `/install.sh`) downloads the newest release for your platform, checks the
archive against the release's `checksums.txt` before it unpacks or runs anything, installs it with
`jst install` and prints what to run next: when the install is not on your `PATH`, the line that
puts it there (for sh, bash or zsh; for another shell, the folder), then `jst doctor` and
`jst adopt`. It changes nothing outside
the install's prefix. It runs only once curl has downloaded all of it, and
reads none of your own curl settings.

Two settings, given to the `sh` that runs the script, as in this line (in sh, bash or zsh):

```sh
script=$(curl -q --proto '=https' --tlsv1.2 -fsSL https://raw.githubusercontent.com/zima-oss/jackstay/main/install.sh) && JACKSTAY_VERSION=0.5.10 JACKSTAY_PREFIX="$HOME/tools" sh -c "$script"
```

- `JACKSTAY_VERSION=0.5.10` (or `v0.5.10`) installs that release instead of the newest.
- `JACKSTAY_PREFIX=DIR` installs under `DIR` instead of `~/.local`.
- It needs `curl`, `tar` and `sha256sum` or `shasum`, and names the one missing.
- Exit: 0 installed; 1 nothing was installed (the message says why); otherwise `jst install`'s own.

The checksum guards against a damaged or swapped download, not against a compromised release: the
archive and `checksums.txt` come from the same place. On macOS the binary is also signed with a
Developer ID and notarised, which `codesign -dv ~/.local/bin/jst` shows.

### Read it before you run it

Download it into a fresh folder, so an older copy is never run, read it, and run it from there:

```sh
dir=$(mktemp -d)
curl -q --proto '=https' --tlsv1.2 -fsSL -o "$dir/install.sh" https://raw.githubusercontent.com/zima-oss/jackstay/main/install.sh &&
  less "$dir/install.sh" &&
  sh "$dir/install.sh"
```

The script is served from `main`. A release made after the script was added carries it at its tag
too: `https://raw.githubusercontent.com/zima-oss/jackstay/vX.Y.Z/install.sh`.

## Put it on your PATH

Put `~/.local/bin` (or your prefix's `bin`) on your `PATH`; the script prints the line to add
(for sh, bash or zsh). Then check the machine:

```sh
jst version
jst doctor
```

`jst doctor` checks the install, that the guard refuses a probe command, and Claude Code's sandbox
settings. On a new machine it reports `guard-never-called` until a session with the guard hook has
made a call; see [What jackstay did](observability.md#jst-doctor). Each problem it reports is
followed by its fix. What else an install leaves to you:
[getting started](getting-started.md#what-an-install-leaves-to-you).

## Other ways to install

### From a release archive

Archives are published for `darwin_arm64` (Apple silicon), `darwin_amd64` (Intel Macs),
`linux_amd64` and `linux_arm64`. Each holds the binary `jst`, jackstay's `LICENSE` and the licences
of the modules compiled into it; `checksums.txt` covers them.

```sh
(
  set -eu
  VERSION=0.5.10                                # the release you want, without the v
  PLATFORM=darwin_arm64                        # or darwin_amd64, linux_amd64, linux_arm64
  ARCHIVE="jackstay_${VERSION}_${PLATFORM}.tar.gz"
  BASE="https://github.com/zima-oss/jackstay/releases/download/v$VERSION"
  curl -fLO "$BASE/$ARCHIVE"
  curl -fLO "$BASE/checksums.txt"
  # The archive's own line in checksums.txt, matched by its whole name, and its hash computed here.
  expected=$(awk -v name="$ARCHIVE" '$2 == name { print $1 }' checksums.txt)
  actual=$( (sha256sum "$ARCHIVE" 2>/dev/null || shasum -a 256 "$ARCHIVE") | cut -d ' ' -f 1)
  test -n "$expected"                         # the archive has a line of its own
  test "$expected" = "$actual"               # and its hash matches
  mkdir jackstay-download
  tar -xzf "$ARCHIVE" -C jackstay-download
  jackstay-download/jst install --binary jackstay-download/jst
)
```

The parentheses run it in a subshell: `set -eu` stops it at the first failing line (a download, a
missing or different checksum) without closing your terminal. With the GitHub CLI, replace only the
two `curl` lines with
`gh release download "v$VERSION" -R zima-oss/jackstay -p "$ARCHIVE" -p checksums.txt`. The
downloaded folder can be removed afterwards.

### From source

You need Go (the version on `go.mod`'s `toolchain` line) and git:

```sh
git clone https://github.com/zima-oss/jackstay.git
cd jackstay
go build -o .build/jst ./cmd/jst
.build/jst install
```

Run inside the checkout, `jst install` builds the checkout again (with its commit) and installs it.
The checkout must have no uncommitted change to `cmd`, `internal`, `go.mod` or `go.sum`. To install
from a checkout elsewhere, use `jst install --checkout DIR`.

### With go install

```sh
(
  set -eu
  dir=$(mktemp -d)                             # a folder of its own, whatever GOBIN says
  GOBIN="$dir" go install github.com/zima-oss/jackstay/cmd/jst@v0.5.10
  "$dir/jst" install --binary "$dir/jst"
)
```

Install at a version tag (`@v0.5.10`), not a branch or a commit: `jst version` then names the tag.

## Update

Run the quick install again, or install a newer release by any of the ways above. The new version
goes live in one step, so a running hook never sees half an install. A version only moves forward:
installing a lower version than the live one is refused, and there is no rollback command.

## Remove

```sh
jst uninstall
```

It removes the commands and every installed version. It leaves `~/.jackstay` (run records, the
trash, the journal) and says so; remove that folder yourself if you want it gone. Projects whose
hooks name `jst-guard` then run without the guard.

## The commands

```sh
jst install [--prefix DIR] [--checkout DIR | --binary FILE]
jst uninstall [--prefix DIR]
jst version                     # jackstay <version> (<commit>)
```

- `--prefix DIR` installs under `DIR` instead of `~/.local`.
- `--checkout DIR` builds from that jackstay checkout; `--binary FILE` installs a binary you have.
- Exit: 0 installed (or already installed); 1 a step failed half-way (run it again to finish);
  2 refused, nothing changed.

On macOS, `jst install` can sign the binary with your own code-signing identity, so a firewall's
rule for `jst` survives an update: see [signing at install](../design/implementation.md#74-signing-at-install-w-070).

## What can go wrong

| What you see | Why, and what to do |
|---|---|
| `jst: command not found` | `~/.local/bin` (or your prefix's `bin`) is not on `PATH`. |
| Refused: the commit is `unknown` or `+changes` | The binary does not name a commit, or the checkout has uncommitted changes. Commit them, or install from a release or `go install …@vX.Y.Z`. |
| Refused: going back | The live version is newer. Uninstall first if you really want an older one. |
| Refused: a command or `lib/jackstay` is not jackstay's | Something else is at that path. jackstay never overwrites what it did not write; move it away. |
| Refused: another install or uninstall is running | Wait for it to finish. |
| The guard does nothing in a session | `jst doctor` in the project says whether its hook runs `jst-guard`. If you installed under another prefix, change the hook's command in `.claude/settings.json` to that prefix's `bin/jst-guard`. |
| `jst doctor` still shows a problem after you did what its `fix:` line says | That is a bug in jackstay: [open an issue](https://github.com/zima-oss/jackstay/issues) with `jst version` and the problem's lines from `jst doctor`. Issues are public: replace project names, paths with your account name and signing identities before you post. |

How an install works (the layout, what is kept, signing):
[install, uninstall, doctor](../design/implementation.md#7-install-uninstall-doctor).
