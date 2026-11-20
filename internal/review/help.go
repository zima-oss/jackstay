package review

const reviewHelp = `usage: jst review [REV | BASE..REV] [--task NAME] [--reviewer NAME,...] [--brief FILE] [--focus TEXT]
                 [--fresh] [--model M] [--effort E] [--timeout MIN] [--past-limit] [--json]

REV alone is reviewed against its first parent (default HEAD). An empty review.use needs
--reviewer. Give a revision named like a subcommand after --.
  --task NAME         the task the rounds belong to (default: the branch)
  --reviewer NAMES    codex, claude-code or both, comma-separated (default: review.use, else codex)
  --brief FILE        a brief file added to the prompt
  --focus TEXT        where to look hardest (it never narrows the round)
  --fresh             start a new chain: a full review from BASE
  --model M           the model, for this call
  --effort E          the reasoning effort, for this call
  --timeout MIN       minutes per run, for this call
  --past-limit        a round past review.round_limit, after the owner's word
  --json              the result as one JSON object instead of the summary

exit: 0 the round does not block; 1 it blocks; 2 usage or configuration; 3 a place is due for
redesign; 4 the round limit is reached, the owner decides; 5 a reviewer gave no usable answer;
6 the environment refuses (a sandbox, no login, reviewer unavailable).

example: jst review origin/main..HEAD --task parser-fix --reviewer codex
`

const lastHelp = `usage: jst review last [--task NAME] [--json]

  --task NAME   the task (default: the branch)
  --json        as JSON

exit: 0 shown, whatever the round's outcome; 2 the request or configuration, a task with no review,
or a record that cannot be read.

example: jst review last
`

const checkHelp = `usage: jst review check [--task NAME] BASE..REV

  --task NAME   the task (default: the branch)

exit: 0 reviewed and clear; 1 not clear, each reason named; 2 the request or configuration.

example: jst review check origin/main..HEAD --task parser-fix
`

const listHelp = `usage: jst review list [--task NAME] [--rebuild] [--json]

  --task NAME   only this task
  --rebuild     write the index anew from the folders first
  --json        as JSON lines

exit: 0 the list; 2 the request or configuration, or the index cannot be read or written anew.

example: jst review list --task parser-fix
`

const showHelp = `usage: jst review show TASK ROUND [--json]

  --json   the round's record

exit: 0 shown; 2 the request or configuration, or no such round (its file cannot be read).

example: jst review show parser-fix 2
`

const pruneHelp = `usage: jst review prune

exit: 0 done (nothing to remove included); 2 the request or configuration, or a removal failed.

example: jst review prune
`
