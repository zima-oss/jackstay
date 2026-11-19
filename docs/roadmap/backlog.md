# Backlog assessment and implementation briefs

This assessment began with the eight open issues in `zima-oss/jackstay` read on 2026-11-19 and
the local audit plan at commit `83673ea`. GitHub Issues remains the public backlog. The A1–A5
briefs below are proposals from that local plan and have no issue number yet. The issue links
are the source for requested behavior. Priorities here are recommendations for scheduling, not
issue labels. The briefs retain their original acceptance contracts; the delivery audit below
records what is implemented now.

## Current baseline

`jst inventory`, portable receipt evidence, resumable local adoption and changelog collection,
`jst timeline`, and portable profile bundles provide the first slices of A1–A5. The reviewed
tree also has optional Codex and Claude Code review adapters. The current test contract and its
multi-runner proposal are described in [testing](../design/testing.md) and the
[test evidence plan](../design/testing-multirunner-proposal.md). Each next slice below starts
from those shipped interfaces and must preserve readable older records.

## Delivery audit, 2026-11-19

The original issue briefs below are historical implementation plans, not a list of unstarted
work. The current tree has these independently checkable local slices:

| Brief | Delivered slice | Remaining boundary |
|---|---|---|
| B1 / #13 | [Check reconciliation](../../internal/gh/checks.go) selects a newer compatible run; fake-provider tests cover supersession. | GitHub still decides which checks branch rules require at landing. |
| B2 / #20 | The [process integration fixture](../../internal/procs/background/background_integration_test.go) waits for observable readiness. | The original intermittent Linux failure cannot be declared impossible from a local pass; keep CI observation. |
| B3 / #11, #1 | [Release attestation](../guide/install.md) and strict verification supplement checksums. Repository intake templates and a [tap preparation path](../guide/homebrew-tap.md) exist. | The Homebrew tap is not published; Homebrew core eligibility is external. |
| B4 / #5 | [`jst test auto` and `changed`](../guide/tests.md) choose a configured scope from committed paths. | Project-specific path rules remain configuration data. |
| B5 / #4 | [`jst setup`](../guide/install.md#plan-machine-setup) plans and applies reversible PATH and Claude sandbox repairs. | App credentials and optional Codex integration are separate setup work; do not present this as complete machine provisioning. |
| B6 / #3 | A [local single-PR queue](merge-queue-pilot.md) prepares a tested candidate, checks remote freshness and records a manually landed tree. The Free organization pilot verified reconciliation. | Explicit operator decision recording, protected automatic landing proof, ordered batches and bisection remain open. |
| B7 / #2 | The [GitHub App pilot](github-app-pilot.md) verified organization registration, installation and scoped token use. | A general interactive setup, rotation and doctor readiness flow are not shipped. The remaining pilot matrix needs evidence before those claims. |
| B8 / chat | [Local MCP session chat](session-chat.md) has durable inboxes and ACK recovery, with managed Codex delivery and Claude lifecycle notification. | Claude active-turn delivery is not yet established; a lifecycle notice is a weaker guarantee. |
| TST-1–TST-8 | Shared verdict, bounded report transport, Go/JUnit/Jest/TRX producer evidence, retry and partition checks, typed Go/Jest selection and durations are [documented](../guide/tests.md). | Generic JUnit cannot promise a runner-specific selector. |
| TST-9 | [SwiftPM and Xcode pilots](../design/swift-structured-pilot.md) establish distinct contracts; Xcode can consume verified XCResult JSON. | SwiftPM's mixed XCTest/Swift Testing output lacks a complete structured source; the text judge remains supported. |

The remaining boundaries are tracked product work or evidence gaps, rather than abandoned local
implementations. A1 policy explanation, A2 remote reconciliation, A3 trusted receipts and A4
workspace aggregation have initial slices; [profile staging](../guide/adopt.md) implements the
first A5 rollout path. Each still needs the end-to-end proof stated in its direction below.

The audit also found an independent correctness gap in existing trash handling: a restored entry
could be replaced by another entry with the same second-based ID. New
[trash entries](../../internal/trash/trash.go) now have a random ID suffix so a stale restore or
prune reference cannot name a later entry. Existing counter-only IDs remain readable.

## Issues and proposals

