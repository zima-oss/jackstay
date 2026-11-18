#!/bin/sh
# jackstay's quick install (W-076): downloads a release of jst for this machine from GitHub,
# checks the archive against the release's checksums.txt before anything is unpacked or run, and
# lets the binary install itself with `jst install` (under ~/.local by default). It never uses sudo.
#
#   script=$(curl -q --proto '=https' --tlsv1.2 -fsSL https://raw.githubusercontent.com/zima-oss/jackstay/main/install.sh) && sh -c "$script"
#
# Settings, from the environment:
#   JACKSTAY_VERSION   the release to install, such as 0.5.11 (default: the newest release)
#   JACKSTAY_PREFIX    where `jst install` puts it (default: ~/.local; the binary is PREFIX/bin/jst)
#   JACKSTAY_RELEASES  the releases page the archives come from
#                      (default: https://github.com/zima-oss/jackstay/releases)
#
# Exit status: 0 installed; 1 nothing was installed (the message says why); otherwise the status of a
# `jst install` that failed or refused (its own message says what is left).
#
# Everything runs from main, called on the script's last line, so a download cut short runs nothing.
# Every curl starts with -q, so a user's ~/.curlrc (an `insecure` line, for one) is not read.

set -eu

say() {
  printf 'install.sh: %s\n' "$*" >&2
}

fail() {
  say "$*"
  say "nothing was installed"
  exit 1
}

need() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 is needed and was not found on PATH"
}

# The SHA-256 of a file, as hex.
sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d ' ' -f 1
  else
    shasum -a 256 "$1" | cut -d ' ' -f 1
  fi
}

# The platform part of an archive's name: darwin_arm64, linux_amd64, ...
platform() {
  system=$(uname -s)
  machine=$(uname -m)
  case $system in
    Darwin) os=darwin ;;
    Linux) os=linux ;;
    *) fail "this system is not supported: $system (jackstay runs on macOS and Linux)" ;;
  esac
  case $machine in
    x86_64 | amd64) arch=amd64 ;;
    arm64 | aarch64) arch=arm64 ;;
    *) fail "this processor is not supported: $machine (jackstay runs on x86-64 and arm64)" ;;
  esac
  # A shell under Rosetta reports x86_64 on Apple silicon: take the native build.
  if [ "$os" = darwin ] && [ "$arch" = amd64 ] &&
    [ "$(sysctl -n sysctl.proc_translated 2>/dev/null || true)" = 1 ]; then
    arch=arm64
  fi
  echo "${os}_${arch}"
}

