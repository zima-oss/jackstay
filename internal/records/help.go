package records

// newHelp is `jst new --help`.
const newHelp = newUsage + `

A record of SERIES (jackstay.json's records) as a file of its own: the next number from the counter
(` + "`jst id next`" + `), the series' template with id (and card) filled in. The file's path is printed.
  --slug SLUG        the file name's words after the number (required)
  --card ID          the card the record is for (needs a cards section)
  --set KEY=VALUE    a field of the template set to VALUE (repeatable; not id)

exit: 0 written, its path printed; 2 the request, jackstay.json, the counter, the template or the
records folder cannot be used, or the file exists (its number is spent, a gap); no file is written.

example: jst new W --slug rename-the-lock
`

// viewHelp is `jst view --help`.
const viewHelp = viewUsage + `

The records of SERIES as a table, printed; nothing is written.
  --where KEY=VALUE   only records whose field KEY is VALUE (repeatable; all must hold)
  --format FORMAT     table (default), list or json

exit: 0 printed; 1 a record file could not be read (the other rows are printed, each such file named
on standard error); 2 the request, jackstay.json or the records folder cannot be used.

example: jst view W --where status=open
`
