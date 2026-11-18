package changes

// newHelp is `jst changes new --help`.
const newHelp = `usage: jst changes new --slug SLUG [--type TYPE] [--audience AUDIENCE] [--title TEXT] [--migration TEXT] [--card ID]

A new changelog entry, as a file of its own; its path is printed. In a project with a release
section, --type, --audience and --title are required.
  --slug SLUG        the entry's name: lower-case letters, digits and -, at most 80
  --type TYPE        breaking, feature, fix, deprecated, security or note
  --audience WHO     consumer or internal
  --title TEXT       the entry's title, one line
  --migration TEXT   what a consumer changes (required with --type breaking)
  --card ID          the card the change is for (needs a cards section)

exit: 0 written, its path printed; 1 the entry would not be valid; 2 the request (a bad or taken
slug, a missing option), jackstay.json, the template or the release state cannot be used.

example: jst changes new --slug faster-lock --type fix --audience consumer --title 'The lock waits less'
`

// checkHelp is `jst changes check --help`.
const checkHelp = `usage: jst changes check [--json]

Every changelog entry: valid, or each problem named.
  --json   the result as one JSON object

exit: 0 every entry is valid; 1 problems, each named; 2 jackstay.json cannot be used.

example: jst changes check
`

// viewHelp is `jst changes view --help`.
const viewHelp = `usage: jst changes view [--version V] [--date D] [--audience AUDIENCE]

The next release's section of the changelog, as it would be written; nothing is written.
  --version V        the version in the heading
  --date D           the date in the heading (default today)
  --audience WHO     consumer (default), internal or all

exit: 0 printed: the section, or the line that there is no entry; 2 the request, jackstay.json or
the release state cannot be used, or (with a release section and no --version) no entry moves the
version: give --version V to see them.

example: jst changes view --version 1.4.0
`

// collectHelp is `jst changes collect --help`.
const collectHelp = `usage: jst changes collect --version V [--date D] [--dry-run]

Writes the entries into the changelog as the section of version V and removes them. For a project
without a release section (with one, ` + "`jst release prepare`" + ` and its pull request do it).
  --version V   the section's version (required)
  --date D      the date in the heading (default today)
  --dry-run     print the section and change nothing

exit: 0 collected (or printed, with --dry-run; or the section was there and its entries are
removed); 1 nothing or not all collected: no entry, an invalid entry, uncommitted changes in the
changelog or the entries' folder, the changelog changed meanwhile, the version's heading there with
other entries, or an entry changed while collected (kept, named); 2 the request, jackstay.json, a
project with a release section, or a file that cannot be read or written.

example: jst changes collect --version 1.2.0 --dry-run
`
