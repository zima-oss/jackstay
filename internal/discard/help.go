package discard

// help is `jst discard --help`.
const help = `usage: jst discard [--] PATH...

Each path (a file or a folder, inside this git work tree) goes back to its committed state, HEAD.
What that drops (the file on disk, an index version that differs from both) is first kept in
jackstay's trash: ` + "`jst trash list`" + ` shows it, ` + "`jst trash restore ENTRY`" + ` puts it back. An untracked
file is left as it is (` + "`jst trash PATH`" + ` moves it). Every path is checked before anything changes.
It takes no options; -- before the paths lets a path start with -.

exit: 0 done; 1 a path could not be put back (the message says which); 2 refused, nothing was
changed.

example: jst discard src/parser.go docs/draft.md
`
