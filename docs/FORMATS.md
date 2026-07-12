# EQOA file formats

This document specifies the container and object formats parsed by
`pkg/eqoa/`. Everything here was recovered by reverse engineering the PS2
client (Ghidra + Emotion Engine loader) and validated against real disc data.
Engine function references (`FUN_00xxxxxx`) are addresses in the Frontiers
beta client executable and mark where each claim was verified.

## CSF — compressed container

Most disc files ship as CSF: a zlib block container around a raw ESF stream.

```
Offset  Size  Field
0       4     Magic "CESF"
4       4     NumberOfBlocks        (int32 LE)
8       8     TotalCompressedSize   (int64 LE)
16      8     TotalDecompressedSize (int64 LE)
24      8     FirstBlockOffset      (int64 LE, typically 40)
32      4     MaxCompressedBlock    (int32 LE)
36      4     Unknown               (often 0x77F534AC)
```

After the header, `NumberOfBlocks` blocks follow, each prefixed by an 8-byte
info record `{int32 compressedSize, int32 decompressedSize}` and containing a
standard zlib stream. Concatenating the inflated blocks yields the ESF file.
Implementation: `pkg/eqoa/csf.go`.

## ESF — object tree ("VIObjFile")

An ESF file is a tree of typed objects. The 32-byte file header:

```
Offset  Size  Field
0       4     Magic: "OBJF" (big endian file) or "FJBO" (little endian)
4       4     NumberOfObjects (top-level)
8       4     FileType
12      4     Unknown1
16      8     Offset
24      8     Unknown2
```

**Endianness rule:** the magic tells you the byte order — `FJBO` = little
endian, `OBJF` = big endian — and all subsequent fields follow it.  In
practice every disc examined (Frontiers beta, Beta 3, retail SLUS-207.44)
is `FJBO`/little-endian, as expected for the all-LE PS2 + Windows toolchain;
the big-endian path exists in the parser but no big-endian disc has been
seen.  Do not assume retail differs from beta here.

Every object starts with a 12-byte header:

```
Offset  Size  Field
0       2     ObjectType         (see table below)
2       2     ObjectVersion      (format revision — parsers branch on this)
4       4     ObjectSize         (body size in bytes, excluding this header)
8       4     NumberOfSubObjects (children parsed recursively from the body)
```

Most objects begin their body with a `uint32 DictID` — a 32-bit identifier
used for cross-references (textures by ID, sprite identity, etc.). DictIDs
are stable across game builds: the same model has the same ID in beta and
retail, and — critically — **the server-side "modelid" is the same value**
(see [MODEL_NAMES.md](MODEL_NAMES.md)).

### Object type registry

The full table lives in `pkg/eqoa/esf.go` (`ObjTypeNames`). The important
families:

| Range | Family |
|---|---|
| 0x1000–0x11xx | Surfaces (textures), materials, material palettes |
| 0x1200/0x1210 | Mesh data (static / skinned) |
| 0x2000–0x2Fxx | Sprites: renderable entities of all kinds |
| 0x2400 | Skeleton (see [ANIMATION.md](ANIMATION.md)) |
| 0x2600 | Animation set (see [ANIMATION.md](ANIMATION.md)) |
| 0x5000 | RefMap — generic int32→int32 dictionary (bone maps, sound refs) |
| 0x3000–0x32xx | Zone structures: rooms, terrain tables, actors |
| 0x4200 | Collision mesh |
| 0xB000–0xB1xx | Audio (PS2 ADPCM, XM modules) |
| 0xC000–0xC3xx | Particles and spell effects (not yet parsed) |

### Sprite containers

A "sprite" is any renderable entity. Character models are usually a
`CSprite` (0x2700) containing, in rough order:

```
0x2700 CSprite
├── 0x2710 CSpriteHeader        (dictID of the sprite)
├── 0x1110 MaterialPalette      (surfaces + materials)
├── 0x5000 RefMap               (sound/effect references — NOT the bone map)
├── 0xB070 (audio container)    (footsteps, vocalizations)
├── (mesh container)            (LOD tiers of 0x2320 SkinSubSprite → 0x1210)
├── 0x2610 (animation list)     (N × 0x2600 HSpriteAnim)
├── 0x2400 HSpriteHierarchy     (skeleton)
├── 0x5000 RefMap               (bone map — immediately AFTER the hierarchy)
└── 0x2450 HSpriteTriggers, misc
```

⚠️ Sprites carry **multiple 0x5000 RefMaps** with different meanings. The
bone map is specifically the one following the 0x2400 hierarchy in the same
child list (the engine's HSprite parser `FUN_0040cdb0` reads them in that
order). Grabbing "the first RefMap in the tree" silently yields the sound-
reference map and breaks all animations. See `findBoneMapSibling` in
`pkg/eqoa/asset.go`.

## Meshes — PrimBuffer (0x1200) / SkinPrimBuffer (0x1210)

Header (after optional dictID when ObjectVersion > 1):

```
int32 pbType      4 = zone terrain, 5 = skinned character mesh, others static
int32 numMaterials
int32 numGroups
int32 totalVerts
int32 p1, p2, p3  quantization exponents
```

