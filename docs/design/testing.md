# Testing: scopes, a judge, a timeout, a gate — `jst test <scope>`

Decisions: [W-012](../decisions/W-012-test-scopes-vocabulary.md) (one vocabulary of scopes; each
project defines its selection) and [W-013](../decisions/W-013-quality-testing-declarative.md) (no
time-sensitive tests; everything declarative and general). Status: built, except that 0.6.0 runs
scopes as a graph with a cache by input hash ([land.md](land.md) section 7, after
[W-096](../decisions/W-096-task-graph-and-input-cache.md)), which section 5 summarises and which
is not built yet. How to use scopes is the
[tests guide](../guide/tests.md); the raw form `jst test -- COMMAND`, its outcomes, receipts and run
record are [implementation.md](implementation.md) section 9. The
[compatibility baseline](testing-compatibility.md) pins the outcomes; the
[multi-runner proposal](testing-multirunner-proposal.md) is the path to structured evidence.

## The problem

Each project otherwise grows its own ladder of scopes, with the rule for when each runs written in
several places. A regex over `Executed N tests` over-counts a Swift run (every enclosing suite
prints a total), fails filtered runs (`Executed 0 tests` per excluded bundle), misses a bundle that
never ended and does not count Swift Testing. A hung suite stopped through its wrapper can leave
`xctest` running. Leftover files fail later runs, and hand-kept slow lists drift.

## 1. Scopes

`jst test plan changed --base REF` resolves a scope from a committed range and, in the working tree, its
uncommitted and untracked paths. The order and path rules
come from `test.selection`. Empty ranges and paths without rules select `gate`. Rename endpoints
and deleted paths participate. With selection configured, `jst test changed` dispatches through
the chosen scope's execution and receipt policy. An explicit `BASE..REV` runs on a clean copy of
`REV`. See [path-based selection](../guide/tests.md#choose-a-scope-from-changed-paths).

`jst test <scope>` runs a scope; `jst test list` lists the project's scopes (name, what it runs,
when the project runs it); `jst test plan <scope>` prints the words that would run, step by step,
and runs nothing. Flags: `jst test --help`.

### The vocabulary

Six names, each with a meaning jackstay documents and every project uses the same way. A project
declares the ones it has; a name it does not declare is refused with the list of those it has.
Extra names are allowed only with the prefix `x-` (`x-coverage`), so the six never drift.

| Scope | Meaning | Typical cost |
|---|---|---|
| `focused` | the tests named on the command line (`jst test focused A B`) | seconds–minutes |
| `changed` | what a change since a base can affect, chosen by the project's own selector | minutes |
| `quick` | a broad look: everything a headless run can do, less the slowest classes | minutes |
| `full` | the whole suite a headless run can do | the longest single run |
| `display` | the sets that need a real screen, device or other environment (`requires`) | varies |
| `gate` | every scope a change must pass to merge: a scope with no `run` whose `deps` are those scopes | the longest |

When each is required is the project's rule, written **once**, in the scope's `when` text, which
`jst test list` prints; jackstay does not choose the cadence.

### Declaring a scope

