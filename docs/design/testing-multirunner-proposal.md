# Proposal — one test evidence contract for multiple runners

Keep `jst test focused|changed|quick|full|display|gate` and `jst test -- COMMAND` as the user
interface. Separate test selection, process execution, evidence decoding, and verdict policy.
Start with Go and JUnit XML. Keep the existing Swift behavior and add a Swift-specific structured
report adapter when the target toolchain supplies evidence with the required fidelity.

Status: TST-1 to TST-8 are built (the last section); the names below that are not in
[testing.md](testing.md#21-structured-evidence) are provisional. Typed selectors and timings:
[Go](../guide/tests.md#select-a-go-test-by-id), [Jest](../guide/tests.md#select-a-jest-test-by-id),
[JUnit](../guide/tests.md#read-junit-case-durations); producers:
[structured test reports](../guide/tests.md#structured-test-reports). Unsupported selector or
duration settings are refused before execution.

## What the current code does

- One verdict ([`judge.Evaluate`](../../internal/judge/verdict.go)) serves the scope runner and the
  direct command ([direct evidence](../../internal/receipts/judge.go)); see the
  [compatibility baseline](testing-compatibility.md).
- `{tests}` expands to a regular expression for Swift selectors and `{slow}` expands to Swift
  classes. A test's identity, the runner's selector syntax, and the input to duration history are
  entangled. `{base}` already delegates change selection to the project's command.
  [Argument expansion](../../internal/testing/words.go) · [Duration history](../../internal/testing/durations.go)
- `supervise`, effective directories and environments, clean copies, and receipt keys are
  reusable across runners. The scoped path already exposes a run-specific temporary directory.
  Retain these boundaries and make direct runs use the same result and report handling.
  [Execution](../../internal/testing/command.go) · [Keys](../../internal/receipts/key.go) ·
  [Current scope contract](testing.md#1-scopes)

## Evidence from the runners

| Producer | First supported evidence | Edge to cover |
|---|---|---|
| Go | Stdout JSON from `go test -json -count=1` | Interleaved packages, package terminal events, build failures, nested tests, cached results and benchmarks. [Go test2json](https://pkg.go.dev/cmd/test2json) · [Go test command](https://pkg.go.dev/cmd/go/internal/test) |
| Python pytest | JUnit XML written with `--junitxml` and pytest exit status | Exit 5 means no tests collected. Collection and usage errors have separate meanings. Count runnable cases and keep skipped cases separate. [Pytest reports](https://docs.pytest.org/en/stable/how-to/output.html#creating-junitxml-format-files) · [Pytest exit codes](https://docs.pytest.org/en/stable/reference/exit-codes.html) |
| JVM | JUnit XML from Maven Surefire first | Validate each supported producer against pinned fixtures. A multi-module run needs distinct artifacts and a completion rule. [Surefire reports](https://maven.apache.org/surefire/maven-surefire-plugin/) |
| JavaScript and TypeScript | Vitest JUnit first, then native Jest-compatible JSON for Jest and Vitest | Verify an execution report and reject discovery results. Jest's collection mode can report cases it would run as passed. Prefer output files for structured reports. [Vitest reporters](https://vitest.dev/guide/reporters) · [Jest CLI](https://jestjs.io/docs/cli) |
| Swift | Current SwiftPM text adapter initially, structured Xcode reports in a later slice | Preserve mixed XCTest and Swift Testing counts, parameterized tests, cancellation and duration history. Prove an extraction contract for each supported Xcode range. [Apple Xcode release notes](https://developer.apple.com/documentation/xcode-release-notes/xcode-16-release-notes) |
| .NET | JUnit XML where the configured MTP extension supports it, TRX for VSTest and MTP in a later slice | The platforms have different flags and prerequisites. A fixed file name can be overwritten by another target framework. [VSTest CLI](https://learn.microsoft.com/en-us/dotnet/core/tools/dotnet-test-vstest) · [MTP test reports](https://learn.microsoft.com/en-us/dotnet/core/testing/microsoft-testing-platform-test-reports) |

Local probes confirmed: Go emits pass events for a parent and its subtest, replays them when
cached, and ends a no-match run with only a package-level pass; pytest writes an empty report and
exits 5 for a selector matching nothing.

## Proposed contract

- **Invocation** resolves argv, directory, environment, deadlines, selection, and the adapter
  name and version before execution. Selection is independent of the adapter. Keep
  `{tests}` and `{slow}` as Swift compatibility syntax. Add an argv-list expansion for typed
  test IDs, and document runner-specific selection as optional capabilities later.
- **Evidence** is a bounded, versioned result. It identifies the producer and attempt, separates
  completed and incomplete evidence, and records executed passes, failures, errors and skips.
  Case IDs carry producer, module, suite, case and parameter identity when available. An
  adapter can provide counts without case IDs, which remains useful for legacy regex scopes.
  Distinguish a zero count from a count that is unavailable.
- **Verdict** combines evidence with the process result once. Preserve the current public
  precedence for known failures, stops, unreadable output and leftovers. A counted pass requires
  complete evidence of positive execution and a successful process outcome. A valid empty or
  all-skipped result yields `nothing-run` for new adapters. An unsupported, missing, truncated
  or contradictory report with a successful process yields `unjudged`. Unknown nonzero exit
  codes remain failures. Map known empty-selection exits only in the applicable adapter.
- **Reports** are part of this invocation. For a file producer, allocate a private, unique output
  directory, pass its path through an explicit argument or environment value, wait for the
  process to finish, then read only declared regular files below it. Reject ambiguous globs,
  duplicate shard reports, paths outside the directory, symbolic links and oversized artifacts.
  Use bounded streaming readers for NDJSON and XML, with limits on bytes, case counts and depth.
  For XML, reject DTDs and external entities. Exclude raw diagnostics and environment from exported
  verification evidence.
- **Receipt identity** gains the evidence format and version, normalized selection and semantic
  runner options. Exclude only the fresh report-directory path as a transport detail. Keep all
  settings, tooling, source inputs and environment already used by the key. An adapter change
  never turns old, cached, discovered, partially parsed or reused evidence into a fresh pass.
  Historical receipt and run records remain readable.

The implemented transport supports one declared report per file adapter and an explicit list for
JUnit; adapter selection is explicit, and an unrecognized format never falls back to a parser that
might confirm a pass.

## Implementation sequence and verification

TST-1 to TST-8 are built: the compatibility fixtures, the common verdict, the per-attempt report
transport, the Go JSON and JUnit adapters, receipt and gate migration, the Vitest, Surefire, Jest
and TRX producers, and typed selectors and timings. Their contracts are
[testing.md](testing.md#21-structured-evidence) and the [tests guide](../guide/tests.md).

TST-9, structured Swift, derives separate SwiftPM and Xcode contracts from captured fixtures and
keeps the text adapter for projects that rely on it; see the
[SwiftPM and Xcode pilot](swift-structured-pilot.md). It stays opt-in until its fixtures
demonstrate the legacy contracts.
