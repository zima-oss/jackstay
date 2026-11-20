package doctor

// help is `jst doctor --help`.
const help = usage + `

Checks the installed layout, the jst on PATH, the guard refusing, its logs of the last 7 days, the
projects heard from and expected, Claude Code's sandbox settings and the lock; in a project, its
adoption and, with a land section, its landing policy against the host's branch rules (the
account, the required checks, the card-line check of adopt's lint workflow, the merge method).
Only that last part asks the host.
  --json          the report as one JSON object
  --prefix DIR    the install prefix to inspect (default: this binary's own)
  --expect NAME   add project NAME to the projects whose guard is expected to be heard from
  --forget NAME   take project NAME off that list

exit: 0 no problem; 1 problems, each named; 2 the request is wrong.

example: jst doctor
`
