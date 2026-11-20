# Tests

`jst test` runs a project's test command, reads its evidence, records the run and keeps its output.
Go JSON, JUnit XML, Jest JSON and VSTest TRX can supply structured evidence. Existing `steps`,
`swift-test` and `exit` judges remain available. A single command passes when its tests were counted. A gate line
shows each step's outcome. A pass is keyed by the exact tree it ran on, so a later run of the same
tree can be skipped where you opt in.

## Read the last `jst test:` line

Every run ends with one line on standard error naming its outcome. Read that line, not the exit
status alone: a test command may itself end with 2 or 77.

| Outcome | Meaning | Exit |
|---|---|---|
| `passed` | one command: ran now, ended 0, output read to its end, tests **counted** (above zero), no veto line. A gate: every step `passed`, `exited-0` or `resumed`, and at least one ran now — so a gate of exit-judged steps alone can be `passed` with no test counted (see each step's outcome on the same line) | 0 |
| `exited-0` | ended 0, but nothing counted its tests: **not a pass** | 0 |
| `reused` | nothing ran: a receipt for this tree exists and reuse is on | 0 |
| `resumed` | a gate under `--resume` where every step was skipped: nothing ran, not a pass | 0 |
| `failed` | ended non-zero, or a veto line was seen | the command's status, or 1 |
| `not-started` | `jackstay.json` or the lock could not be used, or a `requires` check failed; nothing ran | 2 |
| `refused` | `--resume` met a step that failed before on this same tree | 2 |
| `gave-up` | the machine-wide lock was not had within `--wait` | 75 |
| `nothing-run` | ended 0, but the output showed no test | 76 |
| `unjudged` | ended 0, but the output could not be read to its end, or it left processes or files that cannot be attributed | 77 |
| `passed-on-retry` | every test passed, but the report shows at least one that passed only after its runner retried it (a Surefire `flakyFailure`, a Jest assertion run more than once): **not a pass**, no receipt, and a gate step with it fails the gate | 78 |
| `timed-out` | a scope's timeout ended the whole process tree | 124 |
| `stopped` | `jst test` was asked to stop; it ends by that signal | — |

The command cannot start: 127.

## `jst test -- COMMAND`: any command

```text
jst test [--no-reuse] [--lock | --no-lock] [--project NAME] [--wait SECONDS] -- COMMAND [ARG ...]
```

Runs any command, recorded. It needs no `jackstay.json`; without one, a run that ends 0 is
`exited-0`, never `passed`. To make it count, list the command in `receipts.commands` with an
`evidence` pattern (below).

```text
jst test -- go test ./...
jst test last                # the last run again: outcome, what went wrong, where the output is
```

## Scopes: `jst test <scope>`

```text
jst test SCOPE [ARGS...] [--no-reuse] [--wait S] [--base REF] [--lock | --no-lock] [--resume] [--keep-going] [--commit REV]
jst test list [--project NAME]       # the scopes, what each runs, when the project runs it
jst test plan SCOPE [ARGS...]        # the words a scope would run, step by step; runs nothing
jst test auto [--base REF]           # choose and run a scope from changed paths
jst test changed [BASE..REV]         # same choice; a range runs on a clean copy of REV
jst test plan auto [--base REF]      # show the choice and command without running it
jst test plan changed BASE..REV      # show the choice and command for REV
jst test last [--scope S] [--json]   # the last run, without running anything
```

A project declares its scopes in `jackstay.json`'s `test` section. There are six names, with one
meaning each in every project; anything else starts with `x-`:

| Scope | Meaning |
|---|---|
| `focused` | the tests named on the command line: `jst test focused ParserTests` |
| `changed` | what a change since a base can affect, chosen by your own selector |
| `quick` | a broad look, less the slowest classes |
| `full` | the whole suite a headless run can do |
| `display` | what needs a real screen or device |
| `gate` | every step a change must pass to merge, in order |

When each is required is your rule, written once in the scope's `when`; `jst test list` prints it,
so your `AGENTS.md` can point there instead of restating it.

### Select a Go test by ID

The `focused` scope or an `x-…` scope with `selector: "go-test"` accepts one literal ID, written as
`PACKAGE::TestName[/subtest]`. Use the package import path and the test name from Go JSON
events. Each part of the test name is matched literally.

```json
"test": {
  "scopes": {
    "x-go-case": {
      "run": ["go", "test", "-json", "-count=1", "{test_args}"],
      "judge": "go-json",
      "selector": "go-test",
      "durations": true
    }
  }
}
```

```sh
jst test plan x-go-case 'github.com/zima-oss/jackstay/internal/testing::TestGoScopeRefusesUnsupportedCapabilitiesBeforeLaunch/wrong_judge'
jst test x-go-case 'github.com/zima-oss/jackstay/internal/testing::TestGoScopeRefusesUnsupportedCapabilitiesBeforeLaunch/wrong_judge'
jst test last --scope x-go-case --json
```

`{test_args}` expands to separate filter and package arguments. Go can run parent setup
while looking for a nested case and runs descendants of a selected case. jackstay checks the
selected ID against execution evidence. A matching parent that ran only to look for a missing
subtest does not count as a pass. Gate steps and automatic path-based selection cannot
supply a case ID. Swift scopes keep using `{tests}` and `{slow}`.

Set `durations: true` on a `go-json` scope to include completed, executed leaf timings on a
fresh counted pass. The run record prints up to 512 samples at `run.case_durations` in
`jst test last --json`, with `package`, `test` and `seconds`. Repeated execution samples stay
separate. `run.case_durations_truncated` means the sample limit was exceeded. Go timing samples
do not contribute to Swift's `{slow}` class history.
[Go subtest selection](https://pkg.go.dev/cmd/go#hdr-Testing_flags)

### Select a Jest test by ID

For a `focused` or `x-…` scope, configure `selector: "jest-test"` and
`judge: "jest-json"` with a declared `report`. Use this command:

```json
["jest", "--runInBand", "--no-watchman", "--json", "--outputFile={report}", "{test_args}"]
```

Pass one `FILE::full test name` argument, quoted as one shell word. The file is relative to
the scope's working directory. The name includes enclosing `describe` blocks and parameter
labels as Jest reports them. The selector passes an exact file path and a literal anchored
name to Jest. It then verifies both in the JSON report. An existing file with no matching
case yields `nothing-run`. Ambiguous or unrelated execution evidence yields `unjudged` when
the process succeeds. [Jest CLI](https://jestjs.io/docs/cli)

### Read JUnit case durations

A `junit` scope can set `durations: true` for one report or a declared report set. On a fresh
counted pass, `jst test last --scope SCOPE --json` includes up to 512 measurements in
`run.case_durations` across the whole set. Each sample contains the `suite` path, `class`,
`test`, and `seconds` from the producer. Skips and cases without a valid `time` have no sample.
A reused run has no fresh measurements. These values measure the producer's reported time,
which can include setup and teardown. [pytest time reporting](https://docs.pytest.org/en/stable/how-to/output.html#creating-junitxml-format-files)

### Choose a scope from changed paths

Declare `test.selection` to let `jst test changed` choose a scope from the paths changed since a base.
The `order` lists declared scopes from least to most strict and ends with `gate`. Rules map
repository-relative path globs to scopes. `*` matches within one path segment; `**` matches
zero or more complete segments. Every path touched by a commit counts, including a deleted path
and both names of a rename. The strictest matching rule wins. An unmatched path or an empty
range chooses `gate`.

```json
"receipts": {"toolchain": []},
"test": {
  "selection": {
    "order": ["quick", "full", "gate"],
    "rules": [
      {"glob": "docs/**", "scope": "quick"},
      {"glob": "Sources/**", "scope": "full"}
    ]
  },
  "scopes": {
    "quick": {"run": ["make", "docs-check"], "judge": "exit"},
    "full": {"run": ["make", "test"], "judge": "exit"},
    "gate": {"steps": ["full", "lint"], "when": "before merging or publishing"}
  },
  "steps": {"lint": {"run": ["make", "lint"], "judge": "exit"}}
}
```

```sh
jst test plan auto --base origin/main  # prints the choice, each matching rule, when and the command
jst test auto --base origin/main       # runs the selected scope with normal receipt handling
jst test changed origin/main..HEAD     # selects and runs on a clean copy of HEAD
jst test plan changed origin/main..HEAD # plans from that clean copy without running tests
```

Without `--base`, jackstay uses Git's `origin/HEAD` default ref. If that ref is unavailable, name
the base explicitly. A run in the working tree counts the committed range through `HEAD` and
the uncommitted and untracked paths it runs with (those `git status` lists; ignored files do not
count). `--commit REV` and `BASE..REV` run the selection and chosen scope on a clean copy of that
commit, so only its committed range counts. Use `jst test gate` for installation, tags and
release publishing, regardless of the scope selected for ordinary development feedback.
Projects without `test.selection` retain their declared `changed` scope and its existing runner.

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

Keys of a scope or step: `run` (the words; placeholders `{tests}`, the names given to `focused` as
one regular expression, made for `swift test --filter`; `{base}`, the scope's `base` or `--base REF`;
`{slow}`, the slow classes `quick` skips) or `steps`;
`judge` (`steps`: an `evidence` pattern with one number group counts the tests; `exit`: status only,
so `exited-0` at best; `swift-test`: built in for `swift test`; `go-json`: Go test events;
`junit`: declared XML reports; `jest-json`: a Jest result file; `trx`: a VSTest report);
`evidence` and `veto` (RE2);
`timeout` (seconds; the whole process tree is stopped at it); `requires` (checks run first); `env`;
`lock`; `when`. A scope that lists `steps` needs `receipts.toolchain` written out, even as an empty
list.

### Structured test reports

For Go, ask the runner for JSON events and select the `go-json` judge:

```json
"test": {
  "scopes": {
    "full": {"run": ["go", "test", "-json", "-count=1", "./..."],
             "judge": "go-json", "when": "before handing over"}
  }
}
```

The judge reads Go's stdout events, including package and nested-test results. Use `-count=1` for
a fresh execution. An incomplete event stream or cached result is `unjudged`. A filtered run with
no executed tests gives `nothing-run`.

For pytest, declare the report name and give its attempt-specific path to the runner:

```json
"test": {
  "scopes": {
    "full": {"run": ["python3", "-m", "pytest", "--junitxml={report}"],
             "judge": "junit", "report": "report.xml", "empty_exit": 5,
             "when": "before handing over"}
  }
}
```

`{report}` expands to a private path for this attempt. The same path is available to wrappers in
`JACKSTAY_TEST_REPORT`. The judge reads the declared XML after the command exits. It counts
executed cases and treats an all-skipped or empty report as `nothing-run`. `empty_exit: 5`
recognizes pytest's no-tests-collected status only when a valid empty report accompanies it. A
missing, unreadable or incomplete report is `unjudged` when the command otherwise succeeds.

Vitest can use the same JUnit judge with its file reporter:

```json
{
  "test": {
    "scopes": {
      "full": {"run": ["npx", "vitest", "run", "--reporter=junit", "--outputFile={report}"],
               "judge": "junit", "report": "vitest.xml", "when": "before handing over"}
    }
  }
}
```

Install Vitest in the project before running this scope. `run` makes this an execution report;
`--outputFile={report}` sends it to jackstay's private path for the attempt. In the verified
Vitest 5.1.1 producer, a matching pass plus skip exits 0 and counts one executed case, a failed
case exits 1, and a `--testNamePattern` matching no names exits 0 with all cases skipped.
`--passWithNoTests` with no matching files writes a zero-case report and exits 0. Both empty
results are `nothing-run`. Vitest 5.1.1 also has a two-shard producer check. To combine a fixed
Vitest shard plan, direct every shard to its own named JUnit file under `{report_dir}` and list
all files in `reports`. The command must check each shard's exit status. jackstay requires
all declared files and rejects duplicate cases across them. The shard count and output
mapping belong to the project's command. See [Vitest's reporter guide](https://vitest.dev/guide/reporters).

For JVM projects, [Maven Surefire](maven-surefire.md) can write separate XML files. List their
names in `reports` and direct the producer to `{report_dir}`. A missing file prevents the set
from proving a pass.

For Jest or .NET, use one file from a single runner invocation:

```json
"test": {
  "scopes": {
    "x-jest": {"judge": "jest-json", "report": "jest.json",
               "run": ["npx", "jest", "--json", "--outputFile={report}"]},
    "x-dotnet": {"judge": "trx", "report": "run.trx",
                 "run": ["dotnet", "test", "Tests.csproj", "--framework", "net11.0",
                         "--disable-build-servers", "--logger", "trx;LogFileName={report}"]}
  }
}
```

Jest's decoder reconciles its totals with executed assertions. Discovery output, including
`--collectTests` results with `wouldRun`, cannot prove a pass. VSTest's TRX reports are checked
against their individual test results and final counters. Use one test project and target
framework per TRX scope so the logger cannot overwrite an earlier file. Disable build servers
so no compiler process outlives the run. Producer checks use
Jest 30.5.2 and VSTest from .NET SDK 11.0.100 with MSTest 4.2.3. Each Jest shard or VSTest
filter partition uses its own command and report, so use separate scopes or gate steps. A
single report cannot prove coverage of the complete shard plan.

Disable retries for evidence that must prove a first-attempt pass. Jest JSON exposes repeated
invocations, so jackstay reports a successful Jest run with a repeated assertion as
`passed-on-retry` (exit 78), which is not a pass. A skipped or todo assertion with repeated
invocations makes the report `unjudged`. Vitest JUnit and VSTest TRX from MSTest can collapse a failed first attempt followed
by a pass into one final result. These reports cannot establish a first-attempt pass with
retries enabled. TRX from Microsoft.Testing.Platform is outside these checks.
[Jest CLI](https://jestjs.io/docs/cli) · [VSTest logger](https://learn.microsoft.com/en-us/dotnet/core/tools/dotnet-test-vstest)

For `jst test -- COMMAND`, declare the same judge and report settings in `receipts.commands`.
Direct wrappers receive `JACKSTAY_TEST_REPORT` for one file or `JACKSTAY_TEST_REPORT_DIR` for
multiple JUnit files. The [design proposal](../design/testing-multirunner-proposal.md) tracks
additional producers and selection capabilities.

### Judge an Xcode test plan

On the verified Xcode 27.2 contract, use `judge: "xcresult-json"` with
`reports: ["summary.json", "tests.json"]` and `xcresult_bundles` naming every expected unit
test bundle. Export both views from the same fresh `.xcresult` with
`xcrun xcresulttool get test-results summary` and `xcrun xcresulttool get test-results tests`.
The shell wrapper `tests/producers/swift/xcresult.sh` shows how to run the test plan and
write both files to `JACKSTAY_TEST_REPORT_DIR`. It keeps the `xcodebuild test` exit status.

The judge checks the reported bundles, the case tree and summary totals for one device and
configuration. It counts parameterized cases at the test case level and verifies the argument
results separately. A successful process with missing or inconsistent reports yields
`unjudged`. Add `durations: true` to record bounded case timings for a fresh counted pass.
SwiftPM mixed-engine scopes keep using the text judge. [Swift evidence pilot](../design/swift-structured-pilot.md)

### The gate

```sh
jst test gate                    # every step in order; stops at the first that is not passed or exited-0
jst test gate --keep-going       # runs the rest and reports each
jst test gate --resume           # skips steps that passed before on this same clean tree
jst test gate --commit HEAD      # on a clean copy of HEAD that jackstay makes and removes
```

```
jst test: outcome=passed (gate gate, 0.9 s: lint=exited-0, full=passed)
```

- The gate's line names each step's own outcome: `lint=exited-0` ran and ended 0 without counting
  tests; `full=passed` counted them. A step that is `exited-0` (or one that `passed`) can be
  resumed.
- `--resume` skips a step only on a clean checkout whose key (HEAD, the step's definition, the
  listed environment and toolchain, the refs it depends on) matches a step that passed. A step that
  failed on this tree is not run again: the gate ends `refused` until the tree changes.
- `--commit REV` runs on a clone that shares the repository's objects, so your uncommitted work
  cannot make it pass or fail. It has no ignored files (`node_modules`, build folders): a step that
  needs them builds them.

### After a run

Every run's output is kept in `~/.jackstay/<project>/outputs/` (the newest 30). A run that did not
pass prints up to 40 lines that say what went wrong, and where the whole output is. After a
failure, read `jst test last` instead of running again.

## Receipts

After a `passed` run, jackstay stores a receipt keyed by the tree: the files under
`receipts.inputs` (tracked, and untracked ones the committed `.gitignore` files do not hide), less
`receipts.exclude`, the command's words, the toolchain output, the listed environment variables and
the refs you name. A command you opt in is then not run again on the same key: it prints the receipt
and ends `reused`.

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

- Reuse is opt-in per command (`"reuse": true`, an exact `argv`, an `evidence` pattern), and needs
  `env` and `toolchain` written out.
- A command listed with `"reuse": false` (or by `argv_prefix`) is keyed and counted but always runs;
  each repeat is logged as `would-hit`, which measures what reuse would save before you turn it on.
- It never reuses on `main` or `master` (`receipts.no_reuse_branches`), nor when HEAD is the tip of
  such a branch; a receipt is used for `max_age_hours` (default 72).
- `--no-reuse` (or `JACKSTAY_NO_REUSE`) runs anyway.

```text
jst receipts key -- python3 -m pytest -q    # the key here, what went into it, whether it would be reused
jst receipts list [--project NAME]          # the stored receipts
jst receipts stats [--project NAME] [--json]   # runs, reuses and seconds saved
```

The key cannot see what lies outside the checkout (an SDK, a daemon, the network), files the
committed `.gitignore` hides (a build folder), unlisted environment variables, or a flaky test.
Tests that depend on those are not candidates for reuse.

### Share a counted pass

Export an unexpired stored receipt by key. The file holds the receipt key, checkout HEAD, measured
input manifest, and counted outcome. It omits arguments, environment values, paths, toolchain
output and test output.

```sh
jst receipts export "$receipt_key" > evidence.json
jst receipts verify --sha256 "$expected_digest" --head "$expected_head" --key "$expected_key" evidence.json
```

Export prints the SHA-256 of the exact file bytes to standard error. Send the expected digest,
HEAD and key over a trusted channel or in pinned CI metadata. Verification can run on another
machine without the original checkout or receipt store. It checks the record against those
expectations. The digest is neither a signature nor proof that an independent runner ran the
tests. The key describes the measured inputs and policy, so the checkout HEAD by itself says
nothing about uncommitted inputs.

## The machine-wide lock: `jst lock`

```text
jst lock [--project NAME] [--wait SECONDS] [--also-hold PATH=VARIABLE]... [--note-words WORDS] -- COMMAND [ARG ...]
jst lock status [--json]
```

Heavy commands from many sessions and projects on one machine take turns, in the order they came.
`jst lock -- COMMAND` always locks. `jst test` takes the lock only when the project sets
`"lock": {"enabled": true}` (for its scopes, by each scope's `lock`) or the call says `--lock`. The
lock is off by default: turn it on when simultaneous runs are shown to cause failures (see
`jst baseline`), not before.

```sh
jst lock -- xcodebuild test -scheme App
jst lock --wait 600 -- ./run-integration.sh   # give up after 10 minutes, having run nothing (exit 75)
jst lock status                               # who runs, who waits
```

Exit: the command's own status; 2 the lock or `jackstay.json` cannot be used (inside an agent
sandbox that cannot write `~/.jackstay`, for one); 75 gave up waiting; 127 the command cannot start.
`--also-hold` also holds a project's older lock file, so its old script and jackstay exclude each
other. The lock is advisory: a command run without it is not stopped.

## `jst check` and `jst patterns`

```text
jst check [DIR]        # exit 0 usable (perhaps with warnings), 2 a problem or no jackstay.json
jst patterns [DIR...]  # exit 0 every pattern is RE2, 1 some are not, 2 a file is missing or not JSON
```

`jst check` also warns about the `test` section: a scope with no timeout, and a gate that runs a
slow step (over 60 s, by the recorded median) before a fast one (under 10 s).

## Measuring: `jst baseline`

```text
jst baseline --repo . --since 2026-11-01 --until 2026-11-14 [--json]
```

Reads, and changes nothing. Over a span of UTC days it counts, from Claude Code transcripts, git
and jackstay's run record: test runs by kind and their time, merges, overlapping gate runs, every
destructive command the guard would refuse today, and whether failures go together with
simultaneous runs (the evidence for turning the lock on). Kinds come from `jackstay.json`'s
`baseline` section or `--kind NAME=REGEX`. `--examples FILE` writes each command the guard would
refuse to a file; it may hold private text. Without Claude Code transcripts, only the git and
run-record parts have anything to read (`--projects-dir` names another transcript folder).

## What can go wrong

| What you see | What to do |
|---|---|
| `outcome=exited-0` | Nothing counted the tests. Give the scope `judge: steps` and an `evidence` pattern with one number group, or `swift-test`. |
| `not-started`: `receipts.toolchain` must be written out | Add `"receipts": {"toolchain": []}` (or your toolchain commands). |
| `unjudged` with `left-running=…` | The tests left processes behind; jackstay stopped them. Fix the test's cleanup. |
| `timed-out` | The scope's `timeout` ended the whole tree. Raise it, or split the scope. |
| A scope name refused | Only the six names, or `x-…`. `jst test list` shows yours. |
| `gave-up` (75) | The lock was busy past `--wait`; `jst lock status` shows who holds it. |
| `jst lock` exit 2 inside an agent sandbox | The sandbox cannot write `~/.jackstay`. Allow it ([Claude Code](claude-code.md#the-sandbox)), or run heavy runs outside the sandbox. |

The design: [testing.md](../design/testing.md), and for receipts, the lock and `jst baseline`,
[implementation.md, sections 5.4 and 9](../design/implementation.md#9-receipts-and-the-baseline-jst-test----command-jst-receipts-jst-baseline).


### Default timeout for prerequisite commands

A `requires` command has 60 seconds unless it gives its own `timeout`.
This applies to checks attached to command scopes, steps and composite gates.
