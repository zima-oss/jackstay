# Install

You get `jst` and `jst-guard` in `~/.local/bin`, without `sudo`, on macOS or Linux, x86-64 or
arm64. jackstay keeps its state apart, in `~/.jackstay`. On Linux, `lsof` must be installed for
background process tracking and worktree clean-up.

## The quick install

In sh, bash or zsh (fish and other shells read `script=$(…)` differently; run `sh` first there):

```sh
script=$(curl -q --proto '=https' --tlsv1.2 -fsSL https://raw.githubusercontent.com/zima-oss/jackstay/main/install.sh) && sh -c "$script"
```

The script ([install.sh](https://github.com/zima-oss/jackstay/blob/main/install.sh), also served
beside this site as `/install.sh`):

1. runs only once curl has downloaded all of it, and reads none of your own curl settings;
2. downloads the newest release for your platform and checks the archive against the release's
   `checksums.txt` before it unpacks or runs anything;
3. installs it with `jst install`, which writes only within its prefix;
4. prints what to run next: the line that puts the install on your `PATH` when it is missing (for
   sh, bash or zsh; for another shell, the folder), then `jst doctor` and `jst adopt`.

With a terminal and a release that provides `jst setup`, the installer can also offer machine settings repairs before printing the next steps.
If both input and output are terminals, the optional interactive setup can apply the machine settings you choose after showing each change; see [plan machine setup](#plan-machine-setup).

It needs `curl`, `tar` and `sha256sum` or `shasum`, and names the one missing. It exits 0 when it
installed, 1 when nothing was installed (the message says why), otherwise with `jst install`'s
status.

### Choose the version, the folder and the checks

Give settings to the `sh` that runs the script:

```sh
script=$(curl -q --proto '=https' --tlsv1.2 -fsSL https://raw.githubusercontent.com/zima-oss/jackstay/main/install.sh) && JACKSTAY_VERSION=0.6.0 JACKSTAY_PREFIX="$HOME/tools" sh -c "$script"
```

| Setting | Effect |
|---|---|
| `JACKSTAY_VERSION=0.6.0` (or `v0.6.0`) | installs that release instead of the newest |
| `JACKSTAY_PREFIX=DIR` | installs under `DIR` instead of `~/.local` |
| `JACKSTAY_PROVENANCE=attestation` | requires `gh` and verifies the release's signed GitHub build attestation for `zima-oss/jackstay`, `.github/workflows/release.yml` and the selected tag |
| `JACKSTAY_SHA256=HEX` | requires the archive to match a 64-digit lowercase SHA-256 digest you obtained separately; works for older releases and without `gh` |

With both of the last two, both checks must pass.

The default checksum only guards against a damaged download: the archive and `checksums.txt` come
from the same place, so it does not prove where the release came from, and the installer prints an
integrity-only warning. For an attested release, install with `JACKSTAY_PROVENANCE=attestation`; a
missing verifier, attestation or identity match stops before unpacking. A digest for
`JACKSTAY_SHA256` must come from outside the release: one copied from the same release proves
nothing more. On macOS the binary is also signed with a Developer ID and notarised;
`codesign -dv ~/.local/bin/jst` shows it.

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

## Check the machine

Put `~/.local/bin` (or your prefix's `bin`) on your `PATH`, open a new terminal, then:

```sh
jst version
jst doctor
```

`jst doctor` checks the install, that the guard refuses a probe command, and Claude Code's sandbox
settings, and follows each problem with its fix. On a new machine it reports `guard-never-called`
until a session with the guard hook has made a call ([What jackstay did](observability.md#check-the-machine-jst-doctor)).

## What an install leaves to you

The install changes nothing outside its prefix. These steps are yours:

| Step | Where | Needed for |
|---|---|---|
| Put `~/.local/bin` on `PATH` | your shell's profile; the script prints the line | running `jst` |
| `jst setup`: let Claude Code's sandbox write the folders jst needs in `~/.jackstay` | `~/.claude/settings.json` ([Claude Code](claude-code.md#the-sandbox)) | the journal, locks, chat and jobs in sandboxed sessions |
| `jst adopt --apply`, then commit what it wrote | each project ([Adopting a project](adopt.md)) | the guard hook and the `AGENTS.md` block |
| Accept Claude Code's trust dialog | the first session in each project | Claude Code loading the hook |
| Optional: install Codex or Claude Code and sign in | your terminal | review rounds ([Reviews](review.md)) |
| Optional: `gh auth login`, and `github.account` in `jackstay.json` | your terminal; the project | `jst checks`, `jst land` and GitHub in `jst tidy` ([Branches and GitHub](branches-and-github.md#the-account)) |

`jst doctor` checks what it can of these.

### Plan machine setup

`jst setup --plan` turns `jst doctor` findings into a setup list. `--json` gives the same list as
data. Setup can put the installed prefix's `bin` first on `PATH` in zsh, bash, sh, dash or ksh's
profile. When Claude Code's sandbox is enabled, it can let the sandbox write the folders of the
jackstay home jst writes from it (logs, locks, chat, jobs, the brief, ids, the trash), each in
`sandbox.filesystem.allowWrite`, and replace an entry naming the whole home, so evidence (receipts,
review rounds, the run log) stays read-only there. No deny rule may cover a folder it
adds. The plan shows the entries removed and added; the settings file keeps its order and gets a
backup beside it.
Other findings carry doctor's manual fix. Fish and other shells need their own configuration,
so setup only reports their PATH repair.

```sh
jst setup --plan --json
jst setup --yes --select path
jst setup --yes --select sandbox
```

The first command reads files only. The other commands apply **only** the selected repair.
Running `jst setup` in a terminal offers apply, show and skip; with piped input it only prints
the plan. The quick installer runs that interactive setup after a successful install only when both
input and output are terminals. Before it starts, it says that setup first asks who merges and which
reviewer runs (once per machine), then shows each change and asks before making it; Ctrl-C stops
setup and leaves the install as it is. Without a terminal it says that setup did not start and that
`jst setup` runs it once `jst` is on your PATH. A release without `jst setup` is told apart, with
`jst doctor` named instead. Before changing an existing shell profile or Claude Code settings,
setup shows the change, saves the original content in a unique `*.jackstay-backup-*` file beside
it and shows that path. It refuses a symlink, an
unusual file, a profile with a different jackstay PATH entry, malformed settings, or a sandbox
deny rule that needs an owner's decision. A second run does not append the same entry again.
Open a new shell or Claude Code session after a change, then run `jst doctor` again.

Before the repairs, setup on a terminal asks, once per machine, who merges a project's changes and
which reviewer runs `jst review` (Codex recommended), and keeps the answers in
`~/.jackstay/.all/setup.json`; `jst adopt` offers them first, and a project's `jackstay.json` still
decides. The plan lists the current answers. To set one without being asked:

```sh
jst setup --reviewer codex,claude-code
```

`jst setup --chat` shows the steps that connect Codex and Claude Code to the session chat, and
whether each is on your `PATH`; `jst setup --chat --plan --json` gives them as JSON. It changes
nothing. Claude Code is rung by its hooks; Codex reads its inbox with `chat.inbox`
([Session chat](session-chat.md)).

## Other ways to install

### From a release archive

Archives are published for `darwin_arm64` (Apple silicon), `darwin_amd64` (Intel Macs),
`linux_amd64` and `linux_arm64`. Each holds the binary `jst`, jackstay's `LICENSE` and the licences
of the modules compiled into it; `checksums.txt` covers them.

```sh
(
  set -eu
  VERSION=0.6.0                                # the release you want, without the v
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
  GOBIN="$dir" go install github.com/zima-oss/jackstay/cmd/jst@v0.6.0
  "$dir/jst" install --binary "$dir/jst"
)
```

Install at a version tag (`@v0.6.0`), not a branch or a commit: `jst version` then names the tag.

## Update

Run the quick install again, or install a newer release any other way above. The new version goes
live in one step, so a running hook never sees half an install. A version only moves forward:
installing a lower version than the live one is refused, and there is no rollback command.

To repair an installation whose launchers fail their probes, reinstall the same version and commit
from a trusted binary. `jst install` verifies the replacement before it switches the live
executable. This also repairs a missing or non-executable live `jst` when the active version
folder's markers and links still identify it as jackstay's.

On macOS, `jst install` can sign a binary you built with your own code-signing identity, so a
firewall's rule for `jst` survives an update: see
[signing at install](../design/implementation.md#74-signing-at-install-w-070). A release's binary,
already signed and notarised, is kept as it is.

Every flag and exit status: `jst install --help`.

## Remove

```sh
jst uninstall
```

It removes the commands and every installed version. It leaves `~/.jackstay` (run records, the
trash, the journal) and says so; remove that folder yourself if you want it gone. Projects whose
hooks name `jst-guard` then run without the guard.

## What can go wrong

| What you see | Why, and what to do |
|---|---|
| `jst: command not found` | `~/.local/bin` (or your prefix's `bin`) is not on `PATH`. |
| Refused: the commit is `unknown` or `+changes` | The binary does not name a commit, or the checkout has uncommitted changes. Commit them, or install from a release or `go install …@vX.Y.Z`. |
| Refused: going back | The live version is newer. Uninstall first if you want an older one. |
| Refused: a command or `lib/jackstay` is not jackstay's | Something else is at that path. jackstay never overwrites what it did not write; move it away. |
| Refused: another install or uninstall is running | Wait for it to finish. |
| The guard does nothing in a session | `jst doctor` in the project says whether its hook runs `jst-guard`. If you installed under another prefix, change the hook's command in `.claude/settings.json` to that prefix's `bin/jst-guard`. |
| `jst doctor` still shows a problem after you did what its `fix:` line says | That is a bug in jackstay: [open an issue](https://github.com/zima-oss/jackstay/issues) with `jst version` and the problem's lines from `jst doctor`. Issues are public: replace project names, paths with your account name and signing identities before you post. |

How an install works (the layout, what is kept, signing):
[install, uninstall, doctor](../design/implementation.md#7-install-uninstall-doctor).
