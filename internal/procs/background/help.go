package background

const bgHelp = bgUsage + `

Runs COMMAND supervised: in a process group of its own, stopped at a deadline or when the Claude
Code session it was started under is gone, listed by ` + "`jst procs`" + `. Start it with the tool's
background flag.
  --for MINUTES       the deadline (default procs.default_minutes, 30; at most
                      procs.max_minutes, 240)
  --name NAME         a name to stop it by (` + "`jst procs stop NAME`" + `)
  --keep              no deadline and no session check: stopped only by ` + "`jst procs stop`" + `
  --watch PID, --check-every SECONDS   for jackstay's own tests only

exit: the command's own status (128+N for signal N); 2 a bad request; 126 the command could not
start; 127 it was not found; stopped by a signal, jst bg ends by that signal.

example: jst bg --for 20 --name server -- npm run dev
`

const procsHelp = `usage: jst procs [--all] [--json]

What jst bg supervises now: id, name, age, deadline, session, folder, command. Orphans past their
deadline are stopped and gone entries are removed on the way.
  --all    every session's, not only this session's
  --json   as JSON

exit: 0 the list (an empty one included); 2 a bad request, or jst bg's records cannot be opened.

example: jst procs --all
`

const stopHelp = `usage: jst procs stop [--all] ID|NAME...

Stops each one (TERM, 5 seconds, then KILL): by ID whoever started it; by NAME this session's only.
  --all   with a NAME: every session's of that name

exit: 0 stopped; 1 a name or id not stopped; 2 a bad request.

example: jst procs stop server
`
