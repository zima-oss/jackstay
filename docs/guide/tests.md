# Tests

`jst test` runs your tests, counts them, records the run and keeps its output. Only a run whose
tests were counted is a pass. A pass is keyed by the exact tree it ran on, so where you opt in, a
later run of the same tree is skipped.

## Read the last `jst test:` line

Every run ends with one line on standard error naming its outcome. Read that line, not the exit
status alone: a test command may itself end with 2 or 77.

| Outcome | Meaning |
|---|---|
| `passed` | one command: ran now, ended 0, output read to its end, tests **counted** (above zero), no veto line. A `check` scope (`kind: check`): ended 0. A gate: every step `passed` or `skipped`, and at least one ran |
| `exited-0` | ended 0, but nothing counted its tests and it is not a `check`: **not a pass**; a gate with such a step is `exited-0` too |
| `reused` | nothing ran: a receipt for this tree exists and reuse is on |
| `failed` | ended non-zero, or a veto line was seen |
| `not-started` | `jackstay.json` or the lock could not be used, or a `requires` check failed; nothing ran |
| `skipped` | the scope asked for cannot run on this system (its `systems`); in a gate, a step so is ignored |
| `not-run` | in a gate, a step whose dep did not pass, or one after a failure ended the run |
| `needs-other-system` | `jst test changed`: a test of the change runs on no CI system and not on this machine |
| `left-to-ci` | `jst test changed` or `jst test uncovered` had nothing to run here: CI covers it |
| `gave-up` | the machine-wide lock was not had within `--wait` |
| `nothing-run` | ended 0, but the output showed no test |
| `unjudged` | ended 0, but the output could not be read to its end, or the run left processes or files that cannot be attributed |
| `passed-on-retry` | every test passed, but at least one only after its runner retried it: **not a pass**, no receipt, and it fails a gate |
| `timed-out` | a scope's timeout ended the whole process tree |
| `stopped` | `jst test` was asked to stop; it ends by that signal |
| `stopped-waiting` | `jst test` was asked to stop while it waited for the machine-wide lock; nothing ran |

The exit status of each outcome: `jst test --help`.

After a run, `jst test last` shows it again (outcome, what went wrong, where the output is) without
running anything; a run that warns it was not logged names its output file instead. Every run's output is kept in `~/.jackstay/<project>/outputs/` (the newest 30). A
run that did not pass prints up to 40 lines that say what went wrong.

## Run any command

```sh
jst test -- python3 -m pytest -q
jst test last
```

