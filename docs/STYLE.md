# How jackstay's documentation is written

This page is for anyone who writes a page, a help text or an item of the AGENTS block. The Go tests
of `internal/docs` check the rules marked **checked**; a reviewer checks the rest.

## Who reads what

| Reader | Reads | Lives in |
|---|---|---|
| A person using jackstay | the site: how to do a task | `README.md`, `docs/guide/` |
| An agent in an adopted project | what to run and what to do with the result | the AGENTS block (`internal/adopt/block`), `jst <command> --help` |
| Someone changing jackstay | why it works as it does | `docs/design/`, `docs/decisions/` |
| A contributor | how a change gets in | `CONTRIBUTING.md`, this page |

Write for the reader of the file you are in. A guide page does not explain the design; it links to
it.

## The rules

1. **Outcome first.** The first sentence of a page or section says what the reader gets or does.
   Background comes after, if at all.

2. **Plain English.** Short sentences, common words, active voice. Address the reader as "you" in
   the guide. Call a thing by the name the CLI prints: a scope, a receipt, the gate.

3. **No filler.** Cut words that carry nothing: `simply`, `just` (the word, not the tool),
   `basically`, `obviously`, `of course`, `actually`, `really`, `very`, `easily`, `in order to`,
   `note that`, `it is important to`, `seamless`, `powerful`, `robust`, `leverage`. **Checked** in
   the guide, `README.md` and `CONTRIBUTING.md`.

4. **One meaning, one home.** Each fact is written once, in the place below, and linked from
   everywhere else:

   | Fact | Its home |
   |---|---|
   | a command's flags and exit codes | its `--help` (and the generated command reference) |
   | a `jackstay.json` key, its type and default | `internal/config` (and the generated configuration reference) |
   | how to do a task | one guide page |
   | why it works this way | `docs/design/` or a decision record |
   | what an agent must do | the AGENTS block |

   When you find the same paragraph in two places, keep the one in its home and replace the other
   with a link.

5. **Checkable statements.** Say what a reader can test: a number, a default, an exit code, a path,
   the line jackstay prints. Not "fast" but "under 50 ms"; not "usually" but the condition. A claim
   no one can check is cut.

6. **Every command example runs.** A code block tagged `sh` runs as written on an adopted project:
   no `<placeholder>`, no `NAME` in capitals, no `…`. A synopsis with placeholders, and what a
   command prints, go in a block tagged `text`. Inline code in a sentence may name a placeholder
   (`jst trash PATH`). **Checked**: every `jst` command in the guide, `README.md`,
   `CONTRIBUTING.md`, `AGENTS.md` and the AGENTS block's golden files names a command `jst help`
   lists and only flags its `--help` shows (`internal/cli/testdata/help.txt`), or, in a table row
   only, a command `internal/cli` lists as removed, named whole beside the replacement its advice
   names; an
   `sh` block holds no placeholder.

7. **Agent-facing text says only what changes behaviour.** An agent reads every word, every
   session.
   - A command's `--help`: one line of purpose, the usage, the flags, one example, the exit codes.
     No history, no rationale.
   - An item of the AGENTS block: an imperative saying what to run, when, and how to read the
     result. It exists only when it changes what an agent does; the details are one
     `jst <command> --help` away.

8. **Public text.** English. No private names, account names, machine paths, internal commit
   hashes or attribution lines. Examples use made-up names: `my-service`, `example.invalid`,
   `/home/you`. **Checked**: no commit hash written as code in the guide, the roadmap or the
   README.

## Form

- Headings in sentence case. A page has one `#` heading.
- Commands, keys, paths and flags in code spans.
- Options and their meanings in a table or a list, not in a paragraph.
- Links are relative to the file (`tests.md#receipts`); the site turns them into its own. A link to
  `docs/design` or `docs/decisions` leads to GitHub.
- Lines wrap at about 100 characters.

## Before you push

```sh
go test -count=1 ./internal/docs
just docs
```

The Go tests run the checks above and the repository's link checks (the gate runs them in x-go);
`just docs` builds the site and checks its links.
