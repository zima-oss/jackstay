package scopes

const listHelp = `usage: jst test list [--project NAME]

The project's test scopes: what each runs, its timeout, and when the project runs it (its when).
It reads the options of ` + "`jst test SCOPE`" + `; only --project changes what it shows.
  --project NAME   the project's name in the heading (default jackstay.json's)

exit: 0 listed; 2 the request, the working folder or jackstay.json's test section cannot be used.

example: jst test list
`

const planHelp = `usage: jst test plan SCOPE [ARGS...] [OPTIONS]

The words SCOPE would run, step by step, with each step's judge, timeout, folder, environment,
base and checks. Nothing is run. It reads the options of ` + "`jst test SCOPE`" + `:
  --base REF       the {base} the words are planned with
  --project NAME   the project's name (default jackstay.json's)

Use SCOPE=auto, or changed when test.selection is configured, to select a declared scope from committed paths. The plan prints the selected
scope, every matching path rule and the scope's when. It uses origin/HEAD as the default base;
without that ref, pass --base REF. Auto takes no test names or --commit in a plan.
jst test plan changed BASE..REV plans from a temporary clean copy of REV and removes it.

exit: 0 the plan (a step that cannot run as asked says so on its line); 2 refused: no scope, not a
scope of the project, or the request or jackstay.json's test section cannot be used.

example: jst test plan changed --base origin/main
example: jst test plan auto --base origin/main
`
