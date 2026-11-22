package scopes

const listHelp = `usage: jst test list [--project NAME]

Also shows each scope's timeout. Takes the options of ` + "`jst test SCOPE`" + `; only --project changes the
output.
  --project NAME   the project's name in the heading (default jackstay.json's)

exit: 0 listed; 2 the request, the working folder or jackstay.json's test section cannot be used.

example: jst test list
`

const planHelp = `usage: jst test plan SCOPE [ARGS...] [OPTIONS]

Each step with its judge, timeout, folder, environment, base and checks. Takes the options of
` + "`jst test SCOPE`" + `. SCOPE auto (or changed, with test.selection) picks a scope from the changed paths
and prints the matching rules; the base defaults to origin/HEAD (without it, pass --base). auto
takes no test names or --commit; ` + "`changed BASE..REV`" + ` plans on a temporary clean copy of REV.
  --base REF       the {base} the words are planned with
  --project NAME   the project's name (default jackstay.json's)

exit: 0 the plan (a step that cannot run as asked says so on its line); 2 refused: no scope, not a
scope of the project, or the request or jackstay.json's test section cannot be used.

example: jst test plan auto --base origin/main
`

const uncoveredHelp = `usage: jst test uncovered [--commit REV] [--keep-going] [--project NAME] [--lock | --no-lock]

Runs, on a clean copy of REV (default HEAD), every scope that needs no test names and that CI does
not cover on this system, with its deps. The last line names three groups: here (what ran), ci
(covered by CI) and elsewhere (runs only on another system, listed, never holding the run). Its
passed or left-to-ci line is the record ` + "`jst install`" + ` from a checkout needs when the commit's
jackstay.json gives some scope a ` + "`ci`" + `.
  --commit REV     the commit to test (default HEAD)
  --keep-going     after a failure, run the nodes that do not depend on it

exit: 0 passed, or left-to-ci (nothing to run here); 2 not started; otherwise the first failure's
own status, as ` + "`jst test SCOPE`" + `.

example: jst test uncovered
`

const ciHelp = `usage: jst test ci [--keep-going] [--project NAME] [--lock | --no-lock]

Runs every scope whose ` + "`ci`" + ` names this system, with its deps, each once in graph order: the one
command of the CI job ` + "`jst test ci (SYSTEM)`" + `, which the base's rules require.
  --keep-going     after a failure, run the nodes that do not depend on it

exit: 0 passed; 2 not started, or no scope names this system in its ci; otherwise the first
failure's own status, as ` + "`jst test SCOPE`" + `.

example: jst test ci
`
