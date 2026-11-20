# Testing: scopes, a judge, a timeout, a gate — `jst test <scope>`

Decisions: [W-012](../decisions/W-012-test-scopes-vocabulary.md) (one vocabulary of scopes, run as
`jst test <scope>`; each project defines its selection policy) and
[W-013](../decisions/W-013-quality-testing-declarative.md) (no time-sensitive tests; everything
jackstay adds is declarative and general; the goal is good and efficient testing in every project,
with jackstay helping), from a study of the test scopes and scripts of projects that adopt it.

Status: built.

The [verdict compatibility baseline](testing-compatibility.md) pins the direct command, scope,
gate and Swift fixture outcomes before structured runner adapters are added.

The [multi-runner testing proposal](testing-multirunner-proposal.md) describes a staged path to
structured test evidence across languages.

Nothing here names a project. A project declares its commands, checks and paths in `jackstay.json`;
jackstay supplies the behaviour. Any project with `swift test`, or with any command and the `exit`
judge, uses the same keys.

## The problem

Projects that grow their own test tooling tend to grow the same ladder of scopes under different
names, files and rules, with the rule for when each runs written in several places that disagree.
Judging a Swift run by a regex over `Executed N tests` over-counts (every enclosing suite prints a
total) and can fail passing filtered runs (SwiftPM prints `Executed 0 tests` for each bundle a
filter excludes); it does not check that every bundle ended, and it does not count Swift Testing
(`@Test`). Without a timeout that ends the whole process tree, a hung suite can hold a shared lock
for a long time, and stopping its wrapper can leave `xctest` running, reparented to pid 1. Tests
that leave files behind (preference plists, temporary folders) can fail later runs. Gates that run
cheap checks after a long suite, and hand-kept lists of slow classes, waste time (measured on
real suites).

## 1. Scopes

