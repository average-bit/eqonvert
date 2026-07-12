# Contributing to eqonvert

Thanks for your interest! eqonvert is a reverse-engineered converter for EQOA
(PS2) assets. Contributions — bug reports, format findings, and code — are
welcome.

## Ground rules

- **No game assets.** Never commit `.esf`/`.csf`/`.bgm`/`.16`/`.pss`/`.iso`
  files, extracted output (`.glb`/`.flac`/`.vag`/`.png`), or ISOs. The
  `.gitignore` blocks these; keep it that way. This tool only *reads* files you
  already own.
- **Prove format claims.** Where a structure or offset is asserted, cite the
  evidence (a Ghidra decompile of the client, or cross-validation against real
  game data). See [docs/](docs/) for the style — especially
  [docs/ANIMATION.md](docs/ANIMATION.md), which documents the format's traps.

## Development

Requires Go 1.25+. ffmpeg + openmpt123 are optional (audio/video only).

```sh
go build -o eqonvert .   # build
go test ./...            # test
go vet ./...             # vet
gofmt -l .               # formatting (should print nothing)
```

CI runs build/vet/test on every push and PR.

## Pull requests

- Keep changes focused; one logical change per PR.
- Run `gofmt`, `go vet`, and `go test` before pushing.
- For parser changes, note how the behavior was validated (e.g. converted a
  known file and checked the output).
- Match the surrounding code's style and comment density.

## Reporting bugs

Open an issue with:
- what you ran (the exact `eqonvert …` command),
- what happened vs. what you expected,
- the build/game version (`eqonvert --version`) and your OS.

Please do **not** attach game assets — describe the file (type, size) instead.
