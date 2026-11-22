package lock

// commandHelp is `jst lock --help`.
const commandHelp = `usage: jst lock [--project NAME] [--wait SECONDS] [--also-hold PATH=VARIABLE]... [--note-words WORDS] -- COMMAND [ARG ...]

  --project NAME            the project in the holder's note (default: jackstay.json's, else the
                            main checkout's folder name)
  --wait SECONDS            give up after that long, having run nothing (default: no limit)
  --also-hold PATH=VARIABLE also hold the older lock file PATH, setting VARIABLE as its script
                            reads it (repeatable)
  --note-words WORDS        the command's words in the holder's note (default JACKSTAY_LOCK_NOTE_WORDS)

exit: the command's own status (stopped by a signal, it ends by that signal); 2 a lock,
jackstay.json or the words cannot be used; 75 gave up waiting, nothing was run; 127 the command
cannot start.

example: jst lock -- xcodebuild test -scheme App
`

// statusHelp is `jst lock status --help`.
const statusHelp = `usage: jst lock status [--json]

In order, and the same for each older lock.
  --json   the report as one JSON object

exit: 0 the report; 2 the request, jackstay.json or a lock file cannot be used.

example: jst lock status
`