`jst test plan changed --base REF` resolves a scope from a committed range and, in the working tree, its
uncommitted and untracked paths. The order and path rules
come from `test.selection`. Empty ranges and paths without rules select `gate`. Rename endpoints
and deleted paths participate. With selection configured, `jst test changed` dispatches through
the chosen scope's execution and receipt policy. An explicit `BASE..REV` runs on a clean copy of
`REV`. See [path-based selection](../guide/tests.md#choose-a-scope-from-changed-paths).

    jst test <scope> [ARGS…] [--no-reuse] [--wait S] [--base REF] [--lock | --no-lock]
                     [--resume] [--keep-going] [--commit REV]
    jst test list            the project's scopes: name, what it runs, when the project runs it
    jst test plan <scope> [ARGS…]   the words that would run, step by step; runs nothing
    jst test -- COMMAND …    any command, recorded and judged (`receipts.commands`)

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
| `gate` | every step a change must pass to merge: checks and runs, in order | the longest |

When each is required (before a commit, before a pull request, after merges on main, before a
tag) is the project's rule, written **once**, in the scope's `when` text in `jackstay.json`; `jst test
list` prints it, and the project's AGENTS.md points to `jst test list` instead of restating it
(the jackstay block says so). jackstay does not choose the cadence.

### Declaring a scope

`jackstay.json` has an optional `test` section (an unknown key is an error, as everywhere in the file):

```json
"test": {
  "judge": "swift-test",
  "timeout": 1800,
  "leftovers": [{"glob": "~/Library/Preferences/ExampleTests.*.plist", "fail": true}],
  "scopes": {
    "focused": {"run": ["swift", "test", "--filter", "{tests}"], "timeout": 600,
                "when": "while working, for the classes you touched"},
    "changed": {"run": ["python3", "-I", "Scripts/select.py", "--run", "--base", "{base}"],
                "base": "origin/main", "judge": "steps",
                "when": "before every code commit"},
    "quick":   {"run": ["swift", "test", "--skip", "{slow}"], "slow": {"class_seconds": 20},
                "when": "before handing over"},
    "full":    {"run": ["swift", "test", "--skip", "^ExampleTests\\.Snapshot"], "timeout": 2400},
    "display": {"run": ["swift", "test", "--filter", "^ExampleTests\\.Snapshot"],
                "requires": [{"run": ["Scripts/screen-ok.sh"], "why": "needs an unlocked 2x screen"}]},
    "gate":    {"steps": ["format", "docs", "build", "full"], "when": "before every pull request"}
  },
  "steps": {
    "format": {"run": ["swift", "format", "lint", "--strict", "-r", "Sources"], "judge": "exit"},
    "docs":   {"run": ["python3", "-I", "Scripts/check_docs.py"], "judge": "exit"},
    "build":  {"run": ["swift", "build"], "judge": "exit"}
  }
}
```

Keys of a scope (all optional except `run` or `steps`):

- `run`: the words. Placeholders, replaced as whole words or inside one: `{tests}` (the ARGS of
  `focused`, each escaped and joined as one alternation: `Target.Class/test` → exactly that test;
  `Target.Class` → `^Target\.Class(/|$)`; a bare `Class` → `(^|\.)Class(/|$)`, so it matches in
  any target, since `swift test --filter` matches `Target.Class/test`; refused when no ARGS are
  given), `{base}` (the scope's `base`, or `--base REF` on the command line),
  `{slow}` (section 4) and `{report}` (the private JUnit report path). A placeholder is `{name}`
  of lower-case letters and `_`, not preceded by
  `$`; an unknown such name is an error in `jackstay.json` (a typo). Any other brace is literal:
  `${VAR}` in a `sh -c` word, `{2}` or `{2,3}` in a filter pattern.
- `steps`: instead of `run`, a list of step names (`steps` section) or scope names; section 5.
- `judge`: `swift-test` (section 2), `steps` (a count line declared by `evidence`),
  `go-json`, `junit`, `jest-json` and `trx` (section 2.1), or `exit` (status only; the outcome is
  `exited-0` on 0, never `passed`). Default: the section's `judge`, else `exit`.
- `evidence`, `veto`: as in `receipts.commands`, **added** to legacy judges. Structured judges
  accept a veto and count only structured evidence.
- `report`: a file-based judge's report name. JUnit can use `reports` instead for a bounded list
  of file names, and `empty_exit` for an optional no-collection exit status.
- `timeout` (seconds of the command's own run) and `lock_wait` (seconds waiting for the lock,
  counted apart; default: the scope's `timeout`); section 3.
- `requires`: checks run first, each with its own `timeout` (default 60 s) and the same tree stop
  (section 3); one that ends non-zero or times out makes the outcome `not-started` with its `why`
  (and `timed out` when it did), and nothing runs.
- `env`: variables set for the run (names and values in `jackstay.json`, recorded).
- `lock`: whether this scope takes the machine-wide lock when the project's `lock.enabled` is
  true (default true for `full`, `display`, `changed`, steps and `x-` scopes; false for
  `focused`, `quick`). A list of steps (`gate`) has no `lock` of its own: each step takes the lock
  by its own `lock`. The lock stays off by default (W-005).
- `when`: one line of text for `jst test list`.

`jst check` validates the section like the rest of `jackstay.json`; `jst test plan` shows the result.

## 2. The `swift-test` judge

Built into jackstay, in place of per-project `Executed` regexes, for any project that declares it. It
reads the output (both streams, escape sequences removed, read to its end) and requires, for
`passed`:

1. **XCTest bundles.** Every `Test Suite '<name>.xctest' started` has its matching `… passed|failed
   at` and its `Executed N tests, with F failures` line. A bundle that started and did not end
   makes the run `unjudged` (a crash or a quit-as-application end looks like a normal exit
   otherwise).
2. **Counts, per engine.** XCTest: the sum of each bundle's own total (one line per bundle, not
   every enclosing suite), executed less skipped. Swift Testing: its run summary (`✔ Test run with
   N tests … passed`, `✘ Test run with N tests … failed`), judged on its own: a Swift Testing run
   that started (`◇ Test run started`) must reach its summary, or the run is `unjudged`. Its
   summary counts skipped tests too, so Swift Testing's executed count is the number of its
   `✔ Test … passed` / `✘ Test … failed` lines (`started` and `skipped` lines are not counted);
   a summary of `0 tests` is a proven zero (SwiftPM prints one after an XCTest-only run), and so
   is a summary whose N tests are all accounted for by `skipped` lines: either adds 0. When the
   summary's N is above 0 and the per-test lines do not account for all N (a quieter output), the
   run is `unjudged`, not counted from the summary. A bundle
   with 0 is normal (a filter excluded it). The **run** must count above 0 in either engine — a
   run of only `@Test` cases with no XCTest bundle passes on Swift Testing's count — or the
   outcome is `nothing-run` (76).
2a. **Output SwiftPM hides.** `swift test --parallel` does not print a passing test's lines, and
   its xUnit file records a test that ended the process with `exit(0)` as passed and does not
   show skips (SwiftPM 6.3 `ParallelTestRunner`, `XUnitGenerator`). The judge cannot prove a pass
   from either, so a `swift-test` scope whose words hold `--parallel` is an error in `jackstay.json`
   (`judge: exit` remains possible: its outcome is `exited-0`, never `passed`). A wrapper (`make test`) can hide the flag, so the judge also
   checks the output: every engine the scope `expects` (`"expect": ["xctest"]` by default for
   `swift-test`; `["swift-testing"]` or both where declared) must show its own markers — an
   XCTest bundle's `started`/total lines (they appear even for a bundle a filter excluded, with
   `Executed 0 tests`), Swift Testing's run start and summary. An expected engine with no markers,
   or SwiftPM's parallel progress lines (`[N/M] Testing …`), makes the run `unjudged`, whatever the
   other engine counted.
3. **Failures.** Any bundle with F > 0, any `Test Suite '…' failed`, any `Test Case '…' failed`,
   any `✘ Test … failed` / `✘ Test run … failed`, or a status other than 0 makes it `failed`.
4. **After the end.** An XCTest `Test Case '…' started` after every started bundle has ended makes
   it `unjudged` (a test ran outside any judged total). Anything else may follow: Swift Testing,
   which SwiftPM runs after XCTest, has its own rule (2), and a wrapper may print its own lines
   (a `make` recipe's next check).
5. **Project vetoes** (`veto`) and `leftovers` with `"fail": true` (section 6) add failures.
6. **Order of verdicts.** A proven failure (a status the command ended with **by itself** that is
   not 0, a failure line, a veto, an attributed leftover) makes the outcome `failed` even when
   something else could not be judged (an unattributed leftover, a bundle that did not end):
   `failed` > `timed-out` > `stopped` > `unjudged` > `nothing-run` > `passed`. A status caused by
   jackstay's own stop (the timeout, or a signal sent to `jst test`) is not a failure: such a run is
   `timed-out` or `stopped` unless a failure line or veto was seen before the stop. A `failed` run always writes its failure note and, in a gate, its bar.

The judge prints its reading as the last line's detail:
`jst test: outcome=passed (… s, counted=2290 bundles=9/9 xctest=2290 swift-testing=0 skipped=1)`
(`counted` is the sum of every bundle's own total, less the skipped).
`failed` adds the first failing case; `unjudged` says which bundle did not end.

The patterns are SwiftPM's and XCTest's documented forms, held in one table in jackstay
(`internal/judge`) with tests against recorded outputs, anonymised to fixtures. An engine's markers
appear only when it ran something (with a filter that matches none of its tests, SwiftPM prints
none of its lines), so `expect` must name the engines a scope's words really run. A Swift
release that changes a form is a jackstay change, made once for every project.

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
records. Structured gate marks also carry the adapter version and counted pass. Resume needs
that durable verdict, even after the private report directory is removed. It still runs
`requires` before resuming any step that needs an external input. `--commit` runs afresh in a
clean copy, including for structured judges.
The [producer guide](../guide/tests.md#structured-test-reports) lists the pinned probes. The
[compatibility baseline](testing-compatibility.md) records the original exit and reporting contracts.

## 3. A timeout that ends the whole run

- The command runs in a **new session** (`setsid`); its descendants keep that session id even
  when reparented to pid 1, unless one starts a session of its own.
- The session's members are found by listing every pid (`proc_listallpids`) and asking each its
  session with `getsid(pid)` (`proc_pidinfo`'s BSD info gives the parent and group, not the
  session); a pid whose `getsid` fails is skipped and said. Every descendant ever seen (sampled
  every second and at the end, by session and by parent links) is remembered **with its start time**
  (`proc_pidinfo` `pbi_start_tvsec`), and is signalled only if a pid with that start time still
  exists — a reused pid is never signalled.
- At `timeout` seconds of the command's run: SIGTERM to the group, the session and the remembered
  descendants; after a grace of 10 s, SIGKILL to what is left. The same stop is used when
  `jst test` itself is asked to stop (SIGINT, SIGTERM, SIGHUP, SIGQUIT), not a signal to one group
  alone.
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

This takes the place of hand-kept slow lists; it is measured from the project's own runs, so it is never a
reason to keep a slow or timing-dependent test (W-013): a slow class is skipped from `quick`, not
excused from `full`.

## 5. The gate: steps in order

- `steps` names steps (from the `steps` section) or other scopes, run **in the declared order**.
  `jst check` warns when a step whose median recorded time is above 60 s comes before one below
  10 s ("cheap first").
- **Fail-fast**: the first step that is not `passed`/`exited-0` ends the gate; the outcome is that
  step's, and the line names it (`gate failed at docs`). `--keep-going` runs the rest and reports
  each — except a step refused by a bar under `--resume`, which ends the gate at once (below).
- Each step has its own kept output and its own record line (`step`, `gate_run_id`); the gate's
  line lists every step's outcome and seconds.
- **Resume** (`jst test gate --resume`) skips a leaf step that passed before on the same tree. What
  "the same tree" means is exactly what jackstay checks, and no more:
  - **Clean checkout.** `git status --porcelain --untracked-files=all` is empty; no entry is marked
    skip-worktree or assume-unchanged; and no untracked file is hidden only by a rule outside the
    committed `.gitignore` files (`.git/info/exclude`, a global excludes file), except under an
    agent's top folder (`.claude`, `.omc`, `.omx`, `.codex`, `.cursor`), under a regenerable folder
    (`.build`, `DerivedData`, `node_modules`, `__pycache__`, …) or a `.DS_Store`. A checkout that is
    not clean is never resumed and its passes leave no mark.
  - **The step's key**: HEAD's commit; the step's full definition from `jackstay.json` (words,
    folder, `env`, judge, vetoes, timeout, adapter version and report settings); the environment
    variables and the toolchain output the receipts key reads; jackstay's version; the content of
    `receipts.extra_files`; the **tip
    commit** of every ref in `receipts.refs` and the command entry's own `refs`; the **merge base**
    of HEAD with every ref in `receipts.merge_bases` and the entry's own `merge_bases` (only the
    merge base: the ref's tip may move without changing the key); and for the step's resolved
    `{base}`, both its tip and its merge base with HEAD. A ref that cannot be resolved, or a merge
    base that cannot be found, allows no mark. The key is made again before a mark is written and
    before it is used, so a step that chose its tests from `origin/main` (its `{base}`) is not
    resumed after `origin/main` moved; a step that reads a ref's tip without naming it as `{base}`
    lists it in `refs`, not `merge_bases`.
  - **Nothing rewritten while it ran**: the identity, size and times of every tracked file, and the
    content of `receipts.extra_files`, are the same after the step as before it, and the checkout is
    still clean with the same key; otherwise its pass leaves no mark.
  - **No bar** of the key and no `unattributed` leftover under a `"fail": true` glob (section 6).
  Not covered, by design: a file ignored by a committed `.gitignore` (an `.env`, a generated
  config, a cache), an untracked file in the places the clean check passes over, and anything
  outside the checkout (another package by path, the network, the keychain). A step that reads such
  a **file** in the checkout names it in `receipts.extra_files` — one readable file per entry. Its
  bytes then enter the step's key, so a change between runs is seen; its identity, size, times and
  bytes enter the stamps compared before and after a run, so a change during one is seen. A change
  of its mode or other metadata between runs is not seen: a step that depends on those declares
  `"resume": false`. A folder, or a file that cannot be read, is recorded only as `missing`, so a
  change inside a folder is not seen. A step whose inputs cannot all be
  named that way (a folder of them, anything outside the checkout) declares `"resume": false`.
- **Marks and bars.** A step that passed (or ended `exited-0` for an `exit` step) on a clean checkout
  with a key writes a **mark**; one that `failed` writes a **bar** for its key under
  `<jackstay home>/<project>/gate-bars/`. A step that is run removes its key's mark before it starts,
  so one that then ends without a verdict (`timed-out`, `unjudged`, `stopped`) leaves no mark and
  runs again next time. A step whose `requires` check fails is `not-started` before its key is made:
  it removes nothing, and an earlier mark of it can be used once the check passes. A key with a bar
  gets no mark, so no mark outlives a bar; bars are not pruned with receipts and live at least as
  long as any mark can be used (`receipts.max_age_hours` + one day). Marks older than
  `receipts.max_age_hours` are not used; a bar still refuses `--resume` after every mark of its key
  has expired.
- **A bar ends the gate.** Under `--resume` a step whose key has a bar is not run: the gate ends
  `refused` (exit 2) with "nothing has changed since this step failed; change the tree or run the
  gate without --resume", also with `--keep-going`, and the later steps do not run. So resume saves
  repeated work and never turns a failure green by rerunning it (W-013).
- Marks exist only for **leaf** steps (ones with `run`). A step that is itself a scope with
  `steps` is expanded into its leaves, recursively, and `--resume` judges each leaf on its own
  mark, key, bar and `"resume": false`; a composite step never has a mark of its own. A cycle in
  `steps` is an error in `jackstay.json`.
- Inside a gate, receipts reuse (`"reuse": true`) is off: the step mark is the only skip, and a
  skipped step is shown as `resumed`, never as `passed`. `resumed` lets the gate go on to the next
  step, as `passed` does. The gate's outcome is `passed` when every step `passed`, `exited-0` or
  `resumed` and at least one ran now; when every step was `resumed`, the outcome is `resumed`
  (exit 0): nothing ran, and it is not counted as a pass.
- `jst test gate --from STEP` is not offered: the order is the project's contract.
- `jst test <scope> --commit REV` runs the scope on a clean copy of REV that jackstay makes, and always
  removes (W-052, [discard-and-clean-gate.md](discard-and-clean-gate.md) section 3).

## 6. Leftovers

Every run gets `JACKSTAY_RUN_ID` (its run id) and `JACKSTAY_RUN_TMP` (a folder of its own under `$TMPDIR`,
removed after the run unless it holds leftovers) in its environment. `leftovers` is a list of
`{"glob": …, "fail": bool}`; `~`, `$TMPDIR`, `{run_id}` and `{run_tmp}` are expanded.

- A glob that holds `{run_id}` or `{run_tmp}` is **attributed**: whatever matches after the run
  is this run's (a project whose tests name their files with the token gets exact answers).
  With `"fail": true` it makes the outcome `failed`.
- A glob without the token: entries that are new **and** born inside the run are listed. When no
  other run on this machine overlapped (from `runs.jsonl`: other projects' and sessions' recorded
  runs), they are this run's, and `"fail": true` fails it. When another run overlapped, they are
  `unattributed`: under a `"fail": true` glob the outcome is `unjudged` (77) — no receipt, no step
  mark, never `passed` — and the line says which entries; under `"fail": false` they are listed
  and recorded. So a required cleanliness check is never claimed without an answer; the remedy is
  the token. Unrecorded processes can
  create matching files; that is a limit (below), and the remedy is the token.
- A glob whose folder cannot be listed or whose entries' times cannot be read (a permission, the
  sandbox) is not "nothing left": under `"fail": true` the run is `unjudged` (no receipt, no
  mark; a proven failure stays `failed`), under `"fail": false` it is said.
- jackstay never deletes them; the project's tests own their cleanup.

## 7. The record and `jst test last`

`run` lines of a scope carry `scope`, `judge`, `counted`, `bundles`, `swift_testing`,
`timed_out`, `survivors`, `leftovers`, `gate_run_id`/`step`, and `lock_waited_seconds` (the wait,
apart from the run), `killed`, `left_running`, `run_tmp_kept`, `quick`, `env`, `timeout`. `jst test last [--scope S]` prints the
last run of a scope. `jst baseline` reads scopes, so "how often does each scope run, how long,
how often does it fail" is measured per project in one vocabulary.

## Failure behaviour

| Failure | What happens |
|---|---|
| `jackstay.json` `test` section unusable | `not-started` (2) for every scope; `jst test -- …` still works: `jackstay.json` is validated per section, and the raw form needs only `lock` and `receipts` to be usable |
| a scope not declared | refused (2), listing the declared ones |
| `focused` without names | refused (2) |
| a `requires` check fails | `not-started` (2) with its `why`; nothing ran |
| status 0, and neither engine counted a test above 0 (no XCTest bundle total above 0, no Swift Testing summary above 0) | `nothing-run` (76) |
| output not read to its end | `unjudged` (77) |
| timeout | `timed-out` (124), the tree stopped, survivors named |
| durations file unreadable | `{slow}` skips nothing; said in the line |
| resume asked after a `failed` step on the same tree | refused (2) with the reason |

## What it cannot see

- A test that passes for the wrong reason (an absence window that is too short, an assertion that
  checks nothing). The judge counts and reads verdicts; W-013's timing defects are found by the
  projects' reviews and audits, not by jackstay.
- A descendant that starts its own session and is never seen at a sample; and other accounts'
  processes (`sudo`), which `proc_pidinfo` does not describe to this account.
- A `requires` check, run by `jst test` itself, when `jst test` is killed while it runs: the check
  ends at its own timeout or not at all.
- Leftovers outside the declared globs; the cause of an unattributed leftover when runs overlap;
  a file made by a process that is not a recorded run.
- Whether a project's `changed` selector chose well: jackstay runs it and judges what it ran.
- Swift Testing per-suite times, until its event stream is read.
- Output formats other than SwiftPM/XCTest/Swift Testing for the `swift-test` judge; other
  projects use `steps` or `exit`.

## Details of the behaviour

- Names (ARGS) given to a scope whose words hold no `{tests}` are refused (2).
- A scope runs in the checkout's root, or in its `cwd` (a folder of the checkout), whatever the
  folder `jst test` was called from; `cwd` is in the key.
- `requires` checks run in `jst test` itself, before the lock and before a receipt is looked up.
  A failed check writes a `run` line with outcome `not-started`, which no count takes as a run. A
  check stopped at its timeout is a failed check whatever it ended with; a stopped check is
  recorded with its session and what was killed (`requires_session`, `requires_killed`). The
  `requires` of a nested list of steps is checked before its first leaf runs.
- Receipts for a scope: only when its expanded words are a `receipts.commands` entry with
  `"reuse": true` (as for `jst test --`); the scope's definition and `test.leftovers` are in the
  key. Off inside a gate.
- After a normal end the session has up to 1 s to empty by itself (the last helper ending with its
  parent) before what is left is stopped; this bounds a wait for an event and decides nothing by
  time.
- Durations are recorded from `passed` and `failed` runs (not `unjudged`, `timed-out`, `stopped`).
- The gate writes a `gate-start` line and, at its end, a `gate` line (`steps`: each step's outcome,
  seconds and run id); each step's own `start`/`run` lines carry `step` and `gate_run_id`. A gate
  stopped or killed during a step leaves the step's `stopped` line and a `gate-start` with no
  `gate`. A gate refused by a bar ends `refused` (2). `jst test last --scope gate` shows the gate.
- Any bar of a step's key refuses `--resume`, also after a later pass of that step on the same
  tree (the bar means "no run of the same step on the same key ended failed" cannot be shown). A
  key with a bar gets no mark, so no mark outlives a bar.
- A step's key holds a command entry's own `refs` and `merge_bases`; a ref that cannot be resolved
  allows no mark.
- A clean checkout and the same HEAD before and after prove the content of every tracked file;
  the stamps compared before and after are every tracked file's identity, size and times, and
  `receipts.extra_files` with their content.
- A recorded run whose `start` has no `run` overlaps only while its pid is alive.
- Per-section reading reaches every tool: `jst lock` reads `lock`, the guard and `jst tidy` read
  `guard`, `jst test --` and `jst receipts key` read `lock` and `receipts`, the guard's Card check,
  `jst gh`'s card rules and `jst bg` read only their own sections; `jst check` judges all. A
  section's type errors stay in that section (only `jackstay_schema` and `project` are the file's
  own); the project file's cross-section check ([implementation.md](implementation.md) section
  3.2) reads only the sections it compares.
