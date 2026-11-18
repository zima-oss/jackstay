package check

// help is `jst check --help`.
const help = `usage: jst check [DIR]

Is the jackstay.json of DIR's project (default the working folder) usable? Every problem of every
section is named; the test section also gets warnings. For a project's own gate. It takes no
options.

exit: 0 usable (perhaps with warnings); 2 a problem, or no jackstay.json.

example: jst check
`
