package patterns

// help is `jst patterns --help`.
const help = `usage: jst patterns [DIR...]

Lists the patterns RE2 (Go's dialect) does not take, in the jackstay.json at or above each DIR
(default the working folder).

exit: 0 every pattern is RE2; 1 some are not; 2 a jackstay.json is missing or is not JSON.

example: jst patterns .
`