`jst test -- COMMAND` needs no `jackstay.json`. Without one, a run that ends 0 is `exited-0`, never
`passed`. To make it count, list the command in `receipts.commands` with an `evidence` pattern
([Receipts](#receipts)).

## Declare scopes

A scope is a named way to run your tests, declared in `jackstay.json`'s `test` section:

```json
"receipts": {"toolchain": []},
"test": {
  "judge": "steps",
  "timeout": 1800,
  "scopes": {
    "quick":   {"run": ["python3", "-m", "pytest", "-q", "-m", "not slow"],
                "evidence": "([0-9]+) passed", "timeout": 600,
                "when": "while working"},
    "full":    {"run": ["python3", "-m", "pytest", "-q"], "evidence": "([0-9]+) passed",
                "veto": "^ERROR", "when": "before handing over"},
    "gate":    {"steps": ["lint", "full"], "when": "before every pull request"}
  },
  "steps": {
    "lint": {"run": ["ruff", "check", "."], "judge": "exit"}
  }
}
```

```sh
jst test list
jst test plan quick
jst test quick
```

`jst test list` prints each scope, what it runs and its `when`, so your `AGENTS.md` can point there
instead of restating when each is required. `jst test plan` prints the words a scope would run,
step by step, and runs nothing.

Six scope names have one meaning in every project; any other name starts with `x-`:

| Scope | Meaning |
|---|---|
| `focused` | the tests named on the command line: `jst test focused ParserTests` |
| `changed` | what a change since a base can affect, chosen by your own selector |
| `quick` | a broad look, less the slowest classes |
| `full` | the whole suite a headless run can do |
| `display` | what needs a real screen or device |
| `gate` | every scope a change must pass to merge: a scope with no `run` and `deps` |

The keys of a scope or a step:

- `run`: the words to run. Placeholders: `{tests}`, the names given to `focused` as one regular
  expression, made for `swift test --filter`; `{base}`, the scope's `base` or `--base REF`;
  `{slow}`, the slow classes `quick` skips.
- `deps`: the scopes or steps that must pass first, in order. A scope with no `run` (a gate) has
  only `deps`, with `when`, `ci` and `systems`. `steps` is read as `deps`.
- `ci` and `systems`: where CI runs the scope and where it runs at all, by Go's names (`linux`,
  `darwin`); see [where tests run](#where-tests-run). The graph keys need `"jackstay_schema": 3`.
- `judge`: how the run is counted. `steps`: an `evidence` pattern with one number group counts the
  tests. `exit`: the status only, so `exited-0` at best. `swift-test`: built in for `swift test`.
  `go-json`, `junit`, `jest-json`, `trx`, `xcresult-json`: [structured reports](#structured-test-reports).
- `evidence` and `veto`: RE2 patterns; a veto line fails the run.
- `timeout`: seconds; the whole process tree is stopped at it.
- `requires`: checks run first; each has 60 seconds unless it gives its own `timeout`.
- `env`, `lock`, `when`.


### The gate

```sh
jst test gate
jst test gate --keep-going
jst test gate --commit HEAD
```

A run is a graph: each scope or step once, after its `deps`, one at a time, in the order the
declarations give (depth first, deps in their declared order). A step two lists share runs once. The
first that does not pass ends the run, and what comes after it is `not-run`; `--keep-going` runs
every step whose deps passed and reports each. A step whose `systems` leave this machine out is
`skipped`, which stops nothing. Every run runs every step: a gate that stopped or failed runs in
full again. `--commit HEAD` runs on a clean copy of HEAD that jackstay makes and removes.

```text
jst test: outcome=passed (gate, 0.9 s: lint=passed, full=passed)
jst test: outcome=failed (gate failed at full, exit 1, 0.9 s: lint=passed, full=failed, gate=not-run)
```

- The line names each step's own outcome: `lint=passed` is a check (`"kind": "check"`) that ended
  0; `full=passed` counted its tests.
- **Step kinds** (schema 3): `"kind": "test"` must count tests to pass (its judge cannot be
  `exit`); `"kind": "check"`, for a linter, an audit or a build, passes on exit 0. A step without
  `kind` that counts no tests is `exited-0`, and so is a gate that runs it: mark your linters and
  builds `check`.
- A run that fails names the first failure in this order: `failed`, `passed-on-retry`,
  `timed-out`, `stopped`, `stopped-waiting`, `unjudged`, `nothing-run`, `gave-up`, `not-started`.
- `jst check` refuses a cycle of deps, an unknown dep, `steps` with `deps`, a dep that needs test
  names, and, in schema 3, `requires` on a scope with no `run`: the check goes to the dep that
  needs it. A schema-2 list's own `requires` run once, before anything, when the list is in the run.
- `--resume` and the `resume` key are gone in 0.6.0 ([Upgrading](upgrading.md)).
- `--commit REV` runs on a clone that shares the repository's objects, so your uncommitted work
  cannot make it pass or fail. It has no ignored files (`node_modules`, build folders): a step that
  needs them builds them.
- `jst check` warns about a scope with no timeout, and about a gate that runs a slow step (over
  60 s, by the recorded median) before a fast one (under 10 s).

### Choose a scope from changed paths

Declare `test.selection` and `jst test changed` picks a scope from the paths changed since a base:

```json
"test": {
  "selection": {
    "order": ["quick", "full", "gate"],
    "rules": [
      {"glob": "docs/**", "scope": "quick"},
      {"glob": "Sources/**", "scope": "full"}
    ]
  }
}
```

```sh
jst test plan auto --base origin/main
jst test auto --base origin/main
jst test changed origin/main..HEAD
jst test plan changed origin/main..HEAD
```

The `plan` lines print the choice, each matching rule, the scope's `when` and the command, and run
nothing. `auto` runs the chosen scope in the working tree; `changed BASE..REV` runs it on a clean
copy of REV.

- `order` lists declared scopes from least to most strict and ends with `gate`.
- A rule maps repository-relative path globs to a scope. `*` matches within one path segment; `**`
  matches zero or more whole segments.
- Every path touched by a commit counts, a deleted path and both names of a rename included. The
  strictest matching rule wins. An unmatched path or an empty range chooses `gate`.
- In the working tree, the committed range through HEAD counts, and so do the uncommitted and
  untracked paths `git status` lists (ignored files do not). With `--commit REV` or `BASE..REV`
  only the committed range counts.
- Without `--base`, the base is `origin/HEAD`; if that ref is missing, name the base.
- Projects without `test.selection` keep their declared `changed` scope.

Use `jst test gate`, not the selected scope, for installs, tags and publishing a release, unless
the project says where its tests run (below).

### Where tests run

A scope's `ci` names the systems whose CI job runs it; `systems` the systems it runs on at all.

```json
"scopes": {
  "x-build": {"run": ["just", "build"], "judge": "exit"},
  "x-go":    {"run": ["just", "go"], "deps": ["x-build"], "ci": ["linux"]},
  "x-docs":  {"run": ["just", "docs"], "judge": "exit", "ci": ["linux"]},
  "x-ios":   {"run": ["just", "ios"], "deps": ["x-build"], "systems": ["darwin"]},
  "gate":    {"deps": ["x-docs", "x-go", "x-ios"]}
}
```

A scope CI runs on a system is **covered** there, and so is every dep it reaches that can run
there. A scope with `run` can run on a system when its `systems` allow it and all its deps can; a
scope with no `run`, when its `systems` allow it and one of its deps can. `jst check` refuses `ci`
naming a system where the scope cannot run.

```sh
jst test changed --base origin/main   # before a pull request
jst test uncovered                    # before an install or a tag
jst test ci                           # the CI job, one per system: `jst test ci (linux)`
```

`jst test changed` (with `test.selection`) and `jst test uncovered` put every step they reach in
one group: **here** (it runs on this machine and is not covered), **ci** (covered), **elsewhere**
(neither); `jst test auto` does not split, and runs the chosen scope. The here steps run, with
their deps from any group:

```text
jst test: outcome=passed (changed, 40 s; here: x-build=passed, x-ios=passed; ci: x-docs, x-go; elsewhere: none)
```

- `jst test changed` ends `needs-other-system` (80) when a test of the change runs on no CI system
  and not here (`darwin: x-ios` on Linux), and `left-to-ci` (0) when nothing runs here.
- `jst test uncovered` runs on a clean copy of the commit (`--commit REV`, default HEAD) every
  scope that needs no test names and that CI does not cover on this system; it lists elsewhere and
  is never held by it. Its `passed` or `left-to-ci` is what `jst install` from a checkout needs
  when the commit's `jackstay.json` gives some scope a `ci`.
- `jst test ci` runs every scope covered on this system, each once, in graph order. Name the CI
  job `jst test ci (SYSTEM)` and require it in the base's rules; `jst doctor` reports a system
  whose job they do not require. It ends 77 on `exited-0`: a CI job is green only on a pass, so
  mark each linter and build `"kind": "check"`.
- jackstay schedules each scope once but cannot see inside a command: scopes CI runs on one system
  must test disjoint sets, or a test runs twice.
- There is no cache in 0.6.0: a scope with deps runs every step each time. `jst adopt` does not
  write the CI workflow for these jobs: add the job yourself.

## Structured test reports

A runner's own report counts tests more reliably than a pattern over its output. Each structured
judge reads one kind of report.

**Go**: ask for JSON events and use `go-json`.

```json
"full": {"run": ["go", "test", "-json", "-count=1", "./..."],
         "judge": "go-json", "when": "before handing over"}
```

The judge reads package and nested-test results from standard output. Use `-count=1` for a fresh
run: a cached result or an incomplete event stream is `unjudged`, and a filtered run that executed
no test is `nothing-run`.

**JUnit XML (pytest)**: declare the report's name and give its path to the runner.

```json
"full": {"run": ["python3", "-m", "pytest", "--junitxml={report}"],
         "judge": "junit", "report": "report.xml", "empty_exit": 5,
         "when": "before handing over"}
```

- `{report}` expands to a private path for this attempt; wrappers also get it in
  `JACKSTAY_TEST_REPORT`. The judge reads the file after the command exits.
- Executed cases are counted. An all-skipped or empty report is `nothing-run`; a missing,
  unreadable or incomplete one is `unjudged` when the command otherwise succeeds.
- `empty_exit: 5` accepts pytest's no-tests-collected status only with a valid empty report.

**JUnit XML (Vitest)**: install Vitest in the project, then:

```json
"full": {"run": ["npx", "vitest", "run", "--reporter=junit", "--outputFile={report}"],
         "judge": "junit", "report": "vitest.xml", "when": "before handing over"}
```

Checked with Vitest 5.1.1: a failed case exits 1, and a `--testNamePattern` that matches no name,
or `--passWithNoTests` with no matching file, exits 0 and is `nothing-run`. For a fixed shard plan,
write each shard to its own named file under `{report_dir}` and list every file in `reports`; your
command must check each shard's exit status. jackstay requires every declared file and refuses a
case that appears in two. See [Vitest's reporter guide](https://vitest.dev/guide/reporters).

**JUnit XML (Maven Surefire)**: several files, listed in `reports` and written to `{report_dir}`:
[Maven Surefire](maven-surefire.md).

**Jest and .NET**: one file from one runner call.

```json
"x-jest": {"judge": "jest-json", "report": "jest.json",
           "run": ["npx", "jest", "--json", "--outputFile={report}"]},
"x-dotnet": {"judge": "trx", "report": "run.trx",
             "run": ["dotnet", "test", "Tests.csproj", "--framework", "net11.0",
                     "--disable-build-servers", "--logger", "trx;LogFileName={report}"]}
```

- Jest's totals are checked against the executed assertions. Discovery output (`--collectTests`,
  `wouldRun`) cannot prove a pass.
- A TRX report is checked against its individual results and final counters. Use one test project
  and target framework per scope, so the logger cannot overwrite an earlier file, and disable build
  servers, so no compiler process outlives the run. TRX from Microsoft.Testing.Platform is not
  supported.
- Checked with Jest 30.5.2, and VSTest from .NET SDK 11.0.100 with MSTest 4.2.3.
- Each Jest shard or VSTest filter partition needs its own command and report: use separate scopes
  or gate steps. One report cannot prove the whole shard plan ran.

**Retries.** Turn retries off where a pass must be a first-attempt pass. A Jest run with a repeated
assertion is `passed-on-retry`; a skipped or todo assertion with repeated invocations is
`unjudged`. Vitest JUnit and MSTest TRX can fold a failed first attempt and a later pass into one
result, so with retries on they cannot prove a first-attempt pass.
[Jest CLI](https://jestjs.io/docs/cli) · [VSTest logger](https://learn.microsoft.com/en-us/dotnet/core/tools/dotnet-test-vstest)

For `jst test -- COMMAND`, declare the same `judge` and report settings in `receipts.commands`.
Wrappers get `JACKSTAY_TEST_REPORT` for one file, or `JACKSTAY_TEST_REPORT_DIR` for several JUnit
files. Producers still to come: the [design proposal](../design/testing-multirunner-proposal.md).

### Judge an Xcode test plan

Use `judge: "xcresult-json"` with `reports: ["summary.json", "tests.json"]`, and name every
expected unit test bundle in `xcresult_bundles`. Export both views from the same fresh `.xcresult`
with `xcrun xcresulttool get test-results summary` and `xcrun xcresulttool get test-results tests`;
the wrapper `tests/producers/swift/xcresult.sh` runs the test plan, writes both files to
`JACKSTAY_TEST_REPORT_DIR` and keeps the `xcodebuild test` exit status. Checked with Xcode 27.2.

The judge checks the bundles, the case tree and the summary totals for one device and
configuration. Parameterised tests count at the test case level, and their argument results are
checked separately. Missing or inconsistent reports from a successful process are `unjudged`. SwiftPM
mixed-engine scopes keep the text judge. [Swift evidence pilot](../design/swift-structured-pilot.md)

## Run one test, and time each case

### Select a Go test by ID

A `focused` or `x-…` scope with `selector: "go-test"` takes one ID,
`PACKAGE::TestName[/subtest]`, with the import path and test name as Go's JSON events print them:

```json
"x-go-case": {
  "run": ["go", "test", "-json", "-count=1", "{test_args}"],
  "judge": "go-json",
  "selector": "go-test",
  "durations": true
}
```

```sh
jst test x-go-case 'github.com/zima-oss/jackstay/internal/testing::TestGoScopeRefusesUnsupportedCapabilitiesBeforeLaunch/wrong_judge'
jst test last --scope x-go-case --json
```

`{test_args}` expands to separate filter and package arguments, and each part of the name is
matched literally. jackstay checks the ID against what ran: a parent that ran only to look for a
missing subtest is not a pass. Gate steps and path-based selection cannot take an ID. Swift scopes
keep `{tests}` and `{slow}`. [Go subtest selection](https://pkg.go.dev/cmd/go#hdr-Testing_flags)

With `durations: true`, a fresh counted pass records the time of each executed leaf test: up to 512
samples at `run.case_durations` in `jst test last --json`, each with `package`, `test` and
`seconds`; `run.case_durations_truncated` says the limit was hit. Go timings do not feed Swift's
`{slow}` history.

### Select a Jest test by ID

Give a `focused` or `x-…` scope `selector: "jest-test"`, `judge: "jest-json"`, a declared `report`,
and this command:

```json
["jest", "--runInBand", "--no-watchman", "--json", "--outputFile={report}", "{test_args}"]
```

Pass one argument, `FILE::full test name`, quoted as one shell word. The file is relative to the
scope's folder; the name includes the enclosing `describe` blocks and parameter labels as Jest
reports them. jackstay passes Jest the exact file and an anchored literal name, then checks both in
the report. A file with no matching case is `nothing-run`; ambiguous or unrelated results from a
successful process are `unjudged`. [Jest CLI](https://jestjs.io/docs/cli)

### Read JUnit case durations

A `junit` scope with `durations: true` records, on a fresh counted pass, up to 512 samples across
its reports at `run.case_durations` in `jst test last --scope SCOPE --json`: each with the `suite`
path, `class`, `test` and `seconds`. Skipped cases and cases without a valid `time` have none, and a
reused run has none. The time is the producer's, which can include setup and teardown.
[pytest time reporting](https://docs.pytest.org/en/stable/how-to/output.html#creating-junitxml-format-files)

## Receipts

After a `passed` run, jackstay stores a receipt keyed by the tree. A command you opt in is then not
run again on the same key: it prints the receipt and ends `reused`.

```json
"receipts": {
  "inputs": ["."],
  "exclude": [".claude/**"],
  "env": ["CI", "APP_*"],
  "toolchain": [["python3", "--version"]],
  "commands": [
    {"argv": ["python3", "-m", "pytest", "-q"], "reuse": true, "evidence": "([0-9]+) passed"}
  ]
}
```

- The key covers the files under `inputs` (tracked, and untracked ones the committed `.gitignore`
  files do not hide) less `exclude`, the command's words, the `toolchain` output, the `env`
  variables and the refs you name.
- Reuse is opt-in per command: `"reuse": true`, an exact `argv` and an `evidence` pattern, with
  `env` and `toolchain` written out.
- A command listed with `"reuse": false` (or by `argv_prefix`) is keyed and counted but always runs;
  each repeat is logged as `would-hit`, which measures what reuse would save before you turn it on.
- It never reuses on `main` or `master` (`receipts.no_reuse_branches`), nor when HEAD is the tip of
  such a branch. A receipt is used for `max_age_hours` (default 72).
- `--no-reuse` (or `JACKSTAY_NO_REUSE`) runs anyway.

```sh
jst receipts key -- python3 -m pytest -q
jst receipts stats
```

The first prints the key here, what went into it, and whether it would be reused; the second, runs,
reuses and seconds saved. The key cannot see what lies outside the checkout (an SDK, a daemon, the
network), files the committed `.gitignore` hides (a build folder), unlisted environment variables,
or a flaky test. Tests that depend on those are not candidates for reuse.

### Share a counted pass

Export an unexpired stored receipt by its key, and verify it elsewhere:

```sh
jst receipts export "$receipt_key" > evidence.json
jst receipts verify --sha256 "$expected_digest" --head "$expected_head" --key "$expected_key" evidence.json
```

The file holds the receipt key, the checkout's HEAD, the measured input manifest and the counted
outcome. It leaves out arguments, environment values, paths, toolchain output and test output.
Export prints the file's SHA-256 on standard error. Send the expected digest, HEAD and key over a
trusted channel or in pinned CI metadata; verification then needs neither the checkout nor the
receipt store. The digest is not a signature, and does not prove that an independent runner ran
the tests. HEAD alone says nothing about uncommitted inputs: the key describes them.

## The machine-wide lock: `jst lock`

Heavy commands from many sessions and projects on one machine take turns, in the order they came:

```sh
jst lock -- xcodebuild test -scheme App
jst lock --wait 600 -- ./run-integration.sh
jst lock status
```

The second gives up after 10 minutes, having run nothing (exit 75); `jst lock status` shows who
runs and who waits. `jst lock -- COMMAND` always locks. `jst test` takes the lock only when the
project sets `"lock": {"enabled": true}` (for its scopes, by each scope's `lock`) or the call says
`--lock`. Turn it on when simultaneous runs are shown to cause failures (`jst baseline` measures
that), not before. `--also-hold` also holds a project's older lock file, so its old script and
jackstay exclude each other. The lock is advisory: a command run without it is not stopped. Every
flag and exit status: `jst lock --help`.

## Measuring: `jst baseline`

```sh
jst baseline --repo . --since 2026-11-01 --until 2026-11-14
```

It reads and changes nothing. Over a span of UTC days it counts, from Claude Code transcripts, git
and jackstay's run record: test runs by kind and their time, merges, overlapping gate runs, every
destructive command the guard would refuse today, and whether failures go together with
simultaneous runs (the evidence for turning the lock on). Kinds come from `jackstay.json`'s
`baseline` section or `--kind NAME=REGEX`. `--examples FILE` writes each command the guard would
refuse to a file, which may hold private text. Without Claude Code transcripts only the git and
run-record parts have anything to read. Every flag: `jst baseline --help`.

## What can go wrong

| What you see | What to do |
|---|---|
| `outcome=exited-0` | Nothing counted the tests. Give the scope `judge: steps` and an `evidence` pattern with one number group, or a structured judge; for a linter, an audit or a build, `"kind": "check"`. |
| `not-started`: `receipts.toolchain` must be written out | Add `"receipts": {"toolchain": []}` (or your toolchain commands). |
| `unjudged` with `left-running=…` | The tests left processes behind; jackstay stopped them. Fix the test's cleanup. |
| `timed-out` | The scope's `timeout` ended the whole tree. Raise it, or split the scope. |
| A scope name refused | Only the six names, or `x-…`. `jst test list` shows yours. |
| `gave-up` (75) | The lock was busy past `--wait`; `jst lock status` shows who holds it. |
| `jst test` says to run it outside the sandbox | It records evidence (the run log, receipts) that Claude Code's sandbox may only read ([Claude Code](claude-code.md#the-sandbox)). Run it outside the sandbox. |
| `jst lock` exit 2 inside an agent sandbox | The lock is not among the folders the sandbox may write: run `jst setup` ([Claude Code](claude-code.md#the-sandbox)). |

The design: [testing.md](../design/testing.md), and for receipts, the lock and `jst baseline`,
[implementation.md, sections 5.4 and 9](../design/implementation.md#9-receipts-and-the-baseline-jst-test----command-jst-receipts-jst-baseline).
