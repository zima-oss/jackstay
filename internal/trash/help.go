package trash

// help is `jst trash --help`.
const help = usage + `

Entries older than trash.keep_days (default 7) are removed on the way.
  PATH...        files or folders to move; -- before them lets a path be named list, restore,
                 prune or start with -
  --json         with list: as JSON
  --to PATH      with restore: put the item back at PATH instead of where it was

exit: 0 done; 1 a path could not be moved, or no such entry; 2 a bad request.

example: jst trash build/old-output
`
