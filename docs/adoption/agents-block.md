# The jackstay block for a project's AGENTS.md

The block is **printed by jackstay, not copied by hand** ([W-060](../decisions/W-060-jst-adopt-profiles-as-data.md)). `jst adopt` writes it
into the project's `AGENTS.md` between two markers, from the project's `jackstay.json` (only the tools
the project enables), the commands this jackstay has, and the project's profile (its `agents.rules` and
`agents.leave_out`):

```
<!-- jackstay-adopt: begin agents-block sha256:… — written by `jst adopt` from jackstay.json and the profile; change those, not this text -->
## jst: the tools for working on this project
…
<!-- jackstay-adopt: end agents-block -->
```

- To see the block a project gets: `jst adopt --block` in it.
- Its source: the items of `internal/adopt/block` and their golden files
  (`internal/adopt/block/testdata/`); its approved shape: `docs/design/adopt.md`, section 8.
- **When a tool is added or its use changes**, the item and its golden file change in the same
  commit. Once the new jackstay is installed, `jst doctor` in a project says the project is not up to
  date with it; `jst adopt --apply` there brings the block up to date (the checklist:
  `docs/design/adopt.md`, section 11).
- A project's own lines live outside the markers; an edit inside them is found by the hash and
  never overwritten silently (the conflict `agents:edited`).

A block fitted by hand, without the markers, is not read by `jst adopt`: remove it, and
`jst adopt --apply` appends the generated one.
