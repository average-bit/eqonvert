# Git hooks

Versioned hooks for this repo. They are **not** active until you point git at
this directory (once per clone):

```sh
git config core.hooksPath .githooks
```

## `pre-commit`
Blocks a commit whose staged changes **add** a disallowed term (case-insensitive:
`everquest`, `sony online entertainment`). Only newly-added lines are checked, so
unrelated edits aren't affected. Extend the `PATTERN` in the script to add terms.
Intentional bypass (rare): `git commit --no-verify`.
