# Profiles: an organisation's rules for `jst adopt`

jackstay's core is general: it does not care which runner a project uses, which runners its workflows
run on, or how its organisation works (W-059). What an organisation wants of all its projects —
a runner, a gate scope, files that must or must not exist, its own workflows, a few lines in the
AGENTS block — is a **profile**: one JSON file, data and never code, kept wherever the
organisation keeps its shared files (not in jackstay). A project is checked against it with

```
jst adopt --plan --profile PATH
```

and `jst adopt --apply --profile PATH` (the same path, the first time) records it in the project's
`jackstay.json` (`adopt.profile`), so later runs and `jst doctor` use it without the flag. The design is [docs/design/adopt.md](../design/adopt.md), section 6.

The example: [example/profile.json](example/profile.json), with its own workflow template
[example/workflows/card-line.yml](example/workflows/card-line.yml). It uses every capability.

## Every capability

The file is read strictly: an unknown key makes the profile invalid, and `jst adopt` stops (exit
2) before planning anything. Paths in it are relative to the profile's own folder and may not
leave it.

### `jackstay_profile`, `name`, `about`

`jackstay_profile` is the format's version (1). `name` is printed by `jst adopt` and `jst doctor`.
`about` is one line for people.

### `files.required` and `files.forbidden`

```json
"files": {
  "required": [{"path": "justfile", "why": "the recipes the test scopes run"}],
  "forbidden": [{"path": "Makefile", "why": "one runner per project", "instead": "the same recipes in the justfile"}]
}
```

A required path (file or folder, relative to the project's root) that is missing is the conflict
`file:<path>:missing`; a forbidden one that exists is `file:<path>:present`, printed with `why`
and `instead`. `jst adopt` never creates or removes them: a missing file is the project's to
write, a forbidden one a migration the project's session does (its own card), or keeps with an
exception (`adopt.exceptions` in the project's `jackstay.json`, with the reason).

### `scopes.required` and `scopes.run_with`

```json
"scopes": {"required": ["gate"], "run_with": ["just"]}
```

`required`: `test` scopes every project declares (`scope:<name>:missing`). `run_with`: the
programs a scope's command may start with — `run[0]` of the scope, or of each step it names
(`scope:<name>:runner`). This is how an organisation says "our scopes run `just` recipes" without
jackstay itself caring which runner is used.

### `jackstay_json.required` and `jackstay_json.defaults`

```json
"jackstay_json": {
  "required": ["github.account", "cards"],
  "defaults": {"procs": {"default_minutes": 30, "max_minutes": 240}}
}
```

`required` names dotted keys a project's `jackstay.json` must have (`jackstay.json:<key>:missing`) — values
that only the project can know, like the GitHub account jackstay acts as. `defaults` are added by
`jst adopt` where the project has no value: a missing section whole, a missing key inside an
existing section; a value the project already has is never replaced. Each default must pass
`jst check`'s table of keys.

### `workflows`

```json
"workflows": {
  "card-line": {"file": "workflows/card-line.yml", "when": ["cards", "changes"]}
}
```

A workflow template of the organisation's. `file` is the template; `when` lists `jackstay.json`
sections of which any one enables it (absent: always). A name jackstay embeds
itself (`card-line`, `release`) replaces jackstay's template — this is how an organisation runs jackstay's
checks on its own runners or with its own tokens.

Every `uses:` must be pinned by a full 40-character commit hash with a `# vX.Y.Z` comment (or be
a local `./path`, or a `docker://` image by `sha256`); an unpinned one makes the profile invalid.

`jst adopt` writes the template to `.github/workflows/<name>.yml` with a first line naming it as
jackstay's and a hash of the rest. Edits by hand are found by the hash and never overwritten silently;
change the profile instead.

### `values`

```json
"values": {"runner_labels": "self-hosted"}
```

Names a template uses as `@@name@@`. jackstay defines `jackstay_version` (the release the workflow downloads:
the project's `adopt.pin`, set by the first apply and moved by `--pin`), `release_base` and
`jackstay_repository`. A `@@name@@` defined by neither is an invalid profile.

### `agents.rules` and `agents.leave_out`

```json
"agents": {
  "rules": ["`just gate` is this organisation's gate; `jst test gate` runs it."],
  "leave_out": ["owner"]
}
```

`rules` are lines added at the end of the AGENTS block `jst adopt` writes, under "This project's
organisation". `leave_out` drops items of the block the organisation does not use (the item IDs
are in the spec, section 8: `test`, `wait`, `bg`, `lock`, `branches`, `discard`, `guard`,
`github`, `review`, `cards`, `ids`, `changes`, `release`, `log`, `owner`, `doctor`).

## What a profile cannot do

Change what jackstay owns beyond its four places (`jackstay.json`, the AGENTS block, the guard hook, the
workflows it writes); run code; decide a migration. Anything that needs a judgement is a conflict
for the project's session, settled in the project.
