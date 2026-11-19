# Proposal — one test evidence contract for multiple runners

Keep `jst test focused|changed|quick|full|display|gate` and `jst test -- COMMAND` as the user
interface. Separate test selection, process execution, evidence decoding, and verdict policy.
Start with Go and JUnit XML. Keep the existing Swift behavior and add a Swift-specific structured
report adapter when the target toolchain supplies evidence with the required fidelity.

This is an implementation plan. The names and configuration examples below are provisional.

The first implementation uses the [compatibility baseline](testing-compatibility.md), a shared
verdict, bounded per-attempt report transport and opt-in Go JSON and JUnit judges.
See the [implemented contract](testing.md#21-structured-evidence) for current configuration.

## What the current code does

- The scope runner creates a `judge.Reader` and derives a verdict from decoded output, process
  termination and leftovers. The direct command path uses its own regex reader and decision
  sequence. It rejects a negative count that the scope reader can sum. Both write run and receipt
  records, so this difference can change what a user sees and which runs can be reused.
  [Scope verdict](../../internal/judge/verdict.go) · [Direct evidence](../../internal/receipts/judge.go)
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

Local probes in disposable fixtures confirmed three cases. Go emitted pass events for both a
parent test and its subtest. A cached Go rerun replayed those events, and a no-match Go run
ended with a package-level pass and no test-level pass. Pytest emitted two XML cases for one
pass and one skip, and an empty XML report with exit 5 for a selector matching nothing. The
other adapters in the table are proposals based on their published formats.

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

An opt-in starting shape could look like this:

```json
{
  "test": {
    "scopes": {
      "x-go": {
        "run": ["go", "test", "-json", "-count=1", "./..."],
        "judge": "go-json"
      },
      "x-python": {
        "run": ["pytest", "--junitxml={report}"],
        "judge": "junit",
        "report": "pytest.xml"
      }
    }
  }
}
```

The examples require schema work. The prototype supports one declared report. Java, .NET and
sharded workflows need an explicit list of outputs or a producer-provided manifest. Adapter
selection is explicit. An unrecognized format must never fall back to a parser that might
incorrectly confirm a pass.

## Implementation sequence and verification

| Task | Depends on | Deliverable and done check |
|---|---|---|
| TST-1 Compatibility fixtures | None | Capture raw and scoped outcomes, exit status, console summaries, `test last`, receipts and gate marks for the same cases. Pin the Swift parallel, empty, skipped and cancelled examples. Record agreed fixes, such as negative count handling, as deliberate differences. |
| TST-2 Common verdict | TST-1 | Extract one reducer over execution and evidence. Reuse it in both current entry points with a small compatibility policy for existing exit statuses and diagnostics. Prove parity in paired runs, including a stop, a veto line, a zero exit without evidence, and process cleanup failure. |
| TST-3 Artifact transport | TST-2 | Use a private per-attempt report directory for raw and scoped runs. Test stale files, symlinks, missing file, partial file, two concurrent runs and cancellation. Use synthetic reports to keep the core gate independent of additional runtimes. |
| TST-4 Go NDJSON adapter | TST-2 | Exercise real Go fixtures in a dedicated integration job. Check interleaved packages, cached replay, filtered zero match, skip, subtests, repeated tests, build failure, benches, and abrupt end. Start by using the adapter in jackstay's own Go recipe and compare it to the existing counter before migration. |
| TST-5 JUnit XML and pytest | TST-2, TST-3 | Read case results and compare them with applicable suite counts. Prove all-skipped, failure, empty selection, setup error, nested suites, oversized and truncated reports. Run the same fixture via `jst test` and verify the saved record and receipt. |
| TST-6 Receipt and gate migration | TST-2 and first structured adapter | Key results by semantic adapter inputs. Old keys remain readable for reporting where the configured format changes. Verify cache miss on an adapter version change, normal reuse within one version, clean-copy fresh runs, and that a resumed step cannot require a missing artifact. |
| TST-7 Additional producers | TST-5 or TST-4, according to format | Onboard Vitest and Surefire through verified XML dialect fixtures, then Jest JSON and .NET TRX. Use pinning and per-producer CI. Give every producer at least one real invocation per supported version range and cover retries and sharding before accepting those modes. |
| TST-8 Selection and duration capabilities | TST-2, stable IDs from TST-4 and TST-5 | Keep old Swift selection and introduce typed selectors where an adapter genuinely supports them. Measure selections against real fixture IDs. Report unsupported capabilities before launch. |
| TST-9 Structured Swift migration | TST-2, TST-3 | Derive separate SwiftPM and Xcode contracts from captured fixtures. Compare the two Swift testing engines with legacy output on supported versions. Keep the text adapter available for projects that rely on it. |

The first release should close TST-1 through TST-6 and document the API limits. TST-7 producer
lanes can run in parallel once their format is supported. TST-8 and TST-9 remain opt-in until
their fixtures demonstrate the legacy contracts.
