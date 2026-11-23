# Preparing the Homebrew tap

This page is for maintainers: it turns a published, signed and notarised release into a Homebrew
formula for a future `zima-oss/homebrew-tap` repository. The tap is not published yet, so people
install with the [installer or a release archive](install.md). Do not advertise
`brew install zima-oss/tap/jackstay` until its formula is in that repository and tested on macOS.

## Prepare a formula from a release

Download the four `jackstay_<version>_<os>_<arch>.tar.gz` assets and `checksums.txt` from the
*same published release* into one folder, then generate the formula and check its syntax:

```sh
go run ./cmd/jst-homebrew-formula 0.6.0 /path/to/release-assets > /tmp/jackstay.rb
ruby -c /tmp/jackstay.rb
```

Use the release's version in place of `0.6.0`. The generator checks every archive against
`checksums.txt`, requires an executable `jst`, `LICENSE` and the dependency licence files at each
archive's root, prints the formula and changes nothing remote. The formula declares distinct URLs and
SHA-256 hashes for Apple silicon, Intel macOS, Linux arm64 and Linux x86-64. The formula installs
the release binary unchanged, keeping its existing macOS signature, and installs all licence
files in Homebrew's package share directory. Its test runs `jst --version`.

Once `zima-oss/homebrew-tap` exists, place the generated file at `Formula/jackstay.rb` there.
Run `brew audit --strict Formula/jackstay.rb` and test installation and `brew upgrade` on macOS
before publishing installation instructions. The current release workflow does not have
credentials to write another repository, so updating the tap is a separate release step. A
dedicated GitHub App or another explicitly scoped credential can automate that step later.

The generated hash verifies downloaded bytes against the formula. Release attestations and any
independent provenance check follow the [release provenance work](../roadmap/backlog.md); a hash copied from the same release alone is
not an independent origin guarantee.

## Homebrew core

The [Homebrew package acceptance policy](https://docs.brew.sh/Package-Acceptance-Policy) requires
public interest beyond the author. As checked on 19 November 2026, the upstream repository has
zero stars, forks and watchers. The current ordinary GitHub thresholds are 75 stars, 30 forks or
30 watchers, and owner submissions have higher thresholds of 225 stars, 90 forks or 90 watchers.
Core inclusion therefore remains a later external review, not a channel this repository can
enable itself. Recheck the [current policy](https://docs.brew.sh/Package-Acceptance-Policy) and
repository numbers before submitting. Homebrew's [tap guide](https://docs.brew.sh/How-to-Create-and-Maintain-a-Tap)
describes the third-party channel independently of core acceptance.
