# Configuration reference

Every key of `jackstay.json`, the file at the root of an adopted project. `jst check` says
whether a file is usable; editors read the same rules from `schemas/jackstay.schema.json`. A key
left out takes its default. This page is generated from the code by `just reference`; change a
key's description in `internal/config/describe.go`, not here.

## Top-level keys

| Key | Type | What it does |
|---|---|---|
| `$schema` | string | Points editors at the JSON Schema of one jackstay release, for completion and early diagnostics; `jst check` stays the judge. |
| `jackstay_schema` | integer | Says which jackstay the file was written for (1 to 3), so an older jackstay refuses keys it does not know instead of half-reading them. One of `1`, `2`, `3`. |
| `project` | string | Names the project; without it the name is the main checkout's folder name. |

## `adopt`

Holds what `jst adopt` keeps in the project's file: the workflows' pin, the profile and the conflicts kept on purpose.

| Key | Type | What it does |
|---|---|---|
| `exceptions` | map of strings | Keeps adoption conflicts on purpose, each by its ID with the reason the project gives. |
| `pin` | string | Names the jst release the generated workflows download, moved only by `jst adopt --pin`. |
| `profile` | string | Names the profile `jst adopt` and `jst doctor` read, relative to the project's root unless it starts with `/` or `~/`. |
| `profile_sha256` | string | Pins the profile bundle to this SHA-256 digest (64 hex digits), so a later plan or apply refuses a changed bundle. |

## `baseline`

Says what `jst baseline` counts when it measures the project's test runs, merges and destructive commands.

| Key | Type | What it does |
|---|---|---|
| `branch` | string | Names the branch whose merges `jst baseline` counts. |
| `kinds` | list of objects | Lists the kinds of test run `jst baseline` counts; a Bash call takes the first kind one of its simple commands matches. |
| `kinds[].gate` | boolean | Marks the kind as one that stands before a merge, so `jst baseline` measures its overlap with other gate runs. |
| `kinds[].name` | string | Names the kind in the report; two kinds cannot share a name. |
| `kinds[].pattern` | regex | Matches one simple command of this kind (RE2). |
| `merge_unit` | string | Tells `jst baseline` what a merge is: `pr` counts first-parent subjects ending in `(#N)`, `commit` counts the branch's commits. |
| `transcripts` | list of strings | Names the Claude Code session folders `jst baseline` reads, as patterns over folder names without `/`. |

## `brief`

Sets the defaults of `jst brief`.

| Key | Type | What it does |
|---|---|---|
| `max_age_hours` | integer | Keeps a saved brief fresh for this many hours when `jst brief save` is given no `--max-age` (1 to 720, default 168). From 1 to 720; default 168. |

## `cards`

Turns on the `Card:` line: which commits must name their cards, where the cards are, and from when the rule holds.

| Key | Type | What it does |
|---|---|---|
| `branch` | string | Names the branch whose history `jst cards` reads and whose pushes the guard checks for `Card:` lines (default `main`). |
| `dir` | string | Names the folder of card files, written from the repository's root. |
| `exempt_branches` | list of strings | Lists head branches whose pull requests are judged by the tree's rules only, without `Card:` lines (a bot's branch). |
| `first_new` | string | Names the first card number under the new rules: cards numbered from it up keep `"commits": []`. |
| `id` | regex | Matches a card ID (RE2, without inline flags). |
| `since` | string | Reads commits dated on or after this day (YYYY-MM-DD) strictly, and earlier ones loosely. |
| `snapshots` | string | Names the folder where `jst cards snapshot --write` keeps what landed, for after a history rewrite. |
| `unit` | string | Says where `Card:` lines are written: `pr` in the pull request's description, `commit` in each commit's message. |

## `changes`

Keeps the changelog as one entry file per change, collected into the changelog at release.

