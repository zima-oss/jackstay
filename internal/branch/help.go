package branch

// syncHelp is `jst sync --help`.
const syncHelp = syncUsage + `

Brings this branch (of DIR's checkout, default the working folder) up to date with the default
branch: a fast-forward or a merge, never a rebase.
  --no-fetch   use the remote-tracking branch as it is; do not fetch first
  --push       push the branch after the merge
  --rebase     refused: jst sync merges only

exit: 0 done or nothing to do; 1 a conflict (the files are named, the merge is left to resolve or
abort), or git refused; 2 the request (uncommitted work, a detached HEAD, a merge under way).

example: jst sync --push
`

// startHelp is `jst start --help`.
const startHelp = startUsage + `

A new branch NAME and its worktree, on the default branch as fetched.
  --from REF    start from REF instead of the default branch
  --path DIR    the worktree's folder (default .claude/worktrees/NAME when git ignores it there,
                else a folder beside the checkout, CHECKOUT-worktrees/NAME)
  --no-fetch    do not fetch the remote first

exit: 0 made; 1 git refused; 2 the request (a taken name or folder, a REF that is not a commit).

example: jst start fix-parser
`
