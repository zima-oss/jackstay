package owner

// help is `jst owner --help`: the usage lines, then each option.
const help = usage + `

  --session S|all  which session: a substring of its file's name, or all (default: the newest)
  --kind KIND      only typed, queued, widget (an answer to a question) or declined messages
  --grep RE        only messages matching RE (RE2, case-insensitive)
  --since D        only messages from that ISO time prefix on (2026-09-11)
  --max-chars N    only messages of at most N characters
  --stats          where the session's tool calls went, instead of the messages

exit: 0 read (also when the filters match nothing, said so); 1 nothing to read (no transcript
folder, no matching session file, an empty one) or a file that cannot be read; 2 a bad request;
5 the transcripts are in a shape jackstay does not know (also: no message of the owner's, or
--stats finding no tool call); 6 a widget answer in a spelling jackstay does not know.

example: jst owner --session all --grep 'force-push'
`
