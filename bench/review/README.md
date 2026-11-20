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
and a direct arm's answer counts only when its process succeeded and the wrapped arm's own reader and
checks take it (`review.ReadAnswer`, `review.CheckAnswer`), with a verdict of `approve` or `changes`. Every arm runs in a process group of its own; at its deadline the whole
group is stopped (TERM, then KILL), so no paid review outlives its run.

**Scoring.** A person judges every finding: `results/<time>.labels.json` maps each one
(`arm/range/run: place — title`) to the seeded defect or known unseeded real issue (`extras`,
`testdata/defects.json`) it reports, or to `false`. Only these labels count. Recall is the seeded
defects found per run; precision is the findings judged real, of those judged; "extras per run" is
how many real unseeded issues a run reports. A finding without a label is *unjudged*: it counts for
nothing, the report lists it and says the metrics are partial.

A matcher helps the person: it proposes, for a finding whose place names a defect's file or
function and whose place or title uses one of the defect's mechanism words, that defect (an extra
wins a tie). After a run, from the repository's root and with no model call:

```sh
go run ./bench/review -rescore bench/review/results/<time>.jsonl -propose   # adds the proposals
# read every label in results/<time>.labels.json; fix it, or set "false"
go run ./bench/review -rescore bench/review/results/<time>.jsonl            # the report from the labels
```

Each run keeps the model and effort it asked for; for runs saved before that, pass them with the
`-codex-model`, `-codex-effort`, `-claude-model` and `-claude-effort` flags, else the report says
"unknown".

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