- A list of steps (`gate`) takes no `timeout`, `lock_wait`, `lock` or `resume` of its own: each step
  has its own. Its `requires` is checked like a step's.
- A `failed` run of a scope outside a gate (`jst test full`) also writes the bar of that step's key,
  taken before it ran, so a gate's `--resume` cannot stand on an earlier mark of it — but only when
  the scope's `resume` is on, the checkout is clean (as above) and its key can be made, and only for
  that exact key: a run given test names (`{tests}`), another `{base}` or another folder is another
  key, and a run that reused a receipt ran nothing and writes no bar. After a failure outside those
  conditions, run the gate without `--resume`.
- An inner run killed outright (no result) is `unjudged` to the gate: no verdict, no bar.
- The tree remembers a process by pid and start time. It trusts membership by session only while
  the pid of the command is free or still the command (same start time); a child is adopted by its
  parent link only when the parent is the remembered process and the child started after it; the
  group is signalled only while a remembered process is in it; every group a remembered process
  leads is signalled too (SwiftPM starts `xctest` in a group of its own). So a pid or session id
  the system gave to another process is never signalled. While the stop waits it reads the process
  table again and signals what is new at once (a TERM handler that starts a child in a session of
  its own).
- A process table that cannot be read is "the end of the tree cannot be confirmed", not "nothing
  left": the run is `unjudged` (`tree_unconfirmed`, no receipt, no mark) and a `requires` check is
  not passed. `JACKSTAY_PROC_UNREADABLE=1` makes jackstay act so (tests only).
- An XCTest bundle's total must be the very next line of its own end in its stream; a bundle that
  ends without starting is `unjudged`.
- The leftovers walk lists every folder a glob passes through, stats every path component and
  takes only ENOENT/ENOTDIR as "nothing there"; one that cannot be listed is no answer
  (`unjudged` under `"fail": true`).
- `jst test`'s own files in `$TMPDIR` are removed before it ends by re-raising a signal.
- A request to stop that comes after the command ended by itself keeps that end: a non-zero status,
  an attributed leftover, is `failed`.

## Rules

- The six names are fixed; extras need `x-`.
- `--resume` refuses to rerun a step that `failed` on the same tree (W-013's "fix, do not shield").
- Default `lock` per scope (section 1); the machine-wide lock itself stays off by default (W-005).
- Durations are medians over the last 20 runs; `quick` with no data skips nothing.
- Leftovers are never deleted by jackstay; failing on them is opt-in per glob; an overlap with
  another run is said, not failed.
- The AGENTS block (`internal/adopt/block`) has a scopes paragraph: run `jst test list`; use
  `jst test <scope>`; `when` comes from the list.