| Key | Type | What it does |
|---|---|---|
| `dir` | string | Names the folder of changelog entry files, written from the repository's root. |
| `heading` | string | Gives the heading of each released section; it holds `{version}` and may hold `{date}`. |
| `internal_into` | string | Names the file that internal entries are collected into; without it they go only into the release commit's message and the release pull request. |
| `into` | string | Names the changelog file that entries are collected into. |
| `newest` | string | Puts the newest section `first` or `last` in the changelog (default `last`). |
| `template` | string | Names the file `jst changes new` copies for a new entry, with `{card}` and `{slug}` filled in. |

## `chat`

Turns on session chat between agent sessions of this repository.

| Key | Type | What it does |
|---|---|---|
| `claude_code` | object | Configures the Claude Code hook that announces pending chat messages at the next boundary. |
| `claude_code.enabled` | boolean | Lets `jst chat claude-hook` announce pending messages to Claude Code; it needs `chat.enabled`. |
| `codex_bridge` | object | Configures the bridge that delivers chat messages into a managed Codex turn. |
| `codex_bridge.enabled` | boolean | Lets `jst chat codex-bridge` deliver messages to a running Codex turn; it needs `chat.enabled`. |
| `enabled` | boolean | Turns session chat on for this repository; chat commands refuse to run without it. |

## `github`

Says which GitHub account, remote and repository jackstay uses, and how long its GitHub commands wait.

| Key | Type | What it does |
|---|---|---|
| `account` | string | Names the GitHub login jackstay acts as outside GitHub Actions, whatever account `gh` has active. |
| `remote` | string | Names the remote that `jst sync`, `jst start`, `jst tidy` and `jst land` use; without it they take `origin`, else the remote `main` (then `master`) tracks, else the only remote. |
| `repository` | string | Names the repository as `owner/name` when it is not to be read from the remote's URL. |
| `wait` | object | Sets the defaults of `jst checks wait`, `jst checks run-wait` and `jst land`; their flags still win. |
| `wait.interval_seconds` | number | Waits this many seconds between looks at GitHub (default 20). |
| `wait.log_lines` | integer | Shows this many lines of a failed step's log (default 25). |
| `wait.timeout_seconds` | number | Gives up waiting on GitHub after this many seconds (default 1800). |

## `guard`

Adapts the guard to the project: extra regenerable folders, the project's own rules, and each built-in rule's level.

| Key | Type | What it does |
|---|---|---|
| `deny` | list of objects | Adds the project's own rules, each refusing commands its patterns match and shown as `project:<id>`. |
| `deny[].command` | regex | Refuses a command whose words, joined by spaces, match this pattern (RE2). |
| `deny[].env` | map of regexes | Applies the rule only when each named environment variable's whole value matches its pattern (RE2), or cannot be known. |
| `deny[].id` | string | Names the rule in refusals as `project:<id>` (default `rule`). |
| `deny[].instead` | string | Tells the agent what to run instead (default "ask the owner"). |
| `deny[].unless` | regex | Lets a command through when its words, joined by spaces, match this pattern (RE2). |
| `deny[].why` | string | Says why the rule refuses (default "this project refuses it (jackstay.json)"). |
| `regenerable` | list of strings | Adds folder names that may always be deleted inside the project, beside the built-in ones (`.build`, `node_modules`, …); `jst tidy` reads them too. |
| `rules` | map of objects | Sets the level and path exceptions of built-in guard rules, by rule id. |
| `rules.*` | object | Sets one built-in rule's level and, for `rm-recursive` and `find-delete`, its path exceptions. |
| `rules.*.level` | string | Makes the rule `refuse`, `warn` (the command runs and the agent is told) or `off` (default `refuse`, `warn` for `zsh-glob`). |
| `rules.*.paths` | list of strings | Allows a recursive delete under these folders, written below the project's top or below `~/` (only `rm-recursive` and `find-delete`). |

## `ids`

Declares the number series that `jst id` hands out, by series name.

