# Test verdict compatibility baseline

This baseline fixes the observable contract before adding structured runners. The fixtures below
exercise both `jst test -- COMMAND` and `jst test SCOPE` through their usual entry points. The
same outcome appears in the run record and in `jst test last`. A counted pass may create a
receipt when the configured inputs and toolchain permit it. Every other outcome removes an
earlier receipt for that key.

| Input and process end | Direct command | Scope | Fixture |
|---|---|---|---|
| Positive count, exit 0 | `passed`, exit 0, receipt eligible | `passed`, exit 0, receipt eligible | `TestAPassWithEvidenceStoresAReceipt`, `TestAScopeRunsAndIsJudgedAndRecorded` |
| No count, exit 0 | `nothing-run`, exit 76 | `nothing-run`, exit 76 | `TestOutcomesAndExitStatuses`, `TestNothingCountedIsNothingRun` |
| No counting pattern, exit 0 | `exited-0`, exit 0, no receipt | `exited-0`, exit 0, no receipt | `TestACommandThatIsNotOptedInOrNotListed`, `TestExitJudgeIsNeverPassed` |
| Veto, exit 0 | `failed`, exit 1 | `failed`, exit 1 | `TestOutcomesAndExitStatuses`, `TestAVetoFails` |
| Incomplete output or uncertain process cleanup | `unjudged`, exit 77 | `unjudged`, exit 77 | `TestOutcomesAndExitStatuses`, `TestWhatIsLeftAfterANormalEndIsStoppedAndTheRunIsNoPass` |
| Explicit stop after a failure line | `stopped`, signal exit | `failed`, signal exit | `TestEvaluateUsesOneVerdictForDecodedEvidence`, `TestAFailureSeenBeforeAStopIsAFailure` |
| Negative evidence group | `unjudged`, exit 77 | `unjudged`, exit 77 | `TestNegativeEvidenceCannotCreateADirectPass`, `TestNegativeEvidenceCannotCreateAScopePass` |

The direct command retains its established stop precedence and its console wording. Scopes
retain failure before stop, timeout, unjudged, zero and pass precedence. The negative group is
a deliberate correction. Earlier scope counting could subtract a negative match from positive
matches and permit a pass. Both entry points now treat the group as invalid evidence and keep
the positive count as diagnostic data.

Swift fixture coverage stays under `tests/fixtures/swift-test`. `TestRecordedOutputs` pins the
filtered XCTest totals, mixed XCTest and Swift Testing counts, skipped cases and a parallel run
with a known failure. `TestReadingRules` pins zero and all-skipped runs, incomplete bundles,
parallel progress without a failure, and a stopped run. `TestStepsRunInOrderAndStopAtTheFirstFailure`
pins the gate's step outcomes and `jst test last --scope gate` output. These fixtures remain
relevant when structured adapters are added because the visible outcome vocabulary is shared.

`judge.Evaluate` combines normalized evidence facts with process status, stop, output and
cleanup facts. The direct and scoped callers retain their own record and console formatting.
File-based reports require a private per-attempt path and a post-exit read. A scope's
`JACKSTAY_RUN_TMP` is already per attempt, while the direct command needs equivalent transport.
Report validation must reject a stale file, symbolic link or incomplete write before a counted
pass is possible.