The optional `test` section, abridged (use: the [tests guide](../guide/tests.md#declare-scopes)):

```json
"test": {
  "judge": "swift-test", "timeout": 1800,
  "leftovers": [{"glob": "~/Library/Preferences/ExampleTests.*.plist", "fail": true}],
  "scopes": {
    "focused": {"run": ["swift", "test", "--filter", "{tests}"], "when": "while working"},
    "quick":   {"run": ["swift", "test", "--skip", "{slow}"], "slow": {"class_seconds": 20}},
    "gate":    {"steps": ["format", "quick"], "when": "before every pull request"}
  },
  "steps": {"format": {"run": ["swift", "format", "lint", "-r", "Sources"], "judge": "exit"}}
}
```

Keys of a scope (all optional except `run` or `steps`):

- `run`: the words. Placeholders, whole words or inside one: `{tests}` (`focused`'s ARGS, escaped,
  one alternation: `Target.Class/test` exactly; `Target.Class` → `^Target\.Class(/|$)`; `Class` →
  `(^|\.)Class(/|$)`; refused without ARGS), `{base}` (the scope's `base` or `--base REF`), `{slow}`
  (section 4), `{report}` (section 2.1). A placeholder is `{name}` of lower-case letters and `_`,
  not after `$`; an unknown one is an error. Other braces (`${VAR}`, `{2,3}`) are literal.
- `steps`: instead of `run`, a list of step names (`steps` section) or scope names, read as `deps`.
- `deps`, `ci`, `systems`, `inputs`, `cache`: what must pass first, where CI runs the scope, where
  it runs at all, the files its cache hash reads, and whether it is cached;
  [land.md](land.md) section 7.1.
- `judge`: `swift-test` (section 2), `steps` (a count line declared by `evidence`),
  `go-json`, `junit`, `jest-json` and `trx` (section 2.1), or `exit` (status only; the outcome is
  `exited-0` on 0, never `passed`). Default: the section's `judge`, else `exit`.
- `evidence`, `veto`: as in `receipts.commands`, **added** to legacy judges. Structured judges
  accept a veto and count only structured evidence.
- `report`: a file-based judge's report name. JUnit can use `reports` instead for a bounded list
  of file names, and `empty_exit` for an optional no-collection exit status.
- `timeout` (seconds of the command's own run) and `lock_wait` (seconds waiting for the lock,
  counted apart; default: the scope's `timeout`); section 3.
- `requires`: checks run first, each with its own `timeout` (default 60 s) and the tree stop of
  section 3; one that fails or times out makes the outcome `not-started` with its `why`.
- `env`: variables set for the run, recorded.
- `lock`: whether the scope takes the machine-wide lock when `lock.enabled` is true (default true
  for `full`, `display`, `changed`, steps and `x-` scopes; false for `focused`, `quick`); a scope
  with no `run` takes none, and each of its deps takes it by its own `lock`.
- `when`: one line of text for `jst test list`.

`jst check` validates the section like the rest of `jackstay.json`; `jst test plan` shows the result.

## 2. The `swift-test` judge

It reads both streams, escape sequences removed, to their end, and requires, for `passed`:

1. **XCTest bundles.** Every `Test Suite '<name>.xctest' started` has its matching `… passed|failed
   at` and its `Executed N tests, with F failures` line, or the run is `unjudged`.
2. **Counts, per engine.** XCTest: the sum of each bundle's own total, executed less skipped.
   Swift Testing: a run that started (`◇ Test run started`) must reach its summary (`✔`/`✘ Test run
   with N tests …`), or the run is `unjudged`. Its count is its `✔ Test … passed` / `✘ Test …
   failed` lines; a summary of `0 tests`, or one whose N are all `skipped` lines, adds 0; per-test
   lines that do not account for N make it `unjudged`. A bundle with 0 is normal (a filter). The
   **run** must count above 0 in either engine, or it is `nothing-run` (76).
2a. **Output SwiftPM hides.** `swift test --parallel` prints no passing test's lines, and its xUnit
   file counts an `exit(0)` as a pass and shows no skips, so a `swift-test` scope with `--parallel`
   is an error in `jackstay.json`. Behind a wrapper, every engine in `expect` (default
   `["xctest"]`) must show its markers (bundle start and total lines; Swift Testing's start and
   summary); missing markers, or parallel progress lines (`[N/M] Testing …`), make it `unjudged`.
3. **Failures.** Any bundle with F > 0, any `Test Suite '…' failed`, any `Test Case '…' failed`,
   any `✘ Test … failed` / `✘ Test run … failed`, or a status other than 0 makes it `failed`.
4. **After the end.** An XCTest `Test Case '…' started` after every bundle ended makes it
   `unjudged`; anything else may follow. A bundle's total must be the line right after its end.
5. **Project vetoes** (`veto`) and `leftovers` with `"fail": true` (section 6) add failures.
6. **Order of verdicts.** A proven failure (a status the command ended with **by itself** that is
   not 0, a failure line, a veto, an attributed leftover) makes the outcome `failed` even when
   something else could not be judged (an unattributed leftover, a bundle that did not end):
   `failed` > `timed-out` > `stopped` > `unjudged` > `nothing-run` > `passed`. A status caused by
   jackstay's own stop (the timeout, or a signal sent to `jst test`) is not a failure: such a run is
   `timed-out` or `stopped` unless a failure line or veto was seen before the stop. A `failed` run always writes its failure note, and the failure note of its cache hash.

The judge prints its reading as the last line's detail:
`jst test: outcome=passed (… s, counted=2290 bundles=9/9 xctest=2290 swift-testing=0 skipped=1)`
(`counted` is the sum of every bundle's own total, less the skipped).
`failed` adds the first failing case; `unjudged` says which bundle did not end.

The patterns are one table in `internal/judge`, tested against anonymised recorded outputs.
`expect` must name only engines a scope's words really run (an engine a filter excludes prints
nothing).

### 2.1. Structured evidence

`go-json` reads stdout from `go test -json -count=1`. It counts executed leaf test cases,
reports skips separately, and needs a complete stream with package terminal events. A successful
package summary alone adds no test cases. Cached replay, incomplete output and invalid event
sequences cannot establish a new pass.

`junit`, `jest-json` and `trx` read regular files in a private directory allocated for the attempt.
Declare the file name in `report`. In a scope, use `{report}` in an argument or read
`JACKSTAY_TEST_REPORT` from the environment. JUnit also accepts `reports`, an explicit list of
up to 32 distinct file names. Pass its directory with `{report_dir}` or
`JACKSTAY_TEST_REPORT_DIR`. Each listed report must validate, and a JUnit set cannot repeat a
case identity across files. An identity includes the suite path, class and case name. The
combined set may have at most 100,000 distinct cases, with up to 4096 bytes per identity.
The project's command defines the partitions, checks their exits and maps every declared
file to one partition. Direct commands declare settings in
`receipts.commands` and their wrappers read the environment variable. Missing, malformed,
contradictory and oversized reports make a successful process `unjudged`. A valid report
with zero executed cases yields `nothing-run`. For a producer such as pytest, set
`empty_exit: 5` to recognise a nonzero no-collection exit only when its report confirms
zero cases. Other nonzero statuses retain their failure semantics. Jest discovery does not
establish execution. TRX reads one completed VSTest summary and checks it against the test cases.

Scopes may opt into `selector: "go-test"` with the canonical `go test -json -count=1`
invocation and whole-argument `{test_args}`. The selector expands one package-qualified test ID
into an exact per-component Go filter and an import path. Go JSON evidence must witness a
matching test or descendant; ancestor setup alone is a no-match. The `jest-test` selector
expands a relative file and literal full assertion name. Jest evidence must identify the
selected file and a unique matching assertion. An unrelated witnessed result makes an otherwise
successful invocation unjudged. Typed selectors require a standalone `focused` or `x-…` scope.

Go JSON and JUnit scope timings are an opt-in run-log field bounded to 512 samples from a
counted fresh pass. JUnit uses the reported case `time`, with its suite path, class and test
name. Unavailable timings are left out. Swift class selection uses its existing history.

The format name, adapter version and semantic report settings are keyed. The allocated report
path is transport only. Receipt lookup recomputes the saved material's digest and requires a
positive count. Changing the adapter version changes the key without rewriting the historical
records. A scope's cache entry ([land.md](land.md) section 7.3) carries the adapter version and the
counted pass, so a hit needs no report file after the private report directory is removed.
The [producer guide](../guide/tests.md#structured-test-reports) lists the pinned probes. The
[compatibility baseline](testing-compatibility.md) records the original exit and reporting contracts.

## 3. A timeout that ends the whole run

A scope's command runs in a session of its own and is stopped as a session tree: how its members
are found, remembered by id and start time, and signalled is
[implementation.md](implementation.md) section 5.2. What is particular to a scope:

- The session's members are found by listing every pid (`proc_listallpids` on macOS) and asking
  each its session with `getsid(pid)`; a pid whose `getsid` fails is skipped and said.
- At `timeout` seconds of the command's run: SIGTERM to the group, the session and the remembered
  descendants; after a grace of 10 s, SIGKILL to what is left. The same stop is used when
  `jst test` itself is asked to stop.
- **After a normal end, too**: when the command has ended (any status), whatever is left of its
  session and of the remembered descendants is stopped the same way **before** the lock is let go
  and the outcome is written. Processes that had to be stopped are named in the line
  (`left-running=…`), and the run is not `passed` (`unjudged`, 77; `failed` stays `failed`): a
  test run leaves nothing running.
- The outcome is `timed-out` (exit 124), with the seconds and what was killed; any process still
  alive after SIGKILL is named in the outcome line (`survivors=…`). Never a pass, never a receipt.
- `lock_wait` is counted apart: a scope with `timeout: 5` waits at most its `lock_wait` for the
  lock, then `gave-up` (75). The wait and the run are both in the record.
- Without a `timeout` (none for the scope, none for the section) a scope runs unbounded and
  `jst test list` says so; `jst check` warns.

## 4. Durations per class, and the `quick` scope

- Runs of scopes that declare `"durations": true` (default: `full` and `display`; never `focused`,
  `changed` or `quick`, whose runs hold part of a class) record each XCTest case's seconds from
  `Test Case '-[Target.Class test]' passed|failed (S seconds)`, per class, into
  `<jackstay home>/<project>/durations.jsonl` (one line per run: run id, scope, head, per class: total
  seconds, slowest case, and the set of test names seen). A class's numbers are taken only from
  runs whose set of tests for it equals the largest set recorded for it (a run that ran part of a
  class does not count). Kept for the last 20 such runs.
- Swift Testing tests are not in the durations: its per-test lines do not name the suite
  unambiguously (two suites' `testX()` print alike; swift-testing issue 841). They are never
  skipped by `{slow}` until jackstay reads Swift Testing's structured event stream.
- A class not seen in the last 20 runs has no data, not zero time: it is not skipped.
- `{slow}` expands to a `--skip` regex of the classes that are slow by the scope's `slow` rule:
  `class_seconds` (median class total over the recorded runs at or above it) and/or
  `case_seconds` (median slowest case at or above it). Medians, so one run under load does not
  move a class in or out.
- With no recorded runs yet, `{slow}` expands to a pattern that skips nothing, and the outcome
  line says `quick=everything (no durations yet)`.
- `jst test plan quick` prints the classes skipped and their medians.

A slow class is skipped from `quick`, never excused from `full` (W-013).

## 5. The gate: a graph of scopes, cached

How several scopes run together, and when one is skipped because it passed before, is
[land.md](land.md) section 7: each scope once, after its `deps`, in a stable order; a failure stops
the run unless `--keep-going`; a scope whose inputs have a counted pass in the cache is not run and
is marked `cached`. A `steps` list is read as `deps`, so 0.5.x gates keep their order. What 0.6.0
removes from 0.5.x (step marks, bars, `--resume`, the `resumed` and `refused` outcomes, the
`resume` key, receipts reuse for a scope) is listed in its section 7.6.

What stays here:

- `jst check` warns when a scope whose median recorded time is above 60 s comes before one below
  10 s among the deps of one scope ("cheap first").
- Each node has its own kept output and its own record line (`step`, `gate_run_id`); the run's line
  lists every node's outcome and seconds.
- `jst test <scope> --commit REV` runs the scope on a clean copy of REV that jackstay makes, and always
  removes (W-052, [discard-and-clean-gate.md](discard-and-clean-gate.md) section 3).

## 6. Leftovers

Every run gets `JACKSTAY_RUN_ID` (its run id) and `JACKSTAY_RUN_TMP` (a folder of its own under `$TMPDIR`,
removed after the run unless it holds leftovers) in its environment. `leftovers` is a list of
`{"glob": …, "fail": bool}`; `~`, `$TMPDIR`, `{run_id}` and `{run_tmp}` are expanded.

- A glob holding `{run_id}` or `{run_tmp}` is **attributed**: whatever matches after the run is this
  run's; with `"fail": true` the outcome is `failed`.
- A glob without the token lists entries new **and** born inside the run. With no overlapping
  recorded run (`runs.jsonl`), they are this run's, and under `"fail": true` the outcome is
  `failed`. With one, they are `unattributed`: under
  `"fail": true` the outcome is `unjudged` (77) and the line names them; under `"fail": false` they
  are listed. The remedy is the token.
- A glob whose folder or entry times cannot be read is no answer: `unjudged` under `"fail": true`
  (a proven failure stays `failed`), said under `"fail": false`. The walk stats every path
  component and takes only ENOENT/ENOTDIR as "nothing there".
- jackstay never deletes them; the project's tests own their cleanup.

## 7. The record and `jst test last`

`run` lines of a scope carry `scope`, `judge`, `counted`, `bundles`, `swift_testing`,
`timed_out`, `survivors`, `leftovers`, `gate_run_id`/`step`, and `lock_waited_seconds` (the wait,
apart from the run), `killed`, `left_running`, `run_tmp_kept`, `quick`, `env`, `timeout`. `jst test last [--scope S]` prints the
last run of a scope; `jst baseline` measures scopes per project in one vocabulary.

## Failure behaviour

An unusable `test` section, an undeclared scope (the declared ones listed), `focused` or test names
without `{tests}` in the words, or a failed `requires` check is `not-started` (2); `jst test --`
still runs, needing only `lock` and `receipts`. An unreadable durations file makes `{slow}` skip
nothing, said in the line. The other outcomes are sections 2 to 5.

## What it cannot see

A test that passes for the wrong reason (W-013's timing defects are for the projects' reviews); a
descendant that starts its own session between samples, and other accounts' processes; a
`requires` check when `jst test` is killed during it; leftovers outside the declared globs, or made
by an unrecorded process; whether a `changed` selector chose well; output formats other than
SwiftPM/XCTest/Swift Testing for the `swift-test` judge.

## Details of the behaviour

- A scope runs in the checkout's root, or in its `cwd` (a folder of the checkout), whatever the
  folder `jst test` was called from; `cwd` is in the key.
- `requires` checks run in `jst test` itself, before the lock and before a receipt is looked up.
  A failed check writes a `not-started` `run` line, which no count takes as a run; one stopped at
  its timeout fails whatever it ended with (`requires_session`, `requires_killed`).
- A scope is skipped only by the cache of [land.md](land.md) section 7.3; receipts reuse is for
  `jst test -- COMMAND`.
- After a normal end the session has up to 1 s to empty by itself before what is left is stopped.
- Durations are recorded from `passed` and `failed` runs (not `unjudged`, `timed-out`, `stopped`).
- A run of more than one node writes a `gate-start` line and, at its end, a `gate` line (`steps`:
  each node's outcome, `cached` mark, seconds and run id); each node's own `start`/`run` or
  `cached` lines carry `step` and `gate_run_id`. A run stopped or killed during a node leaves the
  node's `stopped` line and a `gate-start` with no `gate`. `jst test last --scope gate` shows it.
- A recorded run whose `start` has no `run` overlaps only while its pid is alive.
- An inner run killed outright (no result) is `unjudged` to the run: no verdict, no failure note.
- Membership by session is trusted only while the command's pid is free or still the command
  (same start time); a child is adopted by its parent link only when the parent is the remembered
  process and the child started after it; every group a remembered process leads is signalled too
  (SwiftPM starts `xctest` in a group of its own). While the stop waits it reads the process table
  again and signals what is new at once.
- A process table that cannot be read is "the end of the tree cannot be confirmed", not "nothing
  left": the run is `unjudged` (`tree_unconfirmed`, no receipt, no cache entry) and a `requires` check is
  not passed. `JACKSTAY_PROC_UNREADABLE=1` makes jackstay act so (tests only).
- A bundle that ends without starting is `unjudged`.
- `jst test`'s own files in `$TMPDIR` are removed before it ends by re-raising a signal.
- A request to stop that comes after the command ended by itself keeps that end: a non-zero status,
  an attributed leftover, is `failed`.
