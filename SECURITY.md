# Security

jackstay runs inside your agent sessions: its guard decides which shell commands an agent may run,
and its other commands run your tests, stop process trees and talk to GitHub with your account. A
flaw in any of that is a security problem: a destructive command the guard lets through, a way to
make jackstay run or delete something it should not, a token or secret it writes where it should
not, a process it fails to stop.

## Reporting

Report privately through GitHub: on this repository's **Security** tab, choose **Report a
vulnerability**. Please do not open a public issue or pull request for it. No e-mail address is
published; the report stays between you and the maintainers until a fix is out.

Say what you ran, what jackstay did and what you expected: the command line, `jst version`, the
system (macOS or Linux, its version) and, for the guard, the exact command it judged
(`jst guard check -- 'COMMAND'` shows its answer).

## What to expect

- An answer within a week saying whether the report is taken as a vulnerability.
- A fix in the next release for one that is; the advisory is published with it and credits you
  unless you ask not to be named.

## Supported versions

Only the newest release gets fixes. jackstay is young and moves fast: update before you report,
and say if the problem is gone there.

## Not in scope

- What an agent can do through tools other than Bash: the guard judges Bash commands only.
- A command the guard was configured to allow (`guard.rules` in `jackstay.json` set to `off` or
  `warn`).
- Problems in Claude Code, Codex, git or GitHub themselves; report those to their makers.
