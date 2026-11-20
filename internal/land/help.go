package land

// help is `jst land --help`.
const help = `usage:
  jst land [--change N|URL] [--title T] [--body-file F] [--card ID]... [--reason TEXT]
           [--prepare | --confirm] [--plan] [--timeout S] [--interval S] [--lines N]
  jst land status [--change N|URL]
  jst land find (--head BRANCH | --search TEXT) [--repo URL] [--state STATE] [--wait S]

Push the branch, open the change request, wait for the checks the base's rules require (and those
land.checks lists), and merge at the head that was checked. The plan is printed first. The agent
merges only when land.autonomy is merge-when-green and the change touches no human path
(jackstay.json, .github/workflows/**, .claude/settings.json, and land.human_paths); any other
change stops ready for a person, who merges it on the host's page or with --confirm. Checks that
are not required are reported, never waited for and never blocking. Autonomy and checks are read
from the base's jackstay.json; human paths from the base's and the change's. This checkout is
never switched, pulled or rebased.

  --change N|URL   an existing open change request from this branch
  --title T        the new change request's title
  --body-file F    its description, from a file
  --card ID        the card the change is for (repeatable), or none with --reason
  --reason TEXT    why the change is no card's work (with --card none)
  --prepare        stop ready for a person, whatever land.autonomy says
  --confirm        a person at a terminal types the change's number to merge it; the policy and
                   checks are judged again after the number is typed
  --plan           print the plan and stop
  --timeout S      seconds to wait for the checks (github.wait.timeout_seconds, else 1800)
  --interval S     seconds between looks (github.wait.interval_seconds, else 20)
  --lines N        log lines shown per failed check (github.wait.log_lines, else 25)

  status           where this branch and its change request stand
  find             one change request by head branch or search text; none found is exit 1

The last line on standard error is "jst land: STATE".
exit: 0 landed (merged, or merged by the host's queue within the wait), or planned (--plan); 1
refused (a required check failed or was cancelled, a draft, a conflict, an operation the host
does not offer); 2 invalid (usage, configuration, no account, no land section); 3 pending (checks
or the queue still pending at the deadline); 4 moved (the head, the base or the test merge moved:
run it again); 5 unexpected (a host answer jackstay does not expect); 6 ready for a person.

example: jst land --title 'Fix the parser' --card W-012
`
