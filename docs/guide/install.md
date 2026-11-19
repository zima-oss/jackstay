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
`jst adopt`. Installation writes within its prefix. With a terminal and a release that provides
`jst setup`, the installer can also offer machine settings repairs before printing the next steps.
The script runs only once curl has downloaded all of it and reads none of your own curl settings.

Two settings, given to the `sh` that runs the script, as in this line (in sh, bash or zsh):

```sh
script=$(curl -q --proto '=https' --tlsv1.2 -fsSL https://raw.githubusercontent.com/zima-oss/jackstay/main/install.sh) && JACKSTAY_VERSION=0.5.11 JACKSTAY_PREFIX="$HOME/tools" sh -c "$script"
```

- `JACKSTAY_VERSION=0.5.11` (or `v0.5.11`) installs that release instead of the newest.
- `JACKSTAY_PREFIX=DIR` installs under `DIR` instead of `~/.local`.
- `JACKSTAY_PROVENANCE=attestation` requires `gh` and verifies a signed GitHub build attestation
  for the archive, `zima-oss/jackstay`, `.github/workflows/release.yml`, and the selected tag.
- `JACKSTAY_SHA256=HEX` requires the archive to match a separately obtained, 64-digit lowercase
  SHA-256 digest. Record it outside the release and its `checksums.txt`. It also works for older
  releases and without `gh`; the script still downloads the archive and checksum over the network.
  If both settings are provided, both checks must pass.
- It needs `curl`, `tar` and `sha256sum` or `shasum`, and names the one missing.
- Exit: 0 installed; 1 nothing was installed (the message says why); otherwise `jst install`'s own.

The default checksum guards against a damaged download. It does not establish release provenance:
the archive and `checksums.txt` come from the same place, and the installer prints an integrity-only
warning. This default keeps releases made before attestations usable. For an attested release,
install with `JACKSTAY_PROVENANCE=attestation`; a missing verifier, attestation, or identity match
stops before unpacking. A digest copied from the same release is not an independent pin. On macOS
the binary is also signed with a Developer ID and notarised, which
`codesign -dv ~/.local/bin/jst` shows.

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

### Plan machine setup

`jst setup --plan` turns `jst doctor` findings into a setup list. `--json` gives the same list as
data. Setup can put the installed prefix's `bin` first on `PATH` in zsh, bash, sh, dash or ksh's
profile. It can also add jackstay's state folder to Claude Code's
`sandbox.filesystem.allowWrite` when the sandbox is enabled and no deny rule covers the folder.
Other findings carry doctor's manual fix. Fish and other shells need their own configuration,
so setup only reports their PATH repair.

```sh
jst setup --plan --json
jst setup --yes --select path
jst setup --yes --select sandbox
```

The first command reads files only. The other commands apply **only** the selected repair.
Running `jst setup` in a terminal offers apply, show and skip; with piped input it only prints
the plan. The quick installer runs that interactive setup after a successful install when both
input and output are terminals. Before changing an existing shell profile or Claude Code settings,
setup shows the change and saves the original content in a
unique `*.jackstay-backup-*` file beside it and shows that path. It refuses a symlink, an
unusual file, a profile with a different jackstay PATH entry, malformed settings, or a sandbox
deny rule that needs an owner's decision. A second run does not append the same entry again.
Open a new shell or Claude Code session after a change, then run `jst doctor` again.

`jst setup --chat` shows local session chat integration routes. The terminal can show steps for
Codex, Claude Code and other MCP hosts. Use `jst setup --chat --plan --json` for the same
read-only plan in JSON. See the [session chat guide](session-chat.md) to register a launcher.
The managed Codex bridge can deliver into a running turn. Other local routes use inbox polling
while their engine integration is unavailable.

## Other ways to install

### From a release archive

Archives are published for `darwin_arm64` (Apple silicon), `darwin_amd64` (Intel Macs),
`linux_amd64` and `linux_arm64`. Each holds the binary `jst`, jackstay's `LICENSE` and the licences
of the modules compiled into it; `checksums.txt` covers them.

```sh
(
  set -eu
  VERSION=0.5.11                                # the release you want, without the v
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
downloaded folder can be removed afterwards. For a release with an attestation, run this before
`tar` in the example (and stop if it fails):

```sh
gh attestation verify "$ARCHIVE" --repo zima-oss/jackstay \
  --signer-workflow zima-oss/jackstay/.github/workflows/release.yml \
  --source-ref "refs/tags/v$VERSION"
```

### From source

You need Go (the version on `go.mod`'s `toolchain` line) and git:

```sh
git clone https://github.com/zima-oss/jackstay.git
cd jackstay
go build -o .build/jst ./cmd/jst
.build/jst install
```

Run inside the checkout, `jst install` builds a private snapshot of the selected commit. Changes
made in your checkout while it builds do not enter the installed binary. The default prefix needs
clean build inputs in `cmd`, `internal`, `go.mod` and `go.sum`. To build from a checkout elsewhere,
use `jst install --checkout DIR`.

### With go install

```sh
(
  set -eu
  dir=$(mktemp -d)                             # a folder of its own, whatever GOBIN says
  GOBIN="$dir" go install github.com/zima-oss/jackstay/cmd/jst@v0.5.11
  "$dir/jst" install --binary "$dir/jst"
)
```

Install at a version tag (`@v0.5.11`), not a branch or a commit: `jst version` then names the tag.

## Update

Run the quick install again, or install a newer release by any of the ways above. The new version
goes live in one step, so a running hook never sees half an install. A version only moves forward:
installing a lower version than the live one is refused, and there is no rollback command.

Reinstall the same version and commit from a trusted binary to repair an installation whose
launchers fail their probes. `jst install` verifies the replacement before it switches the live
executable.

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
It signs only a binary you built; a release's binary, already signed and notarised, is kept as it is.

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
