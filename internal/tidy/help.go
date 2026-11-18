package tidy

// help is `jst tidy --help`: the usage and its options, the exit statuses and an example.
const help = usage + `  DIR            the checkout to look at (default the working folder)

exit: 0 nothing finished is left; 1 something finished is listed and not removed, or a removal
failed or was called off; 2 the request is wrong.

example: jst tidy --fix --branch fix-parser
`
