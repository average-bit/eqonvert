# The EQOA animation system

This is the most reverse-engineering-intensive part of the tool, and the part
where naive assumptions produce output that looks *almost* right — smoothly
moving, but distorting the model in wrong ways. This document specifies the
three formats involved (skeleton, bone map, animation set), the engine's
runtime semantics, and — because they cost us weeks — the three traps that
make this format hostile to guesswork.

All engine references (`FUN_00xxxxxx`) are function addresses in the
Frontiers beta client, decompiled with Ghidra. The relevant call chain:

```
FUN_0040cdb0  HSprite (0x2200) parser — orchestrates the loads below
├── FUN_0040d168  parses 0x2400 skeleton, per joint calls…
│   └── FUN_0041ae00  …which converts world→local and stores joint defaults
├── FUN_0040e430  parses 0x5000 bone map into an int32→int32 dictionary
└── FUN_0040d6b8  parses 0x2600 animation sets (via FUN_0040d5b8)
       └── FUN_00421ea8  ActionSet init — reveals true field meanings

FUN_0041b6d8  StartAnimation — binds channels to joints via the bone map
FUN_0041dd98  Pose evaluator — per frame, writes joint local TRS
```

## 1. Skeleton — HSpriteHierarchy (0x2400)

Body layout (see `pkg/eqoa/hierarchy.go`):

```
[ObjectVersion != 0]  int32 headerFloatCount, float32[headerFloatCount]  (LOD distances)
int32 jointCount
per joint:
    int32     parentIndex        (-1 = root; parents always precede children)
    float32[4] rotation          quaternion XYZW
    float32    scale
    float32[3] position
    [ver != 0] int32 flags       (per-joint LOD level)
    [ver >= 2] float32[headerFloatCount] LOD blend weights
```

### Trap #1: the transforms are WORLD-space, not parent-relative

Every common skeleton format (glTF, FBX, MD5…) stores joints relative to
their parent. EQOA does not: **the stored TRS is the joint's bind pose in
model space.** The engine converts to parent-relative at load time:
`FUN_0041ae00` multiplies each joint's raw matrix with its parent's raw
matrix (`FUN_003d1e38`) and *decomposes* the product into the joint's default
local TRS (`FUN_003d4068`, stored at jointState+0xd0). Root joints store
their raw TRS as-is.

Empirical confirmation (character `0x8C9B4B39`, a Dark Elf female whose mesh
is 1.78 units tall): head joint raw Y = 1.825 ≈ mesh top; the arm chain sits
at raw Y ≈ 1.44 running horizontally out to X = ±0.76 — exactly the mesh's
T-pose arms; the leg chain reads 1.02 → 0.58 → 0.12 → 0.0 (hip, knee, ankle,
floor). These are model-space heights, not parent offsets.

**Why this trap is vicious:** if you treat the values as parent-relative and
accumulate them, the bind pose still renders perfectly — at bind pose,
`world × inverseBind` is identity for *any* self-consistent skeleton, so the
error is invisible until you animate. Always validate skeleton parses with
animated poses.

The exporter mirrors the engine: `HSpriteHierarchy.LocalTRS()` performs
world→local (local rotation = conj(parentWorldRot) ⊗ worldRot, etc.), and
inverse bind matrices are simply the inverse of each raw world TRS.

## 2. Bone map — RefMap (0x5000)

```
uint32 dictID
int32  count
count × { int32 boneID, int32 jointIndex }
```

Parsed by `FUN_0040e430` into a lookup dictionary. `boneID` is a hash-like
identifier for a bone *name*; `jointIndex` is the index into this skeleton's
joint array. This object is how animation channels find their joints — see
below.

⚠️ Sprites contain several 0x5000 RefMaps (sound references, effect
references). The bone map is the one **immediately following the 0x2400
hierarchy** in the same child list, matching the engine's parse order.

## 3. Animation set — HSpriteAnim (0x2600)

Body layout (see `pkg/eqoa/action.go`; fields gated by ObjectVersion):

```
uint32  dictID
[ver>1] int32 compressionType     0 = float32, 1 = int16-quantized
int32   numChannels               ← FIRST count
int32   numFrames                 ← SECOND count
[ver>=3] int32 extraChannelCount  (event track, not needed for export)
float32 timeScale
[ver>0] float32 fps, int32 flags

numChannels × {
    int32 boneID                  ← resolves through the 0x5000 bone map
    numFrames × BoneTransform
}
```

`BoneTransform` per frame: quaternion XYZW, uniform scale, position XYZ.
Uncompressed = 8 × float32 (32 bytes). Compressed = 8 × int16 (16 bytes)
with rotation × 2⁻¹⁵ and scale/position × 2⁻⁹ — dequantization constants
confirmed verbatim in both the parser (`FUN_0040d6b8`) and the evaluator
(`FUN_0041dd98`: `3.051851e-05` and `0.001953125`).

Playback rate: `effectiveFPS = fps × timeScale` (version-0 sets store no fps
and default it to 1.0, putting the whole rate in timeScale).

### Trap #2: the data is channel-major, and the counts are easy to swap

The two int32 counts are (channels, frames) — in that order. Reading them as
(frames, channels), i.e. frame-major, **parses without any error**: the
per-block int32 becomes a plausible-looking "per-frame index", total sizes
match exactly, and the transposed animation even looks smooth in places
(bones near each other have similar rotations, so a cross-section of bones
resembles a time series). The giveaways that finally exposed it:

