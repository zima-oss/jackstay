package check

// help is `jst check --help`.
const help = `usage: jst check [DIR]

Names every problem of every section, and warnings for the test section. DIR defaults to the
working folder.

exit: 0 usable (perhaps with warnings); 2 a problem, or no jackstay.json.

example: jst check
`