| Key | Type | What it does |
|---|---|---|
| `*` | object | Defines one number series: where its files are and how its numbers are written. |
| `*.also_in` | list of strings | Names files whose numbers of this series also count as taken (an old ledger's rows), written from the repository's root. |
| `*.files` | string | Gives the path pattern of the series' files, with `{n}` once in the file's name where the number is. |
| `*.width` | integer | Pads the number with zeros to this many digits, 1 to 9 (default 1). |

## `land`

Says how `jst land` lands a change: who merges, which method, which checks are required and which paths only a person lands.

| Key | Type | What it does |
|---|---|---|
| `autonomy` | string | Chooses who merges: `merge-when-green` (the agent, when the required checks pass and no human path is touched) or `prepare` (a person). |
| `checks` | list of strings | Names checks required in addition to the base's rules, never fewer. |
| `host` | string | Names the code host's adapter (`github`); unset, the push remote's host. |
| `human_paths` | list of strings | Lists globs of paths only a person lands, besides jackstay.json, CI workflows and .claude/settings.json. |
| `method` | string | Chooses the merge method asked of the host: `squash` (the default), `merge` or `rebase`. |

## `lock`

Turns on the machine-wide lock that heavy test runs take in turn.

| Key | Type | What it does |
|---|---|---|
| `also_hold` | list of objects | Names older lock files that a locked run holds together with the shared lock. |
| `also_hold[].held_env` | string | Names the environment variable set for the command while the older lock is held, so the older script does not wait for itself. |
| `also_hold[].path` | string | Gives the older lock's file, an absolute path or one beginning `~/`. |
| `enabled` | boolean | Makes `jst test` take the machine-wide lock for this project (off by default; `--lock` and `--no-lock` decide one call). |

## `procs`

Sets the deadlines of `jst bg`.

| Key | Type | What it does |
|---|---|---|
| `default_minutes` | number | Stops a `jst bg` command after this many minutes when `--for` is not given (default 30). |
| `max_minutes` | number | Caps any `jst bg` deadline at this many minutes (default 240). |

## `receipts`

Says what goes into a tree's key and which commands may reuse a receipt of a pass instead of running again.

| Key | Type | What it does |
|---|---|---|
| `commands` | list of objects | Lists commands that `jst test -- COMMAND` judges, keys and may reuse, matched by their words. |
| `commands[].argv` | list of strings | Matches the command by all its words exactly; reuse needs it. |
| `commands[].argv_prefix` | list of strings | Matches every command that begins with these words, the longest match winning; it cannot be reused. |
| `commands[].empty_exit` | integer | Reads this exit status (1 to 125) as no tests collected when the JUnit report confirms zero cases (`junit` only). |
| `commands[].evidence` | regex | Counts the tests a run passed from the lines this pattern matches (RE2, one group for the number); without a count no run is `passed`. |
| `commands[].judge` | string | Judges the run by a structured report: `go-json`, `junit`, `jest-json`, `trx` or `xcresult-json`. |
| `commands[].merge_bases` | list of strings | Adds the merge base of HEAD with each of these refs to this command's key. |
| `commands[].refs` | list of strings | Adds the commit each of these refs names to this command's key. |
| `commands[].report` | string | Names the report file a file-based judge reads from the run's private report folder. |
| `commands[].reports` | list of strings | Names several report files instead of `report`: up to 32 for `junit`, or `summary.json` and `tests.json` for `xcresult-json`. |
| `commands[].reuse` | boolean | Prints a stored receipt and runs nothing when this command already passed on the same key. |
| `commands[].veto` | regex | Fails a run that ended 0 when a line of its output matches this pattern (RE2). |
| `commands[].xcresult_bundles` | list of strings | Names every unit test bundle an `xcresult-json` report must show. |
| `env` | list of strings | Puts the environment variables whose names match these names or patterns (`APP_*`) into the key; reuse needs it written out, even empty. |
| `exclude` | list of strings | Leaves files matching these patterns, written from the root, out of the key's inputs. |
| `extra_files` | list of strings | Adds these files to the key, each one readable file, also ones git ignores. |
| `include_head` | boolean | Adds HEAD's commit to the key. |
| `inputs` | list of strings | Names the paths whose files make the key: the tracked ones and the untracked ones the committed `.gitignore` files do not ignore; without it there is no key. |
| `max_age_hours` | number | Uses a receipt for at most this many hours (default 72). |
| `merge_bases` | list of strings | Adds the merge base of HEAD with each of these refs to every command's key. |
| `no_reuse_branches` | list of strings | Always runs the command on these branches and on the commits they point at (default `main` and `master`). |
| `refs` | list of strings | Adds the commit each of these refs names to every command's key. |
| `toolchain` | list of list of strings | Adds what each of these commands prints (`[["go", "version"]]`) to the key; reuse and scope steps need it written out, even empty. |

## `records`

Keeps the records of these series as one file each, rendered by `jst view`.

| Key | Type | What it does |
|---|---|---|
| `*` | object | Defines where one series' record files live and how `jst view` shows them; the series needs an `ids` entry of the same name. |
| `*.before` | string | Names the series' old ledger file, kept frozen: `jst lint tree` refuses a new row in it. |
| `*.columns` | list of strings | Lists the front matter fields `jst view` shows as columns (default `id`). |
| `*.dir` | string | Names the folder of the series' record files, written from the repository's root. |
| `*.template` | string | Names the file `jst new` copies for a new record. |

## `release`

Turns on releases made by a release pull request: versions, tag and changelog are written by `jst release`.

| Key | Type | What it does |
|---|---|---|
| `base` | string | Names the branch releases are made from and land on (default `cards.branch`, else `main`). |
| `branch` | string | Names the release pull request's head branch (default `jackstay/release`). |
| `build` | object | Names an app's build number file and its pattern, with one named group `build`. |
| `build.match` | regex | Finds the build number in the file (RE2, multi-line, one group named `build`). |
| `build.path` | string | Names the build number file, written from the repository's root. |
| `files` | list of objects | Lists the version files a release rewrites; at least one is required. |
| `files[].match` | regex | Finds the version in the file (RE2, multi-line, one group named `version`). |
| `files[].path` | string | Names the version file, written from the repository's root. |
| `first` | string | Gives the version the version files hold when the section is added; tags at or below it are history jackstay does not read. |
| `merge` | string | Says who merges the release pull request: `owner` (it opens as a draft) or `agents` (default `owner`). |
| `tag` | string | Names the release tag, holding `{version}` once (default `v{version}`). |

## `review`

Configures `jst review`: the reviewers, what they are told, and how findings decide a round.

| Key | Type | What it does |
|---|---|---|
| `blocking` | list of strings | Lists the severities that fail a round (default `blocker`). |
| `brief` | string | Names the project's standing brief, read from the reviewed tree and put at the top of every prompt. |
| `cite` | list of strings | Lists files and folders (trailing `/`) the reviewer is told to read as the measure; each must exist in the reviewed tree (default `AGENTS.md` when it exists). |
| `keep_days` | integer | Removes a task whose newest round is older than this many days, 1 to 3650 (default 30). |
| `max_concurrent` | integer | Lets this project's reviews hold at most this many reviewer runs at once on the machine (default 4). |
| `queue_minutes` | integer | Waits at most this many minutes for a source view, reviewer slots and the start lock, 1 to 120 (default 15). |
| `repeat_rounds` | integer | Says a place is to be redesigned once it draws a finding this many rounds running, at least 2 (default 2). |
| `repeat_severities` | list of strings | Lists the severities that count as repeats for redesign (default every severity but the last). |
| `reviewers` | map of objects | Overrides the built-in reviewers' settings, by name: `codex` or `claude-code`. |
| `reviewers.*` | object | Overrides one built-in reviewer's command, model, effort, deadline and isolation flags. |
| `reviewers.*.command` | string | Runs the reviewer as this program, a name on `PATH` or an absolute path (default `codex` for Codex, `claude` for Claude Code). |
| `reviewers.*.effort` | string | Sets the reviewer's reasoning effort; empty passes none (default `medium` for Codex, the CLI's own for Claude Code). |
| `reviewers.*.isolation` | list of strings | Replaces Codex's default isolation flags with these words; words jackstay owns are refused, and Claude Code takes none. |
| `reviewers.*.model` | string | Passes this model to the reviewer; empty keeps the CLI's own default. |
| `reviewers.*.timeout_minutes` | integer | Stops one reviewer run after this many minutes, 1 to 120 (default 20). |
| `round_limit` | integer | Stops after this round and asks the owner, at least 2 (default 6). |
| `severities` | list of strings | Lists the severity words a finding may carry, most severe first (default `blocker`, `major`, `minor`). |
| `store` | string | Keeps review results in this folder, an absolute path or one beginning `~/`; empty keeps them under `~/.jackstay/<project>/reviews`. |
| `template` | string | Replaces jackstay's prompt template with this file (Go `text/template`), read from the reviewed tree. |
| `triage` | boolean | Sorts each finding by likelihood and consequence, so only realistic ones block; off, every finding of a blocking severity blocks (default true). |
| `use` | list of strings | Lists the reviewers a round runs when `--reviewer` is absent, and those `jst review check` requires (default `codex`). |

