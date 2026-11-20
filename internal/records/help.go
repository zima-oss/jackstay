package records

// newHelp is `jst new --help`.
const newHelp = newUsage + `

SERIES is one of jackstay.json's records. Takes the next number from ` + "`jst id next`" + `, fills id
(and card) into the series' template, and prints the file's path.
  --slug SLUG        the file name's words after the number (required)
  --card ID          the card the record is for (needs a cards section)
  --set KEY=VALUE    a field of the template set to VALUE (repeatable; not id)

exit: 0 written, its path printed; 2 the request, jackstay.json, the counter, the template or the
records folder cannot be used, or the file exists (its number is spent, a gap); no file is written.

example: jst new W --slug rename-the-lock
`

// viewHelp is `jst view --help`.
const viewHelp = viewUsage + `

  --where KEY=VALUE   only records whose field KEY is VALUE (repeatable; all must hold)
  --format FORMAT     table (default), list or json

exit: 0 printed; 1 a record file could not be read (named on standard error; the other rows are
printed); 2 the request, jackstay.json or the records folder cannot be used.

example: jst view W --where status=open
`
