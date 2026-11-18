package lock

// commandHelp is `jst lock --help`.
const commandHelp = `usage: jst lock [--project NAME] [--wait SECONDS] [--also-hold PATH=VARIABLE]... [--note-words WORDS] -- COMMAND [ARG ...]

Runs COMMAND holding the machine-wide lock, in its turn, so heavy commands do not overlap.
  --project NAME            the project named in the holder's note (default: jackstay.json's, else the
                            main checkout's folder name)
  --wait SECONDS            give up after that long, having run nothing (default: wait as long
                            as it takes)
  --also-hold PATH=VARIABLE also hold a project's older lock file PATH, and set VARIABLE as that
                            lock's script reads it (repeatable)
  --note-words WORDS        the command's words in the holder's note (default JACKSTAY_LOCK_NOTE_WORDS)

exit: the command's own status; 2 a lock, jackstay.json or the words cannot be used; 75 gave up
waiting, nothing was run; 127 the command cannot start; stopped by a signal, it ends by that
signal.

example: jst lock -- xcodebuild test -scheme App
`

// statusHelp is `jst lock status --help`.
const statusHelp = `usage: jst lock status [--json]

Who runs under the machine-wide lock and who waits, in order, and the same for each older lock.
  --json   the report as one JSON object

exit: 0 the report; 2 the request, jackstay.json or a lock file cannot be used.

example: jst lock status
`
