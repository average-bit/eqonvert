# IMPROVEMENTS

A prioritized plan to align eqonvert with the [12-Factor CLI](https://medium.com/@jdxcode/12-factor-cli-apps-dd3c227a0e46)
principles and DRY code, tracked as a checklist. Grounded in the current code.

Status legend: `[ ]` todo · `[~]` in progress · `[x]` done

## P0 — correctness for scripting / CI (do before publicizing)

- [x] **P0.1 Exit codes** (12F: *handle things going wrong*). Done: every
  command is now `RunE` and returns errors; `rootCmd.SilenceUsage` set so a
  runtime error prints only `Error: …` (via cobra, to stderr) and Execute exits
  non-zero. Verified: `convert /bad/path` → exit 1, error on stderr, stdout
  empty. Batch loops log per-file failures and return a summary error.
- [x] **P0.2 Streams** (12F: *mind the streams*). Done: added `cmd/output.go`
  (`logf`/`vlogf` → stderr, honoring `--quiet`/`--verbose`). All status,
  progress, and error messages routed to stderr across every command; stdout
  reserved for data (`inspect` object tree). No `Error`-to-stdout prints remain.

## P1 — 12-Factor gaps

- [x] **P1.1 Wire `--verbose` / add `--quiet`** (12F: *prefer flags*). Done:
  persistent `--verbose` and `--quiet`/`-q` flags on root, backing the global
  `verbose`/`quiet` that `logf`/`vlogf` honor. The `verbose` param is now
  driven by the flag everywhere (dir/ISO/single) instead of hardcoded literals.
- [x] **P1.2 TTY / color degradation** (12F: *be fancy, gracefully*). Done:
  `progress.go` computes `stderrTTY` (via `golang.org/x/term`); the progress
  bar and spinner set `OptionSetVisibility(stderrTTY && !quiet)`, so piped /
  redirected / CI output gets no carriage-return spam — only the stderr summary
  lines. (No ANSI color is emitted, so `NO_COLOR` is already satisfied.)
  Verified: piped run → 0 bar chars, stdout empty, exit 0.
- [x] **P1.3 Narrow the dependency gate.** Done: removed the hard `PreRunE`
  gate. The tools are optional enhancers (renamed `requiredTools` →
  `externalTools`); `convert` now calls a non-fatal `warnMissingTools` that
  notes any missing tool and what it limits, then proceeds. Use sites already
  degrade (ffmpeg → pure-Go FLAC fallback; PSS/XM skipped with a note). Help
  text + README updated to say the tools are optional.

## DRY

- [x] **D.1 Single asset dispatcher.** Done: extracted `convertAssetData(data,
  name, mediaOut, esfOut, …)` — one ext→handler switch now shared by dir, ISO,
  and single-file modes (each computes its own out-dirs). All three call sites
  collapsed to it. **Validated byte-identical**: converting a mixed ESF+BGM dir
  before/after produced the same 156 files, identical checksums (INDEX.md too).
- [x] **D.2 One `--output` flag.** Done: `convertOutputDir` +
  `convertZoneOutputDir` collapsed to a single shared `outputDir` global that
  both commands' `-o` flags bind to (kept as per-command flags so `-o` doesn't
  clutter the positional-arg commands).
- [x] **D.3 Fold repeated `os.ReadFile`+error+`Error`-print** patterns. Done as
  a side effect of P0.1 (RunE) + D.1: the per-mode read/dispatch/print blocks
  collapsed into the dispatcher, and the now-dead `convertFile`, `convert16File`,
  and `convertBGMFile` wrappers were deleted.

## P2 — polish for a public repo

- [x] **P2.1 `--json`** machine-readable output. Done: `inspect --json` emits
  the object tree as JSON to stdout (shared parse path; text output unchanged).
  Verified valid JSON, stderr clean. (`index` already writes a Markdown catalog;
  a JSON variant was judged unnecessary.)
- [x] **P2.2 `CONTRIBUTING.md`** + a basic issue template (12F: *encourage
  contributions*). Done: added `CONTRIBUTING.md` (no-assets rule, dev/test
  commands, PR + bug-report guidance) and `.github/ISSUE_TEMPLATE/bug_report.md`.
- [x] **P2.3 XDG config** — intentionally skipped (decision): the tool is
  stateless (input path + `-o`), so there's no config/cache to store.

## Already good

Great `--help` (simplified, examples, requirements) · real `--version` with
detected dependency versions · cobra subcommands · fast Go startup · markdown
table output (`INDEX.md`) · input-as-arg + output-as-`-o` · clear hard-fail when
tools are missing.
