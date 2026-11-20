# TST-9 Swift structured evidence pilot

The first TST-9 probe uses the mixed [Swift package fixture](../../tests/producers/swift/) on
Swift 6.4.2 and Xcode 27.2. Run `sh tests/producers/swift/capture.sh` on macOS to copy the fixture
to a temporary directory, execute the cases below and retain the raw logs, XML, JSON Lines and
Xcode result bundles there. The script prints the capture path. The source has one XCTest class
and a Swift Testing target with a disabled test and a two-argument parameterized test. A
controlled environment variable triggers one failure in each engine for SwiftPM. The Xcode
failure probe changes only the temporary copy's two assertions.

## Observed producer behavior

| Run | SwiftPM console and status | Structured artifact |
|---|---|---|
| Complete pass | Exit 0. XCTest reports 3 tests, 1 skip. Swift Testing reports 4 tests, including one disabled test and one parameterized test summarized once. | `--xunit-output pass.xml` writes `pass-swift-testing.xml`. It contains the Swift Testing cases and no XCTest cases. Its suite says `tests="3"`, while it has four `<testcase>` children. The parameterized arguments become one case. |
| Failure in both engines | Exit 1. XCTest reports 3 tests, 1 skip, 1 failure. Swift Testing reports 4 tests, 1 issue. | `failed-swift-testing.xml` records only the Swift Testing failure and its final case, with no XCTest failure. |
| Select one Swift Testing test | Exit 0. Swift Testing reports 1 test. | The Swift Testing XML has one case. |
| Select one XCTest method | Exit 0. XCTest reports one executed test. | The Swift Testing XML has zero cases and does not describe the XCTest method. |
| Select a nonexistent test | Exit 0 with a no-match warning. | The Swift Testing XML contains zero cases. |
| Isolate XCTest with `--enable-xctest --disable-swift-testing --parallel` | Exit 0. XCTest executes three methods, including one skipped method. A controlled failure exits 1. | `--xunit-output` writes three XCTest cases and marks the controlled failure, but emits the skipped method as an ordinary case without a `<skipped>` element. Its pass and failure totals cannot establish the true skip count. |

jackstay's strict JUnit reader refuses this XML (the declared total disagrees with the cases), and
it holds no XCTest cases at all, so the `swift-test` text judge stays the mixed-package contract.
A fresh `jst` run over the captured XML returned `unjudged` (77); the text judge returned `passed`
with `counted=5`, `xctest=2`, `swift-testing=3`, `skipped=2`, and `failed` (1) for the controlled
failure. Filtering with `LegacyTests.LegacyTests/testPass` or `ModernTests.modernPass()` runs the
expected engine.

The hidden `--event-stream-output-path` (version 0) writes Swift Testing JSON Lines: a mixed run had
19 records, with a `runStarted`/`runEnded` pair per test product and per-argument
`testCaseStarted`/`testCaseEnded` records, but no XCTest events. A reader must bind every product,
recognise empty ones, require terminal events, pair the stream with an XCTest source, and follow
the upstream [JSON schema](https://github.com/swiftlang/swift-testing/blob/main/Documentation/ABI/JSON.md).

Xcode's `.xcresult` covers both engines. `xcresulttool get test-results summary` reported
`totalTestCount: 7`, 5 passed, 2 skipped; the device subtotal counted the two parameterized
arguments separately, and the `tests` tree gives them child nodes with their own outcomes and
durations, so an adapter must pick one counting level and reconcile it. The forced Xcode failure
exited 65 with 3 passed, 2 skipped, 2 failed. Raw xcresult JSON holds device identifiers and
paths, so only bounded judging fields may enter receipts or diagnostics
([Apple test results](https://developer.apple.com/documentation/xcode/running-tests-and-interpreting-results)).

## Opt-in Xcode adapter

The `xcresult-json` adapter reads `summary.json` and `tests.json`, exported in that order by
`xcrun xcresulttool get test-results summary` and `tests` from the same completed `.xcresult`
bundle. A scope declares the test bundle names it expects. The first supported shape has one
device, one configuration and one test plan. The decoder requires every declared bundle,
valid terminal case and container outcomes, unique case identities, and exact agreement between
the case tree and summary totals. It counts a parameterized test at the parent case level and
checks the device subtotal after accounting for its argument results. Missing, malformed,
oversized or contradictory evidence is unjudged. The adapter never treats a partial Swift
Testing XML report as evidence for XCTest.

The checked-in [sanitized snapshots](../../internal/judge/testdata/xcresult/) retain only
judgment fields from the captured Xcode 27.2 pass and mixed failure. The integration test can
re-export both artifacts with `XCRESULT_PILOT_ROOT=<capture directory> go test -tags integration
./internal/judge -run TestXCResultToolExportsMatchThePilot`. This validates the parser against
real `xcresulttool` output on that version. Test case durations are bounded and come from
completed case nodes; device identities, source paths and raw JSON do not enter the samples.

## Remaining migration contract

1. Find a complete XCTest source for direct SwiftPM runs, with skips and selections:
   `--xunit-output` does not meet it, mixed or isolated. Pair it with Swift Testing's event stream
   only once both prove product completion and selected coverage.
2. Capture more Xcode and Swift versions, selected, zero-match and interrupted runs, and widen the
   schema only where fixtures prove the counts.
3. Compare against the text judge on each version before changing a default; the Xcode adapter
   stays opt-in.

Not yet established: a reliable SwiftPM XCTest source, a cancellation record, a cross-version
schema range.
