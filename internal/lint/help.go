package lint

// pullRequestHelp is `jst lint pr --help`.
const pullRequestHelp = `usage: jst lint pr (--event FILE | --pr N | --body-file F) [--rev REV] [--base B] [--head H] [--head-branch NAME]

Exactly one of --event, --pr, --body-file gives the description.
  --event FILE        a GitHub pull_request event file
  --pr N              that pull request, read from GitHub
  --body-file F       a description in a file
  --rev REV           judge that commit's tree instead of the working tree
  --base B            the base commit
  --head H            the head commit
  --head-branch NAME  the head branch

exit: 0 no problem; 1 problems, each named, or (--pr) the head does not merge with the base
without conflicts; 2 the request, jackstay.json, git or GitHub cannot be used; 5 the --event file
is not JSON, or not a pull_request event as GitHub documents it.

example: jst lint pr --body-file pr-body.md --base origin/main --head HEAD
`

// treeHelp is `jst lint tree --help`.
const treeHelp = `usage: jst lint tree [--rev REV]

  --rev REV   judge that commit's tree instead of the working tree

exit: 0 no problem; 1 problems, each named; 2 the request, jackstay.json or git cannot be used.

example: jst lint tree
`
