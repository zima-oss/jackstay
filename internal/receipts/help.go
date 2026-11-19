package receipts

// testHelp is `jst test --help`: a scope's run and the raw form.
const testHelp = `usage: jst test SCOPE [ARGS...] [--no-reuse] [--wait S] [--base REF] [--lock | --no-lock] [--resume] [--keep-going] [--commit REV]
       jst test [--no-reuse] [--lock | --no-lock] [--project NAME] [--wait SECONDS] -- COMMAND [ARG ...]

Runs a scope of jackstay.json's test section (` + "`jst test list`" + ` shows them), or with -- any command, judged,
recorded and keyed by the tree. Only the outcome passed is a pass: read the last ` + "`jst test:`" + ` line.
With test.selection configured, use jst test changed [BASE..REV] to select a scope from committed paths.
  --no-reuse      run even when a receipt of this tree could be reused
  --wait S        give up waiting for the machine-wide lock after S seconds
  --lock          take the machine-wide lock; --no-lock: do not (default: the scope's lock)
  --project NAME  the project's name in the record (default jackstay.json's)
  --base REF      the scope's {base} or the base for automatic selection
  --resume        a gate: skip the steps that passed on this same tree
  --keep-going    a gate: run the remaining steps after one fails
  --commit REV    run on a clean copy of REV that jackstay makes and then removes

A scope that lists steps needs receipts.toolchain written out in jackstay.json: the commands whose
output names your compilers and tools (` + "`[[\"go\", \"version\"]]`" + `), part of each step's key,
or [] for none.

exit: 0 passed (or exited 0 uncounted); 1 failed; 2 not started or refused, nothing ran;
75 gave up waiting for the lock; 76 no test ran; 77 unjudged (output not read to its end, or
leftovers that cannot be attributed); 124 timed out; 127 the command cannot start. A command that
ran and failed passes its own status on, so 2 or 77 may be its own: the last jst test: line names
the outcome.

example: jst test gate --commit HEAD
`

const lastHelp = lastUsage + `

The last run of this project as ` + "`jst test`" + ` said it: outcome, what went wrong, where its output is.
Nothing is run.
  --project NAME   that project's last run (default this project's)
  --scope S        the last run of scope S
  --json           as JSON

exit: 0 shown, whatever the run's outcome; 1 no run of that project (or scope) is in the record;
2 a bad request, or (without --project) jackstay.json cannot be used.

example: jst test last --scope quick
`

const receiptsHelp = receiptsUsage + `

See each command's --help.

exit: 2 always: it runs nothing and prints this usage on standard error; each command under it
names its own.

example: jst receipts stats --json
`

const keyHelp = `usage: jst receipts key -- COMMAND [ARG ...]

The receipt key COMMAND would have here, what went into it, and whether a receipt would be
reused. Nothing is run. It takes no options.

exit: 0 the key; 1 no key could be made (the reason named); 2 a bad request, or jackstay.json or
its receipts section cannot be used.

example: jst receipts key -- go test ./...
`

const statsHelp = `usage: jst receipts stats [--project NAME] [--json]

Runs, reuses and seconds saved per project, from the run log.
  --project NAME   only that project
  --json           as JSON

exit: 0 the counts (an empty log included); 1 the counts could not be written as JSON; 2 a bad
request.

example: jst receipts stats --project widgets
`

const listHelp = `usage: jst receipts list [--project NAME]

The stored receipts: project, end, seconds, command, head, key.
  --project NAME   only that project's

exit: 0 the list (a receipt that cannot be read is left out); 2 a bad request, or jackstay's home
folder cannot be found.

example: jst receipts list
`
