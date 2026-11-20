# Test verdict compatibility baseline

The observable contract of `jst test -- COMMAND` and `jst test SCOPE`, pinned by the fixtures below.
The run record and `jst test last` show the same outcome; only a counted pass may create a receipt,
and every other outcome removes the key's earlier one.

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
retain failure before stop, timeout, unjudged, zero and pass precedence. Both entry points treat
a negative evidence group as invalid evidence, so it can never be subtracted into a pass, and keep
the positive count as diagnostic data.

Swift fixtures live under `tests/fixtures/swift-test`: `TestRecordedOutputs` (filtered, mixed,
skipped and parallel runs), `TestReadingRules` (zero, all-skipped, incomplete, stopped runs) and
`TestStepsRunInOrderAndStopAtTheFirstFailure` (the gate and `jst test last --scope gate`).
`judge.Evaluate` combines the evidence with process status, stop, output and cleanup facts for both
callers. A file report is read from a private per-attempt path after exit; a stale file, a symbolic
link or an incomplete write never yields a counted pass.