- The engine uses the *second* count as a float animation end-position
  (`FUN_00401410` seeks to `(float)(numFrames-1)`, `FUN_00401a28` to
  `(float)numFrames`) — meaningless for a channel count.
- The ActionSet init (`FUN_00421ea8`) sizes its entry table by the *first*
  count with per-entry data offsets advancing by the *second* — i.e. one
  entry per channel, one block of frames per entry.
- Under the transposed reading, the "per-frame int32" values looked like
  garbage hashes; under the correct reading they are per-channel bone IDs
  that all resolve through the 0x5000 map.

### Channel→joint binding — never guess

`FUN_0041b6d8` (StartAnimation): for each channel, look up its `boneID` in
the sprite's bone map. Found → bind the channel to that joint index. Not
found → **silently skip the channel**. That skip rule is a feature:
animation sets are shared between different creatures, and each skeleton
subscribes only to the bones it has.

The resulting mapping is irregular (e.g. channels 0…8 → joints
1, 2, 8, 9, 10, 5, 6, 7, 4 on one biped). No structural heuristic —
sequential, breadth-first, depth-first — can reproduce it. Earlier versions
of this tool tried topology heuristics and every new creature family broke
them. If you are porting this: parse the bone map; do not infer.

### Trap #3: keyframes REPLACE the local transform (translation included)

The pose evaluator (`FUN_0041dd98`) per animated joint per frame:

1. read current and next frame `BoneTransform`,
2. slerp rotation (`FUN_003da838`), lerp scale and position,
3. **write the result directly into the joint's local TRS** — no composition
   with the bind pose,
4. joints with no channel get their stored default local TRS copied back
   (the world→local defaults from load time).

Two consequences for exporters:

- Rotation-only export is wrong. The frames carry the joint's full local
  *position* too, and it differs from the bind translation. (Sanity check
  that confirmed the whole model: an animated spine channel's position
  `(0, 0.080, 0.010)` equals `world(J1) − world(J0)` of the bind skeleton
  exactly.)
- The semantics happen to be *exactly* glTF's animation-channel semantics
  (sampler output replaces the node TRS property), so the export is direct:
  one rotation sampler + one translation sampler per channel. Scale is 1.0
  in all observed data and is not exported.

## 4. Upper/lower body pairs and layering

Character animation lists are ordered as **pairs**: even index = upper body
(spine, head, arms), odd index = lower body (root, legs). Both halves of a
pair share frame counts and timing. The engine plays two animation slots
simultaneously (`FUN_00401410` iterates exactly 2) and the evaluator supports
weighted blending onto the previous result — this is how EQOA plays "running
while attacking": lower(Running) layered with upper(Attack).

The logical animation ID = `pairIndex = actionIndex / 2`, and it matches the
byte IDs the game server sends (recovered by the ReturnHome server project,
`AnimationState.cs`):

| ID | Name | ID | Name |
|---|---|---|---|
| 0x00 | Idle (breathing)¹ | 0x16–0x35 | combat moves (punches, casts, kicks…) |
| 0x01 | Walking | 0x22 | TakeHit |
| 0x02 | WalkingBackwards | 0x38 | Wave |
| 0x03 | Running | 0x39 | Bow |
| 0x04/05 | TurningInPlace L/R | 0x3C | Point |
| 0x06 | Falling | 0x3D | Cheer |
| 0x07 | Crouching/Landing | 0x3F | SlowRightHand |
| 0x0A/0B | SideStep L/R | 0x40 | SquatFreeze |
| 0x0C/0D | SwimFast / SwimSlow | | |
| 0x0E | Die | | |

¹ 0x00 is absent from the server enum (it is the default state, never
requested explicitly); identified visually.

The exporter names each glTF animation `0x<ID>_<Name>_<upper|lower>_0x<dictID>`
(`animStateNames` in `pkg/gltf/export.go`). The body half is detected from the
data — the half whose channels include a root joint is "lower" — so it holds
for non-humanoid skeletons too. There is no "slow walk / fast walk": the
server modulated playback speed of the single Walking/Running clips.

Unknown IDs (0x08–0x09, 0x10, 0x12–0x15, 0x2D–0x31, 0x36–0x37, 0x3A–0x3B,
0x3E) are exported as `Unknown` — identifying them by watching the exports is
an open task.

## 5. Validation methodology

Every claim above was validated by at least one of:

- **Decompiled engine code** — the parser/evaluator addresses cited inline.
- **Numeric ground truth** — e.g. bone-map lookups reproduced exactly by the
  export; anim positions equal to world-position deltas of the bind skeleton.
- **Visual rendering** — a CPU-skinning renderer (no GPU/viewer dependencies)
  that samples exported GLBs and renders orthographic frames. With it we
  verified: humanoid idle/walk/attack/death poses, an 8-legged black widow, a
  whiptail scorpion (claws + curled tail), a copperhead snake rearing, treant
  variants, mounted riders, a centipede, and a kraken — all of whose
  recovered names later matched their appearance, closing the loop.

If you change animation or skeleton code, re-verify with animated poses of at
least one biped **and** one multi-limbed creature. The three traps above all
produce output that looks fine on the case you happen to test first.
