package gh

// help is `jst checks --help`: the usage lines, then each option.
const help = usage + `
  wait [PR|URL]      wait until a pull request's checks settle (default: this branch's)
    --timeout S      seconds to wait for every check
    --interval S     seconds between looks
    --lines N        log lines shown per failed check
    --any-head       follow the head when it moves, instead of ending with 4
  status [PR|URL]    the checks as they are now; nothing is waited for
  run-wait WORKFLOW  wait for a workflow run to complete
    --ref BRANCH|SHA the run's branch or commit (a SHA prefix of 7 or more); default the newest
    --repo URL       https://github.com/OWNER/REPO (default: this checkout's)
    --timeout S, --interval S  as for wait
    --appear S       seconds the run may take to be scheduled (default 120)

exit: 0 fine; 1 not fine, the reason printed; 2 a bad request (also no account, a wrong login, a
host that is not github.com); 3 still pending at the deadline (for status: pending, or no check
registered yet, at once); 4 the head moved while waiting or while the checks were read; 5 an
external format is not what jackstay assumes.

example: jst checks wait --timeout 1200
`

// StatusUsage is `jst land status`'s usage and options, for `jst land --help`.
const StatusUsage = `  jst land status [--change N|URL]
    where this branch and its pull request stand, then the pull request's checks once
    (exit as for jst checks status; 0 when no pull request is open)
    --change N|URL   select an open pull request from this branch
`

// FindUsage is `jst land find`'s usage and options, for `jst land --help`.
const FindUsage = `  jst land find (--head BRANCH | --search TEXT) [--repo URL] [--state STATE] [--wait S]
    one pull request, printed as NUMBER HEAD URL; none found is exit 1
    --head BRANCH    by its head branch
    --search TEXT    by GitHub search text
    --repo URL       https://github.com/OWNER/REPO (default: this checkout's)
    --state STATE    open (default), closed, merged or all
    --wait S         seconds to wait for it to appear (default 0)
`