| Priority | Issue | Assessment and first deliverable |
|---|---|---|
| P1 | [#13 Cancelled check run](https://github.com/zima-oss/jackstay/issues/13) | Relevant. The current check grouping [counts cancelled runs as failures](../../internal/gh/checks.go). Reconcile a cancelled run with a newer run of the same workflow and commit before deciding the PR result. |
| P1 | [#20 Flaky Linux process test](https://github.com/zima-oss/jackstay/issues/20) | Relevant. The [integration test](../../internal/procs/background/background_integration_test.go) has condition-based polling helpers, while the reported failure's root cause remains unproved. Reproduce it and add the missing readiness condition at the failing transition. |
| P1 | [#11 Release download provenance](https://github.com/zima-oss/jackstay/issues/11) | Relevant. The current [installer](../../install.sh) and [adopted workflow](../../internal/adopt/templates/files/release.yml) verify a checksum fetched from the same release. Specify an independently trusted provenance or pinned digest path, then pilot it for one download. |
| P1 | Local agent session chat | Let parallel agents discover each other and exchange direct messages, requests and replies. Persist delivery and acknowledgment locally. [Product specification](session-chat.md). |
| P2 | [#5 Select a test scope from changed paths](https://github.com/zima-oss/jackstay/issues/5) | Relevant for fast agent feedback. The existing [scope runner](../../internal/testing/commands.go) requires the caller to choose a scope. Define explicit ordering between scopes and a conservative fallback before adding automatic selection. |
| P2 | [#4 Idempotent machine setup](https://github.com/zima-oss/jackstay/issues/4) | Relevant for onboarding. [Doctor](../guide/observability.md) already diagnoses local setup. Begin with a plan and one reversible machine setting, with terminal approval for sensitive file changes. |
| P2 | [#1 Install channels](https://github.com/zima-oss/jackstay/issues/1) | Split by value. Build attestations belong with #11. Issue and pull request templates can ship independently. A tap needs release automation and a macOS installation check. Homebrew core has an external eligibility gate. |
| Strategic | [#3 Merge queue](https://github.com/zima-oss/jackstay/issues/3) | High potential impact and high correctness risk. Build an app-managed queue with a tested-candidate handoff on ordinary GitHub plans. Enable protected direct landing only where an exact-tree precondition is proved. |
| Discovery | [#2 GitHub App setup](https://github.com/zima-oss/jackstay/issues/2) | Relevant to projects using the release workflow. Start by proving the App permission, installation and secret flows in a disposable GitHub repository. The proposed credential flow requires a separate threat review before implementation. |

The first two issues affect the trustworthiness of an existing gate or landing command. #11
affects downloads in the installation and adoption paths. #5 and #4 improve repeated daily use.
The queue and App setup need verified GitHub behavior and a pilot before their interfaces settle.

## Issue briefs

### B1. Reconcile superseded checks, #13

**Contract.** A required cancelled check is superseded only when a newer run belongs to the same
repository, workflow and PR head commit. The replacement remains pending until its required
check finishes. A cancellation without a matching replacement is reported as cancelled with its
run identifier. A failed replacement remains a failure. Branch protection and the expected base,
head and test-merge identities are checked again at landing.

**Sequence and proof.** Add a failing fake GitHub test to `internal/gh/checks_test.go` for a
cancelled run followed by a newer pending, passing or failing run. Include unrelated workflows,
another SHA, a missing run and a changing PR head. Implement bounded run lookup through the
existing GitHub client and reuse it from `pr-land` and check waiting. Verify that each transition
reports the selected run and that no merge occurs on unknown identity. Run the `internal/gh`
integration suite and full gate. Depends on the existing G1–G3 identity work.

### B2. Remove timing from the Linux process test, #20

**Contract.** The fixture waits for a concrete readiness signal after child registration and
then checks listing and stopping by name. It does not require an idle runner or a fixed startup
interval. A failed readiness wait reports the process state and cleans up its child.

**Sequence and proof.** Reproduce the reported failure against Linux with repeated race-enabled
runs. Inspect the existing condition-based polling helpers and identify the unobserved transition
in `TestRealKeptIsListedAndStoppedByName`. Add a bounded readiness condition using the existing
wait or registry interface. Run the target test at least 200 times under load, then the Linux
integration gate. Preserve the macOS process test. This can proceed independently of B1.

### B3. Establish download provenance, #11 and part of #1

**Contract.** The release workflow produces an attestation bound to the archive digest, source
repository, workflow and commit. A project pin can carry an independently recorded digest.
Verification uses an explicitly configured trust source and refuses a mismatch before unpacking
or running the binary. Existing checksums remain a corruption check. A verification failure names
the artifact and the failed condition without printing credentials.

**Sequence and proof.** First pin the release and workflow identities and test a downloaded
artifact with a real published attestation in a disposable project. Decide the verification path
for machines without `gh` and for offline installs. Add the publisher, then one verifier in
`install.sh`, then the shared adopted workflow download template. Update template goldens and run
`jst adopt --plan` in jackstay. Test valid, changed archive, changed provenance and unavailable
verifier cases on macOS and Linux. This work depends on the release artifact format and A5 bundle
pin semantics. The separate tap and repository templates from #1 can proceed after this proof or
in an independent documentation lane.

### B4. Select a scope from a commit range, #5

**Contract.** Configuration maps changed path patterns to declared scopes and explicitly orders
their strictness. All touched paths count, including both names of a rename and deleted paths.
Unmatched paths choose `gate`. A range without a known default ref requires an explicit base.
The command prints the selected scope and every rule that raised it. Installation, tags and
release publishing still require the full gate.

**Sequence and proof.** Record fixtures for Markdown-only, code-only, mixed, renamed, deleted and
unknown paths. Extend schema validation and `jst test plan` before execution. Use Git's NUL
delimited name-status output and existing scope execution, receipt and clean-copy handling.
Reject ambiguous or cyclic scope precedence. Prove the chosen scope and `when` explanation,
then run race-enabled `internal/testing` integration tests and the full gate. Align with the
shared verdict from TST-2 so selection does not invent a second pass rule. This feature is
independent of Go JSON and JUnit XML parsing.

### B5. Finish machine setup, #4

**Contract.** `jst setup --plan --json` reports changes derived from doctor checks. Interactive
setup offers apply, show and skip for each owned or third-party file. A second run makes no edits.
A nonterminal run only reports the plan. `--yes` applies only the items the caller explicitly
selects. Before editing a third-party file, show its diff and retain a recoverable copy.

**Sequence and proof.** Add a shared doctor finding identifier and one apply handler for the
install bin path. Next handle Claude Code state access, then the optional Codex guard and review
integration, and finally `gh` diagnostics. Verify each item with a fake home, terminal and
nonterminal input, repeat invocation, conflicting user edits, and trash recovery. Check shell
syntax on macOS and Linux. The project file remains under `jst adopt`.

### B6. Pilot a merge queue, #3

The [merge queue pilot](merge-queue-pilot.md) defines a local queue, a manual handoff for ordinary plans and the provider preconditions for enabling automatic landing.

**Contract.** A per-repository queue records PR head, base and required check identities, the
exact candidate tree, its test evidence and the observed landing effect. One writer owns a
repository lane. A stale candidate is retested. A batch that fails may be split into smaller
candidates, but no candidate lands until its exact tree has a fresh counted gate pass and the
required review and remote checks are current. The queue reports uncertain remote effects for
operator reconciliation.

**Sequence and proof.** The local single-head lane pins refs, prepares a candidate, runs the
gate and exports counted evidence for manual handoff. Linked worktrees share the lane.
[Queue commands](../guide/merge-queue.md) describe the available path. Next, establish policy
and review freshness and crash recovery against fake GitHub. An opt-in automatic landing mode
requires a server-enforced precondition for the exact tested tree. Batches and bisection depend
on that proof.

### B7. Prove GitHub App setup, #2

The [GitHub App pilot](github-app-pilot.md) defines the permissions, credential handoff and
disposable-repository acceptance matrix.

**Contract.** An owner-initiated setup plan states the GitHub identity, target repositories,
minimal App permissions and local changes. A key moves directly to the chosen secret store and
never enters a repository, log or agent prompt. The setup verifies installation access and
supports key rotation with an observable rollback state.

**Sequence and proof.** Use a disposable organization and personal repository to test the
manifest conversion, installation, secret visibility, ruleset bypass and workflow-trigger claims
listed in the issue. Record which permissions each API call actually needs. Specify a local
interactive handoff and a no-terminal plan. Implement behind explicit owner action only after a
threat review. Fake API tests cover permission refusal, interrupted conversion, failed secret
write and rotation, while a manual pilot checks real GitHub behavior. This has no dependency on
the merge queue and should reuse B5 setup reporting when that contract is stable.

### B8. Local agent session chat

The [session chat specification](session-chat.md) defines MCP addressing, bounded durable
inboxes, requests, replies and acknowledgment. The first slice uses polling, stable recipient
IDs and optional reconnect capabilities.

Exercise two processes in linked worktrees, unrelated repository isolation, restart,
duplicate sends and quota refusal. Engine-specific delivery comes after the shared transport
works. It must report the observed delivery boundary for each host.

## Further product slices from the local audit plan

| Direction | Next end-to-end slice | Dependencies and verification |
|---|---|---|
| A1 Runner control | Explain one policy decision across a Claude Code guard call, a Codex guard call and a shell invocation using stable repository and session identities. | Build on `jst inventory`, shared guard decisions and the optional review adapters. Test two independent repositories with the same name, a linked worktree, missing hooks and explicit unknown identity. Keep policy distribution as a later opt-in service design. |
| A2 Delivery | Add remote effect reconciliation for one bounded GitHub operation, beginning with a release PR update. | Use the local delivery record and G1–G3 identities. A fake provider interrupts before and after every remote response, then replays without duplicate effects. Require observable remote identity or idempotency support before claiming exactly one effect. B6 uses this protocol later. |
| A3 Verification | Bind portable evidence to a named trust root and CI identity, then verify it from a second machine. | Extend the existing exported receipt envelope only after TST-2 gives all runners one verdict contract. Test signature/key rotation, changed source revision, policy and toolchain, offline verification and replay refusal. Preserve readable local receipts. |
| A4 Observability | Opt in to workspace aggregation of existing redacted timelines, with gate throughput, failure classes and available token counts. | Build on per-repository `jst timeline`. Test same-name repository isolation, retention, partial scans and redacted output. A monetary estimate needs a dated and independently verified price table, so token counts are the first cost measure. |
| A5 Distribution | Stage a profile or binary upgrade across two repositories, preview each generated diff, and report adapter compatibility. | Build on portable bundles, pin records and `jst doctor`. Test unknown bundle schema, conflicting local settings, interrupted staging and rollback. Persist the exact verified bundle digest for each repository. |

## Dependency order

1. Repair B1 and B2 independently. Prove B3's trust and artifact contract separately.
2. Complete TST-1 and TST-2, then implement B4 selection while Go JSON and JUnit XML adapters
   work in separate packages behind the common verdict and report transport.
3. Pilot B5 one machine item at a time. Use its reporting contract for B7 after the real GitHub
   App experiment settles the credential flow.
4. Expand A2 remote reconciliation and A3 portable verification. Use their proved identity and
   evidence contracts for B6's single-PR pilot, then batches.
5. Expand A1 policy explanation, A4 workspace reporting and A5 staged upgrades independently
   where their data formats do not overlap. Reconcile schema changes before parallel writes to
   adoption, receipts or telemetry records.
6. Ship local B8 chat alongside the independent queue and App pilots. Add engine delivery once
   each host can demonstrate addressed injection, reconnect and acknowledgment.


## Verified integration work

- [Protected signed CI evidence](../guide/tests.md#protected-workflow-pilot) was verified
  from a separate environment. Changing the downloaded bytes or expected run attempt failed.
- [Digest-pinned profiles](../guide/adopt.md) were upgraded and restored in two independent
  repositories. Both kept their local settings and refused a modified bundle.
- [Manual queue recovery](merge-queue-pilot.md) was exercised with GitHub provider faults
  and checked persisted attempts and observed outcomes after retry.

## Next integration work

1. Extend the [managed chat pilot](session-chat.md#managed-codex-pilot) to a shared test
   workspace and measure discovery, delivery and acknowledgment on live agent turns.
2. Extend the [GitHub Free queue pilot](merge-queue-pilot.md#github-free-manual-pilot)
   with provider-side exact-candidate enforcement before piloting automatic landing or batching.
3. Follow the acceptance steps in the [compaction brief](context-brief.md),
   [workspace board](workspace-board.md), and [reviewer pool](reviewer-pool.md)
   specifications before adding more hosts or managed scheduling.
