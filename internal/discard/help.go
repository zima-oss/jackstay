package discard

// help is `jst discard --help`.
const help = `usage: jst discard [--] PATH...

Each file or folder goes back to HEAD; the file on disk and a differing index version are then
deleted, each deletion logged (` + "`jst trash list`" + ` shows it). With trash.keep on (experimental)
they are kept in jackstay's trash instead, and ` + "`jst trash restore ENTRY`" + ` puts them back. An
untracked file is left alone (` + "`jst trash PATH`" + ` deletes it). Every path is checked before anything changes; -- lets a path
start with -.

exit: 0 done; 1 a path could not be put back (the message says which); 2 refused, nothing was
changed.

example: jst discard src/parser.go docs/draft.md
`
