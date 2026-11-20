# App-managed merge queue pilot for issue #3

## Implemented local slice

The [local queue guide](../guide/merge-queue.md) covers `jst queue add`, `prepare`, `test`,
`status`, `handoff` and `remove`. This slice holds one full local or remote-tracking branch ref,
keeps a versioned, atomically replaced checkpoint under jackstay's project state, and uses a
single-writer file lock. Git writes an unreachable candidate commit, without moving any ref;
`test` runs the gate on its clean copy and requires an exportable counted receipt before
`handoff`. A moved base or head, or a changed configured base ref, invalidates that evidence.

The optional PR-bound handoff rechecks live head and base, the GitHub test-merge tree, required
checks and review approval. A project can require the PR path and an independent local review.
The outcome slice records a successful handoff, then accepts an observed landing commit and
compares its Git tree with the tested candidate. Local lanes require the target tip. PR lanes
also require GitHub to confirm the exact merged commit reachable from the target.
Batching and provider-side exact-tree enforcement remain separate from manual handoff.

The primary feature is a durable queue owned by jackstay, with one writer per repository and one tested candidate at a time. It works without GitHub's native merge queue. The first slice is a manual single-PR lane in a disposable repository. It proves candidate identity, crash recovery and the conditions for safe landing. Ordered batches, failure bisection and a service process follow after that slice. [Issue #3](https://github.com/zima-oss/jackstay/issues/3) · [Backlog brief](https://github.com/zima-oss/jackstay/issues/3)

## Product contract and plan availability

The local queue accepts eligible PRs, persists their order, computes a candidate from the current target and queued heads, runs `jst test gate` on that exact tree, and records a portable, counted receipt. A failed candidate blocks its lane and can be split only after each smaller candidate is built and tested independently. A changed base, head, review, check policy or merge method invalidates the attempt. The queue never treats a passing test on an earlier tree as evidence for a later one.

| Repository capability | Landing behavior |
|---|---|
| Protected branch with a required check that GitHub enforces on the exact tested candidate, strict freshness, no bypass, and a pilot-proven atomic merge decision | jackstay may request a protected direct PR merge and then reconcile the effect. |
| No enforceable exact-candidate condition, unknown rule state, or unproven provider semantics | jackstay offers an assist/manual handoff with the tested candidate, receipt and current PR state. It makes no autonomous merge request and reports the observed result after a human merges. |

Local scheduling, testing, dry-run and recovery are available on GitHub Free and Team. GitHub documents branch protection for public organization repositories on Free and for public and private repositories on Team. A private organization repository on Free can exercise queue planning and manual handoff, but cannot satisfy the protected direct-merge acceptance condition through branch protection. The pilot uses a private organization-owned repository with synthetic content and detects its actual capabilities before choosing a mode. It does not require Enterprise or GitHub's native queue. The native queue is available for organization-owned public repositories on Free and private repositories on Enterprise Cloud, but is outside this primary feature. [GitHub protected branches](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-protected-branches/about-protected-branches) · [GitHub native queue availability](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/configuring-pull-request-merges/managing-a-merge-queue)

## Current implementation boundary

`jst gh pr-land` binds its green verdict to the PR head, base and GitHub test-merge commit, then re-reads the PR before merging with an expected head SHA. It can observe a merge after an ambiguous response. Its [landing path](../../internal/gh/land.go) does not persist a candidate tree or queue checkpoint. The existing [delivery record](../../internal/delivery/delivery.go) provides a versioned, atomically replaced local checkpoint with phases and attempts. The queue should use that mechanism and hold Git object IDs and receipt references, never credentials or raw test output.

GitHub's synchronous merge API accepts an expected PR-head SHA. Its asynchronous API supports `direct_merge`, returns a request UUID for polling, and may report a request as accepted before branch protection is evaluated. Neither API exposes an expected base SHA in the merge request. The result record lasts 24 hours, so a resumed lane must also inspect the PR and target ref. The asynchronous `merge_queue` action is unrelated to the local queue and is not used here. [GitHub PR merge APIs](https://docs.github.com/en/rest/pulls/pulls?apiVersion=2026-03-10#merge-a-pull-request-asynchronously)

## Candidate identity and durable state

Each attempt records repository identity, target ref and base commit, ordered PR numbers and head commits, merge method, candidate commit and tree, review and required-check policy identities, gate receipt identity, effect request UUID if present, and observed landed commit and tree. Before testing, fetch and verify every recorded input. Build one candidate with Git's merge semantics and run the gate in an isolated checkout of that candidate. A missing, uncounted or unverifiable receipt blocks landing. Re-read base, heads, reviews, checks and policy immediately before handoff or request. Any change starts a new attempt and gate run.

Persist intent before the remote request. After a crash or ambiguous response, query the request UUID if known, then the PR's merged state and target ref. Never issue a second request while the first effect is unknown. An expired UUID or unknown landed tree remains open for operator reconciliation. Every outcome records the observed commit and tree.

## Atomic landing condition

The expected-head parameter prevents a changed PR head from merging. It does not bind the target branch. A base advance between the last local read and GitHub's merge decision can change the resulting tree. Comparing the landed tree with the tested tree after the merge detects a violation but cannot prevent it. An automated landing therefore requires GitHub to reject a merge whose candidate is different at the instant it applies the merge.

