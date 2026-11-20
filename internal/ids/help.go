package ids

// help is `jst id --help`.
const help = `usage: jst id next SERIES [--count N] [--json]
       jst id list [SERIES] [--json]
       jst id peek SERIES

Series come from jackstay.json's ids. A number is never handed out twice, whatever branch or
worktree asks.
  next SERIES   hand out the next number of SERIES, recorded
  --count N     with next: hand out N numbers (1 to 100)
  list          the numbers handed out, of every series or of SERIES
  peek SERIES   the number next would hand out; nothing is recorded
  --json        as JSON

exit: 0 done; 2 the request, the series, jackstay.json, git or the record cannot be used (no
unrecorded number is printed; one recorded before the failure is named as a gap).

example: jst id next W
`
