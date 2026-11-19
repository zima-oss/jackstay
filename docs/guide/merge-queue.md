# Local merge candidate queue

The first queue lane holds one branch head for manual landing. It runs locally and works without
GitHub Enterprise or GitHub's native merge queue. It does not open, approve or merge a pull request.

Enable the queue in `jackstay.json` with a full ref for the target branch:

```json
"queue": {"enabled": true, "base_ref": "refs/heads/main"}
```

`base_ref` may name a local branch or a fetched remote-tracking branch such as
`refs/remotes/origin/main`. Fetch it yourself before preparing a candidate; jackstay does not
fetch or change a ref. A head must also be a full local or remote-tracking branch ref.

```sh
jst queue add --head refs/heads/my-change
jst queue prepare
jst queue test
jst queue status
jst queue handoff
jst queue reconcile --commit <observed-target-commit>
jst queue remove
```

`add` pins the base and head commits in a local, persistent record shared by linked worktrees.
Only one head can be queued in this slice. `prepare` uses Git's merge result to write an
unreachable candidate commit and records its commit and tree IDs. It refuses a conflict without
changing a branch or the checkout. If the base or head moves before handoff, `status` shows `stale`
and `prepare` starts a new attempt with its prior evidence cleared. A recorded handoff holds
the tested attempt for reconciliation.

`test` runs the configured `gate` on a clean copy of that exact candidate with
`jst test gate --commit`. It accepts the result only when the gate passes, the run log names that
candidate, and at least one counted step has an exportable portable receipt. Configure
`receipts.inputs` to cover the files the gate tests; a gate can pass without an exportable receipt,
but the queue then refuses handoff. `handoff` exports the saved receipts again and checks their
digests and candidate IDs, as well as current base and head refs. It prints the tested candidate,
tree, gate run and receipt identities for a person to inspect before merging. A successful
handoff also saves that exact tested attempt so a later process can reconcile the outcome.

Manual landing still has a race: the base or head can move after `handoff`. Recheck both refs at
the merge decision. After the merge, update or fetch the configured target ref and pass its full
observed commit ID to `reconcile`. In a local-only lane, that commit must be the current target
tip. In a PR lane, GitHub must report that the pinned PR merged at that exact commit, which may
be an ancestor of the target tip if another change landed later. The command records the commit
and its Git tree beside the tested tree. A matching tree exits 0. The command records a
mismatch and exits 1. An unreadable or unconfirmed outcome leaves the lane open for another observation.
`status` shows the saved outcome, including after restart. Repeating `reconcile` with the same
commit returns the saved result, while a different commit cannot replace it. `remove` empties
the lane and its outcome record after inspection. Keep the lane for audit while a mismatch or
unconfirmed merge is being investigated. Record the IDs printed by `status` and the receipt
exports in your normal audit system before removing the lane. No automated merge decision is
implied.

The [pilot plan](../roadmap/merge-queue-pilot.md) tracks landing verification, recovery after
ambiguous effects and the conditions for adding batching or automatic landing.

## Verify a pull request before handoff

Use `refs/heads/<branch>` for `queue.base_ref` in PR mode, then enqueue the PR number
together with its head:

```sh
jst queue add --head refs/heads/my-change --pr 17
```

Set `queue.require_pull_request: true` when project policy requires a PR for every handoff.
`queue.review_task` optionally requires a clear local review of the pinned base-to-head range.
Provider PR mode always reads GitHub policy, even when local-only handoff is also allowed.

Handoff reads the live target branch separately from the PR and checks the head identity.
It matches the GitHub test merge commit's parents and tree to the tested inputs, and verifies required checks on that merge commit. It requires an effective approved
review decision and a current-head approval from someone other than the author. A missing
check, unavailable policy or unknown review decision blocks PR handoff. The queue reads the
PR and check policy again before handing off. The final merge still belongs to the operator.
Once GitHub reports the merge, fetch the target ref before `reconcile` so its commit is locally
readable and reachable from the target.
