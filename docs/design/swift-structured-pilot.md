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

The strict JUnit reader in jackstay correctly refuses the complete SwiftPM XML because the
declared total disagrees with the testcase count. Even a looser XML reader could not prove the
XCTest portion because SwiftPM does not put it in this output. The current `swift-test` text
judge remains the usable mixed-package contract for this toolchain. A SwiftPM structured adapter
needs a complete source of evidence for both engines and a producer-specific rule for
parameterized cases before it can confirm a counted mixed pass. The fixture's stable test list
includes `LegacyTests.LegacyTests/testPass` and `ModernTests.modernPass()`. Filtering with either
name runs the expected engine in the captured probe.

A fresh `jst` run over the captured complete XML returned `unjudged` with exit 77 and a suite
count mismatch. A fresh `jst` run using the legacy `swift-test` judge returned `passed` with
`counted=5`, `xctest=2`, `swift-testing=3`, `skipped=2` and one completed XCTest bundle. The
controlled failure returned `failed` with exit 1 and the same test counts. This comparison
checks the current migration boundary on the actual producer output.

`swift test --help-hidden` on this toolchain exposes `--event-stream-output-path` and
`--event-stream-version`. Version 0 produced JSON Lines for Swift Testing. A complete mixed run
had 19 records, including two `runStarted` and two `runEnded` events because the two test
products each start a Swift Testing process. The events include a test identity with source
location and separate `testCaseStarted`/`testCaseEnded` records for parameterized arguments.
The argument records carry `_testCase.id` and a display name. This stream contains no XCTest
case events. A future reader must bind every expected test product, recognize empty Swift
Testing products, require terminal events, and pair the stream with a separate XCTest source.
The upstream versioned JSON schema should be used for that reader. [Swift Testing JSON
schema](https://github.com/swiftlang/swift-testing/blob/main/Documentation/ABI/JSON.md),
[Swift Testing JSON ABI proposal](https://github.com/swiftlang/swift-evolution/blob/main/proposals/testing/0002-json-abi.md)

Xcode's `.xcresult` from `xcodebuild test` covers both targets. `xcresulttool get test-results
summary` reported a passed result with `totalTestCount: 7`, `passedTests: 5` and
`skippedTests: 2`. Its device/configuration subtotal reported six passes and two skips because
it also reflects the two parameterized arguments. The `tests` tree gives XCTest case nodes and
Swift Testing case nodes, with two argument children under `parameterized(_:)`. The children
have their own outcomes, numeric `durationInSeconds` and argument-qualified URLs. The adapter
must choose one counting level and reconcile it against the summary. The script's forced Xcode
failure variant returned exit 65. Its summary reported `result: Failed`, `totalTestCount: 7`,
three passes, two skips and two failures. The tree identified one failed XCTest method and one
failed Swift Testing test.
Raw xcresult JSON also contains local device identifiers and paths, so only the bounded fields
needed for judging should enter receipts or diagnostics. [Apple test results
documentation](https://developer.apple.com/documentation/xcode/running-tests-and-interpreting-results),
[Xcode 16 structured test-results commands](https://developer.apple.com/documentation/xcode-release-notes/xcode-16_3-release-notes)

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

1. Find a complete XCTest source for direct SwiftPM execution, including skips and selected
   tests. Its existing `--xunit-output` has now been tested both in mixed and isolated modes and
   does not meet that contract. Pair it with Swift Testing's versioned event stream only after
   both producers can prove product completion and selected coverage.
2. Capture additional Xcode and Swift versions, selected runs, zero-match runs and interrupted
   runs. Expand the supported schema only after fixtures prove the corresponding count and
   completion semantics. Keep the existing Swift text judge available.
3. Compare counted, skipped, failed and selected cases against the text judge on each supported
   version before changing a default. The Xcode adapter remains an opt-in path for verified
   artifacts.

The current probe establishes a mixed pass, mixed failure and both engine selections. It has
not established a reliable SwiftPM XCTest structured source, a cancellation record, or a
cross-version schema range. Those cases remain prerequisites for enabling a structured SwiftPM
judge or broadening the Xcode adapter's version range.
