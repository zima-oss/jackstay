package baseline

// help is `jst baseline --help`.
const help = usage + `
  --repo DIR             the project's checkout (required)
  --since, --until DAY   the first and last UTC day, YYYY-MM-DD (required)
  --project NAME         the project's name (default: its jackstay.json's, else the folder's)
  --config FILE          read the baseline section from FILE instead of the project's jackstay.json
  --kind NAME=REGEX      a kind of test run: a command matching REGEX (repeatable)
  --gate NAME            a kind that is a gate, timed for overlap (repeatable)
  --merge-unit pr|commit a merge is a subject ending in (#N), or each commit (alias --per)
  --branch B             the branch merges are counted on (default main)
  --transcripts GLOB     the session folders to read, by name (repeatable)
  --also NAME=DIR        another project whose gate runs count for overlap (repeatable)
  --projects-dir DIR     where Claude Code keeps sessions (default ~/.claude/projects)
  --jackstay-log FILE    read this run log instead of jackstay's own
  --examples FILE        write each command the guard would refuse to FILE (may hold private text)
  --json                 the report as one JSON object

exit: 0 the report; 1 the --examples file or the JSON report could not be written; 2 the request
(an option missing or wrong, a bad day, a jackstay.json, --config or --also that cannot be used).

example: jst baseline --repo . --since 2026-11-01 --until 2026-11-14
`
