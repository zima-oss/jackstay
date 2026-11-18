package guard

// help is `jst guard --help`.
const help = `usage: jst guard                                the hook: a tool call on standard input
       jst guard check [--cwd DIR] -- 'COMMAND'

The Claude Code hook (also installed as jst-guard) that refuses destructive commands and names
what to run instead; a project adds rules in jackstay.json's guard section.
  check           judge COMMAND as the hook would, without a tool call
  --cwd DIR       with check: judge it as run in DIR (default the working folder)

exit: the hook 0 always (a refusal is in its answer); check: 0 allowed, 2 refused (the reason on
standard error); 64 the words are wrong.

example: jst guard check -- 'git push --force origin main'
`
