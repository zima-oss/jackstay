package wait

// help is `jst wait --help`: the usage lines, then each option.
const help = usage + `

Exactly one condition:
  --pid PID          until that process has ended
  --file PATH        until the file exists
  --nonempty         with --file: until it exists and holds something
  --gone PATH        until the file or folder is gone
  --line REGEX       until a line of the --in file matches (the line is printed)
  --quiet SECONDS    until the --in file has not grown for that long
Common options:
  --in PATH          the regular file --line, --quiet and --fail read (it can start out missing)
  --fail REGEX       end with 1 as soon as a line of the --in file matches
  --while-pid PID    end with 1 when that process (the writer) ends first
  --new              count only lines written after jst wait started
  --timeout SECONDS  the deadline (default 540)
  --tail N           how many of the file's last lines to print on the way out (default 20)

Pattern matching ends with exit 1 if a line is longer than 1 MiB.
exit: 0 it happened; 1 it cannot happen any more, or the log ended the wait in failure;
2 the request is wrong; 3 the deadline came first.

example: jst wait --line 'listening on' --in server.log --fail 'error' --timeout 60
`
