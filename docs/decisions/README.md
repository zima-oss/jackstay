# Decisions

A decision record here states one decision about how jackstay is built or works, why it was made,
and what follows from it; every record listed is in force, and code and specifications cite records
by their W number. The numbers have gaps because some records are kept private: they concern
matters outside jackstay or were superseded by later records. To propose a decision, open an issue
first and discuss it there; once it is agreed, a maintainer gives the next W number and the record
is added in a pull request.

| Id | Decision |
|---|---|
| [W-004](W-004-stage-0-and-ground-rules.md) | One machine; sequential numbers from one counter; a card closed in the pull request that carries its code, found by the `Card:` line |
| [W-005](W-005-lock-off-by-default.md) | The machine-wide test lock is off by default; the evidence threshold for proposing it |
| [W-006](W-006-tidy-and-github-tools.md) | Finished branches and worktrees are tidied with `jst tidy`; the `jst gh` helpers and `jst owner` |
| [W-007](W-007-tools-from-the-usage-study.md) | The guard catches zsh globs; `jst wait`; the `jst test` summary and `jst test last`; `jst sync`, `jst start` |
| [W-008](W-008-state-in-dot-jackstay.md) | State lives in `~/.jackstay`, one folder per project and `.all` for what is shared; the sandbox must allow writing it |
| [W-009](W-009-stage-1-scope-history-and-remote.md) | The `Card:` line and its check, the number counter, one file per record and change entry, the process registry; a snapshot before a history collapse |
| [W-012](W-012-test-scopes-vocabulary.md) | One vocabulary of test scopes, run as `jst test <scope>`; selection stays in each project |
| [W-013](W-013-quality-testing-declarative.md) | No time-sensitive tests; everything added is declarative and general |
| [W-017](W-017-review-fixes-and-threat-model.md) | The guard is against accidents: cheap deliberate bypasses closed, the rest known limits |
| [W-019](W-019-repeated-findings-mean-redesign.md) | A place that fails review two rounds running is redesigned; `pr-land` never touches local branches; `jst sync` merges only |
| [W-020](W-020-version-tags.md) | One version, one commit; annotated `vX.Y.Z` tags, never moved |
| [W-026](W-026-release-scope-now-in-parallel.md) | The release features' scope: change types, audience, version rules, app versions, prepare, the documented-change check |
| [W-029](W-029-queue-self-merge-configurable-go-last.md) | Who may merge the release pull request is a project setting (`release.merge`) |
| [W-032](W-032-jst-release-belongs-to-release-features.md) | `jst release …` belongs to the release features; a hold is undone by `unhold` |
| [W-034](W-034-release-pr-is-a-request-app-commits.md) | The release pull request carries a request; the writing GitHub App commits, tags and publishes the release |
| [W-035](W-035-justfile-not-makefile.md) | jackstay's developer recipes live in a justfile, never a Makefile |
| [W-036](W-036-go-native-not-python-mimicry.md) | Go written natively from the spec; RE2 patterns; logs as a field schema; a shell parser, an unparsable command refused |
| [W-037](W-037-current-go-and-dependencies.md) | Latest stable Go and dependencies, checked at their source and recorded; the Go practices followed |
| [W-039](W-039-unparsable-command-refused.md) | The guard refuses a command neither the bash nor the zsh parser reads |
| [W-040](W-040-python-frozen-tests-by-change-readable-names.md) | Tests by what changed; a finished step pushed after one run; readable names in jackstay's code |
| [W-042](W-042-recoverable-trash.md) | `jst trash`: a recoverable delete inside the sandbox, named by the guard's advice |
| [W-043](W-043-guard-go-design-approved-trash-seven-days.md) | `jst trash` keeps entries 7 days by default |
| [W-047](W-047-release-redesign-fixed-settings-no-one-off-imports.md) | Release simplified: no one-off imports; the settings file changes alone; prepare only prints; a GitHub release created once |
| [W-048](W-048-review-findings-triaged-by-realism.md) | Review findings rated by likelihood and consequence; rare ones go to a later-findings card (amended by W-094) |
| [W-050](W-050-go-part3-design-github-account-all-steps-here.md) | GitHub through go-github with a project's own `github.account` |
| [W-051](W-051-jst-review-codex-rounds.md) | `jst review`: Codex review rounds; its defaults, effort medium |
| [W-052](W-052-guard-dead-end-and-clean-copy-gate.md) | `jst discard` ends the guard's keep-a-copy dead end; the gate runs on a clean copy it removes |
| [W-053](W-053-guard-rules-configured-per-project.md) | The guard's rules configured per project (refuse/warn/off, path exceptions); `zsh-glob` warns by default |
| [W-055](W-055-every-guard-rule-configurable-now.md) | Every guard rule takes a level; one check per rule; the rows that stay fixed |
| [W-056](W-056-observability-otel-traces-local.md) | Monitoring: OpenTelemetry traces as a local OTLP-JSON telemetry journal, GenAI conventions behind an adapter; `jst log`, `jst stats` |
| [W-057](W-057-observability-design-approved-masked.md) | Command text recorded `masked` by default, `skeleton` a setting |
| [W-058](W-058-projects-adopt-through-jst-adopt.md) | Projects adopt through an idempotent `jst adopt` and one short prompt; existing workflows classified |
| [W-059](W-059-runner-agnostic-core-and-profiles.md) | The core is runner-agnostic; an organisation's rules are a profile, data outside the tool; an example profile |
| [W-060](W-060-jst-adopt-profiles-as-data.md) | `jst adopt`: profiles as data, conflicts settled by migration, the AGENTS block as code, drift found by `jst doctor` |
| [W-066](W-066-jackstay-zima-oss-apache.md) | The name jackstay: command `jst`, alias `jackstay`, `jackstay.json`, `~/.jackstay`, `JACKSTAY_*`; Apache-2.0 |
| [W-069](W-069-jackstay-supports-linux.md) | Linux is supported: the process table from /proc, the gate green on ubuntu-latest |
| [W-070](W-070-sign-jst-at-install.md) | `jst install` signs the binary with a machine-configured identity and the identifier `jst`; `jst doctor` checks it |
| [W-072](W-072-public-contact-identity-records.md) | Contact through GitHub only; public decision records keep their W numbers |
| [W-073](W-073-public-repository-shape.md) | The public tree: decisions in force as short records, backlog in GitHub Issues, `docs/guide` and `docs/design` |
| [W-075](W-075-release-through-public-ci.md) | jackstay is released by the public repository's CI with GoReleaser (sign and notarise, keys in a protected `release` environment); no release harness in the code |
| [W-076](W-076-quick-install-and-docs-site.md) | A quick install script and a documentation site on GitHub Pages built from the same Markdown; no unfilled placeholders |
| [W-077](W-077-docs-site-for-users-only.md) | The documentation site carries only what a user needs; design documents and decision records stay in the repository (amended by W-093) |
| [W-078](W-078-setup-after-install.md) | After an install, `install.sh` prints the remaining steps and every `jst doctor` problem names its fix |
| [W-083](W-083-no-macos-job-in-ci.md) | CI runs the gate on Linux only; the macOS-only code is checked by `just gate` on a Mac before an install and a release |
| [W-085](W-085-adopt-lands-settings-and-files-apart.md) | `jst adopt --settings-only`: a change of `jackstay.json` and other files lands in two pull requests, settings first (rule D0) |
| [W-087](W-087-review-pinned-source.md) | Reviewers navigate pinned Git snapshots and read patches on demand |
| [W-090](W-090-compatibility-of-0-6-0.md) | 0.6.0 compatibility: newer keys need `"jackstay_schema": 3` and an older pin is never handed one; internal limits are constants; Codex is the default reviewer again |
| [W-090](W-090-agent-chat.md) | 0.6.0 chat: the agent chat follows Claude Code's messaging model and extends it: one store per account, repositories as channels, the engine's session ID, delivery over Claude Code's socket with hooks as the fallback |
| [W-090](W-090-landing.md) | 0.6.0 landing: one `jst land` behind hosting adapters; two autonomy modes chosen at adopt and setup; `jst gh` and the queue removed |
| [W-092](W-092-thin-land-and-where-tests-run.md) | `jst land` stays thin: push, open, required checks, merge; where tests run is each project's declared split between local and CI |
| [W-090](W-090-one-status-command.md) | 0.6.0 overview: one `jst status` replaces `jst timeline`, `jst inventory` and `jst workspace board`; the local merge queue is no longer shown |
| [W-093](W-093-docs-for-people-and-agents.md) | The documentation: a checked style guide; flags, exit codes and keys in one home, references generated from it; the site by task, with `llms.txt` for agents and the contributors' pages (amends W-077) |
| [W-090](W-090-review-of-0-6-0.md) | 0.6.0 review: the pool folded into `jst review --background` with chat events, only missing answers paid again after a crash, one access profile for Codex and Claude Code, the wrapper measured |
| [W-094](W-094-unusual-severe-findings-need-an-ordinary-path.md) | A review finding rated unusual and severe blocks only when it names the ordinary-work path to its setup; otherwise it is rare (amends W-048) |
| [W-095](W-095-the-socket-is-a-doorbell.md) | Claude Code's messaging socket is only a doorbell; a chat message is delivered when the recipient reads it from its inbox |
| [W-096](W-096-clear-limits-over-more-machinery.md) | Every mechanism has simple behaviour and written limits; a place that draws corner cases is narrowed or replaced by an existing solution; CI does not run the same test twice on one commit, within written limits (concurrent jobs, cache eviction); see [the task-graph record](W-096-task-graph-and-input-cache.md) |
| [W-096](W-096-task-graph-and-input-cache.md) | Test scopes run as a task graph, each once after its `deps`, and are cached by a hash of their inputs; CI runs each scope at most once per system job |
