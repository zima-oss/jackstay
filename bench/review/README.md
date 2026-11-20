# Review wrapper measurement

What `jst review` adds over calling a reviewer directly, measured ([W-090](../../docs/decisions/W-090-review-of-0-6-0.md)).
`just review-bench` builds `jst`, makes a throwaway repository from `testdata/fixture` (a base and
three commits, each a range), and has every arm review every range, run after run:

| arm | what runs |
|---|---|
| `codex-jst` | `jst review BASE..HEAD --reviewer codex --fresh --json`: the pinned view, jackstay's prompt and access profile |
| `codex-direct` | `codex -a never exec -s read-only` in a checkout of the range's head, with a short prompt naming the range |
| `claude-jst` | `jst review BASE..HEAD --reviewer claude-code --fresh --json` |
| `claude-direct` | `claude -p` in the checkout, read tools and `git diff`/`show`/`log` only, the same short prompt |

Both arms of a reviewer use the same model and effort (by default the ones in the user's own
Codex and Claude Code configuration, `$CODEX_HOME` and `$CLAUDE_CONFIG_DIR` honoured; jackstay's
reviewers ignore that configuration, so without one model for both the arms could compare different
models, and the harness refuses to start without it). Every answer uses jackstay's verdict schema,
and a direct arm's answer counts only when its process succeeded and its verdict is `approve` or
`changes`, as the wrapped arm's. A run's deadline interrupts the program and waits for it, so no paid
review outlives its run.

**Scoring.** `testdata/defects.json` lists 10 seeded defects and the fixture's known real issues
that were not seeded (`extras`). A finding matches a defect of its range when it names the defect's
file or function and uses the defect's words, a word in its place or title counting three and one in
its detail one; the best match wins, an extra winning a tie. Recall is the seeded defects found per
run; precision is the findings that match a seeded defect or an extra, of all findings; "extras per
run" is how many real unseeded issues a run reports. The report lists every finding that matches
neither, for a person to judge; when one is real, add it to `extras` and score the saved runs again
with `go run ./bench/review -rescore results/<time>.jsonl` (no model call).

**Stages** (wrapped arms only): `prepare` is getting a source view slot and building the pinned
view (`round.json` `prepare_seconds`); `wait` is getting a reviewer slot and the start lock
(`run.json` `wait_seconds`); `model and tools` is the reviewer's own run. Cost is what Claude Code
reports (`total_cost_usd`); Codex reports tokens only.

```sh
just review-bench -runs 5                    # every arm, 5 runs; about an hour; paid
just review-bench -runs 1 -arms claude-jst   # one arm, a quick look
```

Results are written to `results/<time>.md` (the tables) and `.jsonl` (every run). Run it outside
an agent's sandbox: both reviewers need their logins.