The candidate implementation is a branch-protection rule with strict required checks and no bypass, plus a required check tied to the tested candidate's GitHub test-merge commit and receipt. GitHub documents strict checks as requiring the PR branch to be current with the base. It also permits selecting a specific GitHub App as the required-check source. Those documented properties suggest a path, but do not by themselves prove that a particular tested tree is an atomic merge precondition. The private protected pilot must verify the full behavior, including a base change after final local validation and before the merge request. A direct merge is enabled only for a rule and merge method that pass that test. An admin or App bypass invalidates the proof. [GitHub required checks and strict freshness](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-protected-branches/about-protected-branches#require-status-checks-before-merging) · [GitHub merge API head parameter](https://docs.github.com/en/rest/pulls/pulls?apiVersion=2026-03-10#merge-a-pull-request)

Manual mode makes the same candidate and receipt visible to an operator. It checks for staleness
before handoff and persists that handoff. The operator supplies the observed landing commit for
reconciliation. Its tree comparison is saved under the same candidate attempt, including a
mismatch. An unavailable target or unconfirmed PR outcome remains open. The comparison detects
a changed tree after landing and does not prove the provider enforced an atomic exact-tree
condition. Recording an explicit human decision before the merge remains follow-up work.

## Existing evidence, with synthetic identities

A temporary Git probe merged one feature head into a base. Merge and squash commits made from the same computed candidate had equal tree object IDs. Advancing the base changed the candidate tree. This establishes the local Git invariant for a simple clean merge.

A prior disposable private-repository experiment used synthetic PRs and a strict required check. A detached candidate passed a counted gate and matched GitHub's test-merge tree. Advancing the base made the old candidate stale. GitHub rejected a merge without the new required check; it rejected a stale expected head separately. With a fresh gate, the candidate and landed trees matched. An asynchronous direct merge returned a pending request that could later be observed as merged, and an exported receipt verified from a second checkout. These observations cover examples, not the full race: the experiment did not prove an atomic exact-tree condition for every base advance after the final read. No repository or account identifier from that experiment is needed for this specification.

## GitHub Free manual pilot

The November 2026 pilot used a private organization test repository on GitHub Free and
isolated synthetic target and source branches. GitHub denied access to the repository
rulesets endpoint on that plan. Jackstay's local-only handoff was used for this run.

Advancing the live target invalidated the first gate result. After a fresh counted gate,
GitHub's recomputed test merge tree matched the candidate even though the PR's embedded
base SHA still reported the original commit. PR-backed handoff reads the live target ref
separately and verifies the test merge commit's tree and parents.

An operator squash-merged the test pull request. The squash commit differed from the
candidate commit, and its tree matched the candidate, the computed GitHub test-merge
tree and the tree read back from GitHub after landing. A fresh Jackstay invocation recorded
`landed-matched`. Repeating reconciliation returned the same recorded result. The pilot
left the default branch untouched and exercised manual handoff on a plan without private
branch protection.

## Local recovery fault pilot

An isolated Git fixture exercises the queue with a scripted GitHub provider. The provider fails
on the first PR read, the final PR reread, and the final target-ref reread in separate attempts.
Each failure blocks handoff without writing its marker. A later successful read records the
exact tested candidate; a new service instance loads that attempt from disk. After a manual
merge in the fixture, a provider confirmation failure leaves the outcome empty and reports the
read error. Repeating reconciliation after recovery records the observed commit and matching
tree. Subsequent retries preserve its timestamp and reject a different commit, even when the
provider is unavailable again. A replacement lane with the same candidate and gate identities
cannot inherit a handoff checked for another PR.

This is local fault injection and real Git object/ref testing. It does not exercise a live
GitHub outage or an automated merge request. Manual merge remains an external operator action,
so the queue never retries an unknown remote write effect.

## Disposable private organization pilot

The pilot uses synthetic commits and checks. Repository creation, rule changes and merge requests require an explicit operator handoff. The local plan and dry-run should be reviewable before those external effects.

1. Read the repository's plan capability, default branch, merge methods, actor permissions, branch rule, required check source and bypass settings. On a private Free organization repository, exercise planning and manual mode. On a private Team repository with branch protection, also exercise the protected direct-merge proof. Do not upgrade a plan merely to run the pilot.
2. Enqueue one unstacked PR, persist a candidate attempt, run `jst test gate` on its isolated checkout, and verify the exported counted receipt in a second checkout. Assert that dry-run changes neither local queue state nor remote state.
3. Change the base and head separately before handoff. Each change invalidates the attempt and requires a new candidate and gate. Change reviews, check policy and merge method in separate cases. Ensure the lane refuses stale or ambiguous evidence.
4. For protected direct merge, place an independent synthetic PR between the final local read and the merge request. The required check must be bound to the candidate tested before that advance. The provider must reject the old candidate at its merge decision. A successful merge of a different tree fails the automated-mode pilot, even when the mismatch is immediately detected. Repeat for each supported merge method.
5. Interrupt after persisting request intent, after an accepted asynchronous response, and before saving the final response. Resume from the record and reconcile UUID, PR state and target ref before any retry. Verify no duplicate merge effect. After the UUID expires, retain enough PR and ref identity to reconcile safely.
6. Land one stable protected candidate only after step 4 succeeds. Fetch the observed commit and compare its tree to the tested tree. Verify the PR is merged and the enforced check and review identities match the attempt. Separately exercise manual handoff and record the observed outcome without claiming automatic exact-tree enforcement.

The fake provider covers stale head, advanced base at the atomic decision, failed required check, rejected review, bypassed rule, conflicting writer lock, expired asynchronous result and unknown landed tree. The first implementation is complete when every case yields an explicit terminal or resumable state, the private pilot exercises manual mode, and a protected direct merge is enabled only where the exact-tree precondition is demonstrated. Batch ordering and bisection depend on this single-PR evidence.