Positions are dequantized by `1.0 / 2^p1`, UVs by `1.0 / 2^p2`. Vertices are
stored per face group; the skinned layout (pbType 5) is **21 bytes** per
vertex with no padding:

```
int16[3]  x, y, z      × 1/2^p1
int16[2]  u, v         × 1/2^p2
int8[3]   nx, ny, nz   × 1/127
uint8[4]  joint indices
uint8[4]  joint weights (÷255; renormalize — quantization drift is common)
```

A widely tempting mistake is assuming a padding byte after the normals
(22-byte stride). It parses without error and produces garbage positions —
verify with known-shape models when porting this.

Zone terrain (pbType 4) vertices carry a `uint16 VGroup` that indexes the
`ZonePreTranslations` (0x3250) array **per vertex**; applying translations
per-sprite instead of per-vertex causes seams between terrain sub-blocks.

## Materials and textures

- `Surface` (0x1000): a texture. PS2 formats including 8-bit paletted
  (PSMT8) with the GS's 32×32 block swizzle — `pkg/eqoa/surface.go`
  implements the unswizzle. Multiple mip levels may be present.
- `Material` (0x1100): layer list; each layer references a Surface by
  `TexID` (a dictID).
- `MaterialPalette` (0x1110): groups a `SurfaceArray` and `MaterialArray`
  for a sprite subtree; the nearest palette up the tree wins.

Surfaces referenced by no material (AO/detail maps) are skipped during export
to avoid phantom near-black textures in the GLB.

**Fullscreen UI backdrops are split across textures.** The GS has no
1024-wide texture format and only 4MB of VRAM, so hi-res screens (title,
status) are stored as consecutive 512×512 slices — the border art and title
lettering run continuously across the seam.  `convert` stitches runs of
consecutive standalone 512×512 surfaces into `PREFIX_screenN.png` composites
(rows 448–511 of each slice are padding); individual slices are always
written too.  The stitch order is file order: exact draw coordinates live in
client code, and texture dictIDs are hashed from names at runtime so they
never appear as searchable constants.  Animated UI uses a different
mechanism entirely — cells within one atlas addressed by UV offsets — never
file splitting.

## Audio

`Adpcm` (0xB000) wraps PS2 VAG-format ADPCM: `AdpcmHeader` (0xB010) carries
sample rate and block counts; `AdpcmSampleData` (0xB020) is the raw stream.
`convert` decodes it directly to 16-bit PCM `.wav` (`DecodeADPCM` in
`pkg/eqoa/audio.go` — the standard SPU2 codec: 16-byte blocks of
predictor/shift + 28 nibbles, five fixed filter pairs; block flag 0x07 ends
the stream).

`Xm` (0xB030) holds music converted from FastTracker II modules into a
runtime binary format: all text stripped, structures padded into fixed
arrays (256 pattern slots × 12 B directory, 128 instrument slots × 224 B,
128 sample headers × 24 B — unused slots filled with MSVC `0xCD`), pattern
data kept in standard XM packing verbatim, and samples re-encoded as PS2 SPU
ADPCM (sample lengths are in 16-byte ADPCM blocks; ×16 equals the 0xB060
blob size exactly).  `RebuildXM` (`pkg/eqoa/xm.go`) reverses the conversion
back to playable `.xm` files during `convert`: it decodes the ADPCM,
delta-encodes the PCM, and synthesizes the stripped 60-byte text header.
All 21 modules on the beta disc validate in libopenmpt.

Sound effects export directly as FLAC (pure-Go encoder, `mewkiz/flac`) —
verified bit-identical to the decoded PCM.  The `.xm` files are the archival
masters for music; render them to audio with any libopenmpt-based player
(`openmpt123 --render`) if a fixed waveform is preferred.

## Fullscreen images (.16 files)

`LOADING*.16` / `ERROR*.16` on disc are headerless 640×448 16-bpp images in
GS PSMCT16 order: little-endian `uint16`, red in bits 0–4, green 5–9, blue
10–14, alpha bit 15 (exactly 573,440 bytes). `convert` turns them into PNG.

## Zones

Zone ESFs (`TUNARIA.ESF` etc.) hold terrain in world-space coordinates, an
AABB tree of `ZoneRoom`s linking to sprites, `ZonePreTranslations` (LOD
centers / terrain sub-block offsets), `CollBuffer` collision meshes, and
`ZoneActors` placement records. `reader convert-zone` groups geometry per
Zone object and re-centers it; `reader scene` dumps actor placements to JSON.

## Practical notes for implementers

1. **Trust the magic, not the file extension** — some `.ESF` files on disc
   are CSF-compressed, some are raw.
2. **ObjectVersion changes layouts.** Parsers must branch on it (see the
   0x2600 and 0x2400 parsers for examples of version-gated fields).
3. **Bind-pose rendering proves nothing about your skeleton parse.** At bind
   pose, skinning matrices are identity regardless of how wrong your joint
   transforms are. Validate skeletons with *animated* poses. This masked a
   major bug in this tool for weeks — see
   [ANIMATION.md](ANIMATION.md#trap-2-the-skeleton-stores-world-space-transforms).