## `status`

Sets the defaults of the workspace overview, `jst workspace board`.

| Key | Type | What it does |
|---|---|---|
| `repos` | list of strings | Lists the repositories the overview reads when none are named: paths relative to this file's folder, or absolute. |

## `telemetry`

Sets how long the local journal is kept and how command text is recorded in it.

| Key | Type | What it does |
|---|---|---|
| `commands` | string | Records command text `masked` (secrets hidden) or as a `skeleton` (only its shape) (default `masked`). |
| `compress_after_days` | integer | Gzips a journal day file older than this many days, 1 to 3650 (default 14). |
| `keep_days` | integer | Deletes a journal day file older than this many days, 1 to 3650 and not below `compress_after_days` (default 180). |

## `test`

Declares the project's test scopes and steps, their judges and timeouts, run by `jst test <scope>`.

| Key | Type | What it does |
|---|---|---|
| `expect` | list of strings | Lists the engines (`xctest`, `swift-testing`) every `swift-test` scope must show markers of, unless the scope sets its own (default `xctest`). |
| `judge` | string | Judges every scope and step that sets no judge of its own (default `exit`). One of `swift-test`, `steps`, `exit`, `go-json`, `junit`, `jest-json`, `trx`, `xcresult-json`. |
| `leftovers` | list of objects | Lists file globs a test run must not leave behind. |
| `leftovers[].fail` | boolean | Fails the run when its leftovers match the glob; false lists them. |
| `leftovers[].glob` | string | Matches leftover files; `~`, `$TMPDIR`, `{run_id}` and `{run_tmp}` are expanded, and a glob holding the run's token is attributed exactly. |
| `scopes` | map of objects | Declares the scopes `jst test <scope>` runs: `focused`, `changed`, `quick`, `full`, `display`, `gate`, or `x-` extras. |
| `scopes.*` | object | Defines one scope: the command it runs or the steps it lists, how it is judged, and when the project runs it. |
| `scopes.*.base` | string | Gives the ref that `{base}` in the words stands for; `--base REF` overrides it. |
| `scopes.*.cwd` | string | Runs the command in this folder of the checkout instead of its root. |
| `scopes.*.durations` | boolean | Records each test class's seconds for `{slow}` (default on for `full` and `display` under `swift-test`). |
| `scopes.*.empty_exit` | integer | Reads this exit status (1 to 125) as no tests collected when the JUnit report confirms zero cases (`junit` only). |
| `scopes.*.env` | map of strings | Sets these environment variables for the run; they are recorded. |
| `scopes.*.evidence` | regex | Counts the tests a run passed from the lines this pattern matches (RE2, one group for the number); the `steps` judge needs it. |
| `scopes.*.expect` | list of strings | Lists the engines (`xctest`, `swift-testing`) whose markers a `swift-test` run must show (default `test.expect`, else `xctest`). |
| `scopes.*.judge` | string | Says how the run's outcome is read: `swift-test`, `steps`, `exit`, `go-json`, `junit`, `jest-json`, `trx` or `xcresult-json` (default `test.judge`, else `exit`). |
| `scopes.*.lock` | boolean | Takes the machine-wide lock when `lock.enabled` is true (default true, false for `focused` and `quick`). |
| `scopes.*.lock_wait` | number | Gives up waiting for the lock after this many seconds (default the scope's timeout). |
| `scopes.*.report` | string | Names the report file a file-based judge reads; `{report}` in the words gives its path. |
| `scopes.*.reports` | list of strings | Names several report files instead of `report`: up to 32 for `junit`, or `summary.json` and `tests.json` for `xcresult-json`. |
| `scopes.*.requires` | list of objects | Lists checks run first; one that fails or times out makes the outcome `not-started` and nothing runs. |
| `scopes.*.requires[].run` | list of strings | Runs these words as the check. |
| `scopes.*.requires[].timeout` | number | Stops the check after this many seconds (default 60). |
| `scopes.*.requires[].why` | string | Says what the run reports when the check fails. |
| `scopes.*.resume` | boolean | Lets `jst test gate --resume` skip this step after a pass on the same tree; false always runs it (default true). |
| `scopes.*.run` | list of strings | Runs these words, with placeholders such as `{tests}`, `{base}`, `{slow}` and `{report}` filled in. |
| `scopes.*.selector` | string | Expands one test ID into the runner's filter: `go-test` or `jest-test`, for a `focused` or `x-` scope. |
| `scopes.*.slow` | object | Defines which test classes `{slow}` skips, from their recorded durations. |
| `scopes.*.slow.case_seconds` | number | Skips a class whose median slowest case takes at least this many seconds. |
| `scopes.*.slow.class_seconds` | number | Skips a class whose median total takes at least this many seconds. |
| `scopes.*.steps` | list of strings | Runs these steps or scopes in order instead of `run`, stopping at the first that does not pass. |
| `scopes.*.timeout` | number | Stops the whole process tree after this many seconds of the command's run (default `test.timeout`). |
| `scopes.*.veto` | regex | Fails a run when a line of its output matches this pattern (RE2), whatever its status. |
| `scopes.*.when` | string | Says when the project runs this scope, one line that `jst test list` prints. |
| `scopes.*.xcresult_bundles` | list of strings | Names every unit test bundle an `xcresult-json` report must show. |
| `selection` | object | Lets `jst test changed` choose a scope from the paths changed since a base. |
| `selection.order` | list of strings | Orders the scopes from least to most strict, ending with `gate`; the strictest scope any rule picks wins. |
| `selection.rules` | list of objects | Maps changed paths to scopes; a path no rule matches selects `gate`. |
| `selection.rules[].glob` | string | Matches changed paths, written from the repository's root; `**` stands for whole path segments. |
| `selection.rules[].scope` | string | Names the scope a matching path selects, one of `test.selection.order`. |
| `steps` | map of objects | Declares the steps a scope's `steps` lists, with the same keys as a scope. |
| `steps.*` | object | Defines one step: the command it runs or the steps it lists, and how it is judged. Takes the keys of `test.scopes.*`. |
| `timeout` | number | Stops a scope or step without a timeout of its own after this many seconds. |

## `trash`

Sets how `jst trash` keeps what it removes.

| Key | Type | What it does |
|---|---|---|
| `keep_days` | integer | Keeps a trash entry for this many days (default 7). |