# next_steps prints what an install leaves to the user, then the first commands to run. It asks
# the question that matters, which jst this PATH runs, rather than comparing PATH's spelling: when
# none, or one in another folder (another install first on PATH), it prints the line that puts the
# install's folder first, for a POSIX shell (sh, bash, zsh, dash, ksh), and for any other shell names
# the folder. The folder is named as the system resolves it, the one the check compared, as one
# quoted word. It only prints: nothing outside the install prefix is changed.
next_steps() {
  bin=$1
  installed=$(cd "$bin" 2>/dev/null && pwd -P) || installed=$bin
  found=$(command -v jst 2>/dev/null) || found=
  case $found in
    /*) found_in=$(cd "$(dirname "$found")" 2>/dev/null && pwd -P) || found_in= ;;
    *) found_in= ;;
  esac
  if [ "$found_in" != "$installed" ]; then
    if [ -n "$found_in" ]; then
      said="$found comes first on your PATH, not this install: put $installed before it."
    else
      said="$installed is not on your PATH."
    fi
    shell_name=$(basename "${SHELL:-sh}")
    case $shell_name in
      sh | bash | zsh | dash | ksh)
        # The folder as one literal word: in single quotes, each ' written as '\''.
        line="export PATH='$(printf '%s' "$installed" | sed "s/'/'\\\\''/g")':\"\$PATH\""
        case $shell_name in
          zsh) profile="$HOME/.zshrc" ;;
          bash)
            profile="$HOME/.bashrc"
            [ "$(uname -s)" = Darwin ] && profile="$HOME/.bash_profile"
            ;;
          *) profile="$HOME/.profile" ;;
        esac
        cat >&2 <<EOF

$said Add this line to $profile:

  $line

then open a new terminal, or run that line in this one.
EOF
        ;;
      *)
        # Not a POSIX shell (fish, csh, nu and others): its syntax is its own, so the folder is named and
        # the line is left to the user rather than printed untested.
        cat >&2 <<EOF

$said Put this folder first on PATH in $shell_name's configuration, then open a new terminal:

  $installed
EOF
        ;;
    esac
  fi
  cat >&2 <<EOF

Next:
  jst doctor           is this machine ready for jackstay; each problem it finds names its fix
  jst adopt --plan     in a project: what jackstay would write there (jst adopt --apply writes it)
  jst help             every command
EOF
}

main() {
  need uname
  need curl
  need tar
  if ! command -v sha256sum >/dev/null 2>&1 && ! command -v shasum >/dev/null 2>&1; then
    fail "sha256sum or shasum is needed to check the download and neither was found on PATH"
  fi

  releases=${JACKSTAY_RELEASES:-https://github.com/zima-oss/jackstay/releases}
  releases=${releases%/}
  prefix=${JACKSTAY_PREFIX:-$HOME/.local}
  # An absolute path, as jst install makes it, so the PATH line printed at the end names it from
  # any folder.
  case $prefix in
    /*) ;;
    *) prefix="$(pwd)/$prefix" ;;
  esac
  target=$(platform)

  # Only https (TLS 1.2 or newer), redirects included, unless a releases page given in the
  # environment says otherwise.
  case $releases in
    https://*) set -- --proto '=https' --proto-redir '=https' --tlsv1.2 ;;
    *)
      say "warning: $releases is not https"
      set --
      ;;
  esac

  version=${JACKSTAY_VERSION:-}
  if [ -z "$version" ]; then
    # The newest release: GitHub redirects .../releases/latest to .../releases/tag/vX.Y.Z.
    latest=$(curl -q -fsSL "$@" -o /dev/null -w '%{url_effective}' "$releases/latest") ||
      fail "could not ask $releases/latest for the newest release"
    version=${latest##*/}
    case $version in
      v[0-9]*) ;;
      *) fail "$releases/latest led to $latest, which names no release" ;;
    esac
  fi
  version=${version#v}
  case $version in
    '' | *[!0-9A-Za-z.-]*) fail "not a version: $version" ;;
  esac

  archive="jackstay_${version}_${target}.tar.gz"
  base="$releases/download/v$version"
  work=$(mktemp -d "${TMPDIR:-/tmp}/jackstay-install.XXXXXX")
  trap 'rm -rf "$work"' EXIT
  trap 'exit 1' HUP INT TERM

  say "downloading jackstay $version for $target"
  curl -q -fsSL "$@" -o "$work/$archive" "$base/$archive" ||
    fail "could not download $base/$archive (is $version a release, with a build for $target?)"
  curl -q -fsSL "$@" -o "$work/checksums.txt" "$base/checksums.txt" ||
    fail "could not download $base/checksums.txt"

  # The archive's own line, matched by its whole name, and its hash computed here.
  expected=$(awk -v name="$archive" '$2 == name { print $1 }' "$work/checksums.txt")
  [ -n "$expected" ] || fail "checksums.txt has no line for $archive"
  actual=$(sha256 "$work/$archive")
  [ "$expected" = "$actual" ] ||
    fail "$archive does not match its line in checksums.txt (expected $expected, got $actual)"

  mkdir "$work/unpacked"
  tar -xzf "$work/$archive" -C "$work/unpacked" || fail "could not unpack $archive"
  if [ ! -f "$work/unpacked/jst" ] || [ ! -x "$work/unpacked/jst" ]; then
    fail "$archive holds no jst"
  fi

  status=0
  # Telemetry off: the install's own journal entry would be written under ~/.jackstay, outside the
  # prefix, and the script changes nothing outside it.
  JACKSTAY_TELEMETRY=off "$work/unpacked/jst" install --prefix "$prefix" --binary "$work/unpacked/jst" || status=$?
  if [ "$status" -ne 0 ]; then
    say "jst install stopped (exit $status): its message above says what is left"
    exit "$status"
  fi

  say "jackstay $version is installed: $prefix/bin/jst"
  next_steps "$prefix/bin"
}

main
