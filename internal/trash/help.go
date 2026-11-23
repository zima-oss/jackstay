package trash

// help is `jst trash --help`.
const help = usage + `

By default (trash.keep off) each item is deleted at once, and the deletion is logged: its path,
kind, size, SHA-256, project, session and command; ` + "`jst trash list`" + ` shows the log. With
trash.keep on (experimental), each item is kept as an entry for trash.keep_days (default 7), and
` + "`jst trash restore`" + ` puts it back; older entries are removed on the way.
  PATH...        files or folders to delete; -- before them lets a path be named list, restore,
                 prune or start with -
  --json         with list: as JSON, {"kept": [...], "deleted": [...]}
  --to PATH      with restore: put the item back at PATH instead of where it was

exit: 0 done; 1 a path could not be deleted or kept, or no such entry; 2 a bad request.

example: jst trash build/old-output
`
