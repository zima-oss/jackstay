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
