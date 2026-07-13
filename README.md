# Acknowledgements

- [joukop/ESF-file-format](https://github.com/joukop) — independent ESF
  research that cross-checked several structures (PrimBuffer VGroups).
- [EQOAGameServer / ReturnHome](https://github.com/EQOAProject) — the
  community server emulator; its captured retail data supplies the model
  names and the AnimationState ID table.
- The PS2 homebrew/RE community for Ghidra Emotion Engine tooling.
- Shoutout to Ali for aiding me with Java extraction modifications forever ago.

- This is alpha testing and has bugs - it is not perfect yet

# eqonvert — EQOA asset extractor

`eqonvert` converts game assets from **EQOA**
(PlayStation 2, 2003–2012) into open formats. Character models export as glTF
binary (`.glb`) with skeletons, skinning, textures, and **fully working, named
animations**; zones export as assembled per-zone GLBs; audio exports from the
PS2 ADPCM stream format.

Everything in this tool is the product of reverse engineering: the file
formats were recovered by decompiling the PS2 client executable (Ghidra with
the Emotion Engine loader) and cross-validating every structure against real
game data. Where a design decision looks unusual, there is a documented reason
from the engine itself — see [docs/](docs/) for the full evidence trail.

## Install

**Download a prebuilt binary** (no Go toolchain needed) from the
[Releases page](https://github.com/average-bit/eqonvert/releases) — builds are
published for Linux, macOS, and Windows (amd64 + arm64):

```sh
# example: macOS (Apple Silicon)
tar -xzf eqonvert-darwin-arm64.tar.gz
./eqonvert-darwin-arm64 --version
```

**Or build from source** (Go 1.25+):

```sh
go build -o eqonvert .
```

### External dependencies (optional)

Models convert with no external tools. Audio and video output shells out to two
programs when they're on your `PATH`; without them, model/texture extraction
still works, audio falls back to a built-in FLAC encoder, and video is skipped.
Run `eqonvert --version` to see what's detected.

| Tool | Version used | Purpose | Install |
|---|---|---|---|
| ffmpeg | 8.0.1 | audio (FLAC) + video (FMV) encoding | `brew install ffmpeg` |
| openmpt123 / libopenmpt | 0.8.7 | rendering `.xm` tracker music to FLAC | `brew install libopenmpt` |

Other recent versions should work too — these are what the project is built and
tested against.

## Quick start

```sh
# Convert a single asset file
./eqonvert convert CHAR.ESF -o out/

# Convert an extracted game directory (walks for .csf/.esf files)
./eqonvert convert /path/to/DATA -o out/

# Convert straight from a disc image
./eqonvert convert eqoa.iso -o out/
```

Output files are self-contained GLBs viewable in any glTF viewer (Blender,
three.js, <https://gltf-viewer.donmccurdy.com/>, Windows 3D Viewer, …).

## What you get

- **Named model files** — `CHAR_black_widow_0x13ECB2D8.glb`,
  `CHAR_DarkElf_Female_0x8C9B4B39.glb`. Names are recovered by joining the
  asset IDs against a community server database; the hex dictID is kept for
  traceability. See [docs/MODEL_NAMES.md](docs/MODEL_NAMES.md).
- **Named animations** — `0x03_Running_lower`, `0x0E_Die_upper`,
  `0x00_Idle_lower`. EQOA splits every animation into an upper-body half and a
  lower-body half that the engine layers at runtime (running while attacking =
  lower Running + upper Punch). Both halves are exported; play them together
  for the complete motion. See [docs/ANIMATION.md](docs/ANIMATION.md).
- **Correct skinning** — skeleton, inverse bind matrices, joint weights, and
  per-frame rotation *and* translation tracks that reproduce the PS2 engine's
  pose math exactly.
- **Embedded textures** — PS2 swizzled/paletted surfaces are decoded to PNG
  and referenced by the glTF materials.
- **Zone geometry** — terrain plus placed static objects (trees, rocks,
  buildings) grouped per zone with world-space placement.
- **Named zones and a navigable index** — Tunaria zone GLBs are written with
  their in-game area names (`zone_87_Qeynos.glb`, `zone_65_Blackburrow.glb`,
  …) from embedded community map data, and directory/ISO conversions finish
  by generating an `INDEX.md` catalog of every zone, named character, texture
  and audio file (also available standalone: `eqonvert index <dir>`).

Everything above ships in the single `eqonvert` binary — no external tools or
data files needed at runtime.

## Commands

### stick to convert unless you want bugs and mistakes for now.

| Command | Purpose |
|---|---|
| `convert <path>` | Assets → GLB. Accepts `.esf`, `.csf`, a directory, or an `.iso` |
| `convert-zone <path>` | Assemble per-zone GLBs + a placement manifest |
| `decompress <path>` | CSF → raw ESF (zlib container removal) |
| `inspect <path>` | Print the object tree of a CSF/ESF file |
| `extract <path>` | Extract individual resources (currently surfaces/textures) |
| `dump-body <path>` | Dump raw bodies of a given object type (for format research) |
| `scene <path>` | Extract ZoneActor placements to JSON |

## Project layout

```
cmd/          CLI commands (cobra). model_names.json is embedded here.
pkg/eqoa/     File-format parsers: CSF container, ESF object model, meshes,
              skeleton, animation, materials, surfaces, audio, zones.
pkg/gltf/     glTF document builder and the asset→glTF export logic.
docs/         Format documentation and the reverse-engineering evidence.
```

## Documentation

- [docs/FORMATS.md](docs/FORMATS.md) — CSF/ESF container and every object
  format the tool parses: meshes, vertex layouts, materials, textures, audio.
- [docs/ANIMATION.md](docs/ANIMATION.md) — the animation system end-to-end:
  world-space skeletons, the bone-map object, channel-major keyframes,
  upper/lower body layering, and how each finding was proven from the
  decompiled engine. **Read this before touching the animation code** — the
  format contains three traps that produce plausible-looking-but-wrong output.
- [docs/MODEL_NAMES.md](docs/MODEL_NAMES.md) — how asset IDs were linked to
  human-readable names via the server database.

## Format support status

| Asset | Status |
|---|---|
| Static meshes (0x1200) | ✅ |
| Skinned meshes (0x1210) | ✅ |
| Skeletons (0x2400) | ✅ world-space→local conversion |
| Animations (0x2600 + 0x5000) | ✅ rotation + translation, named |
| Textures (0x1000) | ✅ all surfaces → PNG, incl. standalone UI sheets, item icons, face textures |
| Materials (0x1100) | ✅ base color + texture layers |
| Zones (0x3000 family) | ✅ terrain + placements |
| Sound effects (0xB000 ADPCM) | ✅ FLAC (decoded) + `.vag` (original ADPCM, source-of-truth) |
| Music (0xB030 XM modules) | ✅ back-converted to playable `.xm` during convert |
| Loading/error screens (`.16` files) | ✅ 640×448 PSMCT16 → PNG |
| Item models + icons | ✅ named from the item database (`ITEM_Adorned_Crossbow_0x….glb`, `ITEMICON_…png`); 0x2D00 PointSprites are 8-byte glow-marker refs, not geometry |
| Streamed music + voice-overs (`.BGM`) | ✅ FLAC (decoded, 44100 Hz) + `.vag` (original ADPCM, preserves loop flags/structure) |
| Particles / spell effects (0xC000/0xC200) | ✅ per-effect GLBs (`effects/…_effect_0x….glb`): visible sprite cards + full parameters in glTF `extras`; plus the `_particlefx.json` graph (spell→particle→texture resolves). Attach an effect GLB to any character's `Joint_N` node to position it — glTF has no standard particle sim, so engines drive emission from the embedded parameters (the industry-standard data+textures shape) |
| Character customization | ✅ nothing to composite: player bodies ship complete with heads; the 80 CHARFACE face-variant textures export as PNG (applied by texture swap at char-create) |
| Fonts (0x7000) | ⚠️ partially decoded: header + 256 glyph widths understood; glyph bitmap packing TBD |

## License

MIT — see [LICENSE](LICENSE).

EQOA and its game assets are the property of their respective rights holders.
This project ships no game assets — it only reads data from discs you own.
