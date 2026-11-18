package patterns

// help is `jst patterns --help`.
const help = `usage: jst patterns [DIR...]

Each DIR's jackstay.json (found at or above it; default the working folder): the regular expressions
RE2, Go's dialect, does not take. It takes no options.

exit: 0 every pattern is RE2; 1 some are not; 2 a jackstay.json is missing or is not JSON.

example: jst patterns ~/repositories/widgets
`
