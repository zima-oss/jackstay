package doctor

// help is `jst doctor --help`.
const help = usage + `

Is jackstay alive on this machine: the installed layout, the guard refusing, its logs of the last 7 days,
the projects it was heard from and those expected, Claude Code's sandbox settings, the lock.
  --json          the report as one JSON object
  --prefix DIR    the install prefix to inspect (default: this binary's own)
  --expect NAME   add project NAME to the projects whose guard is expected to be heard from
  --forget NAME   take project NAME off that list

exit: 0 no problem; 1 problems, each named; 2 the request is wrong.

example: jst doctor
`
