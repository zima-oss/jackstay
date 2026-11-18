package gh

// help is `jst gh --help`: the usage lines, then each option.
const help = usage + `
  ci-wait [PR|URL]   wait until a pull request's checks settle (default: this branch's)
    --timeout S      seconds to wait for every check
    --interval S     seconds between looks
    --lines N        log lines shown per failed check
    --any-head       follow the head when it moves, instead of ending with 4
  ci-status [PR|URL] the checks as they are now; nothing is waited for
  run-wait WORKFLOW  wait for a workflow run to complete
    --ref BRANCH|SHA the run's branch or commit (a SHA prefix of 7 or more); default the newest
    --repo URL       https://github.com/OWNER/REPO (default: this checkout's)
    --timeout S, --interval S  as for ci-wait
    --appear S       seconds the run may take to be scheduled (default 120)
  pr-find            one pull request; none found is exit 1
    --head BRANCH    by its head branch
    --search TEXT    by GitHub search text
    --repo URL       as for run-wait
    --wait S         seconds to wait for it to appear (default 0)
    --state STATE    open (default), closed, merged or all
  pr-status          where this branch and its pull request stand
  pr-land            push, open the pull request, wait for its checks, squash-merge the head that
                     was checked, delete the remote branch; the plan is printed first
    --title T        the new pull request's title
    --body-file F    its description, from a file
    --card ID        the card the change is for (repeatable), or none with --reason
    --reason TEXT    why the change is no card's work (with --card none)
    --no-push, --no-create, --no-wait, --no-merge  stop before that step
    --dry-run        print the plan and stop
    --timeout S, --interval S, --lines N  as for ci-wait

exit: 0 fine; 1 not fine, the reason printed; 2 a bad request (also no account, a wrong login, a
host that is not github.com); 3 still pending at the deadline (for ci-status: pending, or no check
registered yet, at once); 4 the head moved while waiting or while the checks were read; 5 an
external format is not what jackstay assumes.

example: jst gh pr-land --title 'Fix the parser' --card W-012
`
