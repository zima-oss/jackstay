package release

// The help of each `jst release` command.

const prepareHelp = `usage: jst release prepare [--json]

The next release as the release workflow would make it: version, notes, files; writes nothing.
  --json   the plan as one JSON object

exit: 0 the plan; 1 nothing to release (no entry bumps the version, no override), or an entry is
invalid; 2 the request, jackstay.json or the release state (the checkout, the history, the version
files) cannot be used.

example: jst release prepare
`

const overrideHelp = `usage: jst release override --version V --reason TEXT

A version given by hand for the next release, written as a file for a pull request.
  --version V      the version the next release gets (required)
  --reason TEXT    why; recorded in the release (required)

exit: 0 written (or already there, the same), its path printed; 1 V is not greater than the last
release, or another override is there; 2 the request, jackstay.json or the release state cannot be
used.

example: jst release override --version 2.0.0 --reason 'the new file format'
`

const notesHelp = `usage: jst release notes [VERSION] [--format FORMAT] [--audience AUDIENCE]

A released version's notes, or without VERSION the next release's.
  --format FORMAT     markdown (default), text, testflight or sparkle-html
  --audience WHO      consumer (default), internal or all; testflight and sparkle-html are for
                      consumers only

exit: 0 printed; 1 VERSION is not a release on this branch, an entry is invalid, or the testflight
notes do not fit even as titles; 2 the request, jackstay.json or the release state cannot be used.

example: jst release notes 1.3.0 --format text
`

const yankHelp = `usage: jst release yank X.Y.Z --reason TEXT

Marks release X.Y.Z yanked, as an entry for a pull request.
  --reason TEXT   why the release is yanked (required)

exit: 0 written (or already there, the same), its path printed; 1 X.Y.Z is not a release on this
branch, or its entry file is there with other content; 2 the request, jackstay.json or the release
state cannot be used.

example: jst release yank 1.3.1 --reason 'it loses the settings file'
`

const pullRequestHelp = `usage: jst release pr [--dry-run]

The release workflow's step: create, refresh or close the release pull request. Outside GitHub
Actions it is always a dry run.
  --dry-run   say what would be done; write nothing

exit: 0 done, or nothing to do; 1 a problem reported, the rest done (the base's entries cannot be
released or prepared, a pull request not closed, the pull request kept changing), or GitHub's rate
limit; 2 the request, jackstay.json, the release state, git or GitHub cannot be used.

example: jst release pr --dry-run
`

const publishHelp = `usage: jst release publish [--dry-run]

The release workflow's step: make, tag and publish what a landed release pull request asks.
Outside GitHub Actions it is always a dry run.
  --dry-run   say what would be done; write nothing

exit: 0 done, or nothing to do; 1 a problem reported, the rest done (a tag on another commit, a
trailer or tag on no release, a draft release, a tag or release not made, the base kept moving), or
GitHub's rate limit; 2 the request, jackstay.json, the release state, git or GitHub cannot be used,
or the release the request asks for could not be made.

example: jst release publish --dry-run
`
