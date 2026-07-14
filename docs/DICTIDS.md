# EQOA DictIDs — reference

A **DictID** is EQOA's 32-bit resource identifier. Nearly every addressable
asset in the CSF/ESF data — a texture, a model, a sound, a zone placement — is
keyed by a DictID, and cross-references between assets (a material pointing at a
texture, a zone actor pointing at a model) are DictID references.

This document consolidates what is known about DictIDs from two sources: the
**file formats** (CSF/ESF) and **Ghidra decompiles of the retail client**. Where
a Ghidra address is cited it is from the analysed `slus_280.28` snapshot; note
the **address-skew caveat** at the end — addresses differ between the snapshot,
the SN-Systems symbol dump, and retail.

---

## 1. Where a DictID lives in the file

An ESF object is a 12-byte header followed by a body (and/or child objects):

```
ObjectHeader (12 bytes): int16 ObjectType, int16 ObjectVersion,
                         int32 ObjectSize, int32 NumberOfSubObjects
Body:                    ObjectSize bytes (may contain child objects)
```

For the object types listed below, the **DictID is the first u32 of the body**
(`body[0:4]`, in the file's byte order). It is *not* a separate header field —
so "the object's DictID" and "`body[0:4]`" are the same value. Types that carry
a DictID (from `pkg/eqoa/esf.go`):

| Type | Meaning | DictID identifies |
|------|---------|-------------------|
| `0x1000` | Surface | a texture |
| `0x1100` | Material | a material |
| `0x1200` / `0x1210` | PrimBuffer | mesh geometry |
| `0x2000` | Sprite | a model |
| `0x2200` | HSprite | a skeletal/segmented model |
| `0x2700` | GroupSprite | a multi-member model (creatures, windmills) |
| `0x2A10` | LODSprite header | an LOD model |
| `0x2310` / `0x2320` | Sub-sprite | a member mesh |
| `0x2C00` / `0x2C30` | (sprite variants) | a model |
| `0xA000` | (container) | — |
| `0xB000` | Adpcm | a VAG-ADPCM sound |
| `0xB030` | Xm | a tracker-music module |
| `0xB100` | (audio container) | — |
| `0x6000` | ZoneActor | the model/resource to place in a zone |
| `0x6020` | (zone actor variant) | — |
| `0x3240` | ZoneRoom | a room/chunk |

A DictID of `0` means "none" and is ignored.

---

## 2. Two provenances: name-hash vs. server modelid

A DictID is produced one of two ways, and both appear in the data:

### 2a. Name-hash (`FUN_003ceb10`)

Asset names are hashed into a DictID with a simple polynomial hash (base `0x83`):

```
uint32 hash(char *name) {
    uint32 h = 0;
    for (; *name; name++) h = h * 0x83 + (uint8)*name;
    return h;
}
```

Verified against known names (`csf_golang/.../model_names.json`):

| name (as hashed) | DictID |
|------------------|--------|
| `EruditeMale`  | `0x320C0B47` |
| `TrollMale`    | `0x4C6FA532` |
| `Fishing_Pole` | `0x4E9A52E9` |

The hashed string form varies (CamelCase, with/without underscores), so name
recovery is a dictionary attack: hash candidate names and match the DictID.

### 2b. Direct server modelid

Many DictIDs are **not** name-hashes — they are the server's `modelid` directly
(a small assigned number). This is how most creature/object names were
recovered: `sprite DictID == EQOAGameServer modelid`. Only ~23 of 916 known
names matched the `0x83` hash; the rest are direct modelids from the DB. So when
recovering a name, try **both**: the server DB (modelid) and the name-hash.

---

## 3. Client-side resolution (Ghidra)

The client resolves a DictID to a loaded resource through a **VIDictionary**
(the per-scene / global resource dictionary).

| Function | Role |
|----------|------|
| `FUN_003ceb10` | name string → DictID (the `0x83` hash) |
| `FUN_003c7c18` | resource lookup by id/handle in a manager |
| `FUN_00409090` | find an already-loaded resource by name |
| `VICSpriteCust::SetResources` | builds a VIDictionary from a CSF's surfaces |

**Important exception — index vs. DictID addressing.** Not everything is
addressed by DictID hash. Character-customization textures in `CHARCUST.CSF`
(the `VICSpriteCust` armor/face/hair system) are addressed by **dictionary
index** — their position in the CSF surface order — *not* by a name-hash. So
hashing candidate names never finds them, and a raw-DictID scan of the binary
finds nothing (the tables hold dictionary indices, not DictIDs). See
`ARMOR_TEXTURE_MAPPING.md`.

---

## 4. Recovering names for DictIDs

Sources eqonvert uses to put human names on DictIDs:

- **`model_names.json`** (916 entries): hex DictID → name. Built from the
  EQOAGameServer DB (`modelid == DictID`) plus name-hash matches.
  See `tools/build_model_names.py`, `rename_models.py`, and `docs/MODEL_NAMES.md`.
- **`itempattern` table** (EQOAGameServer): armor/item appearance — `patternfam`
  (armor set), `equipslot` (body slot), `itemname` — for naming armor textures.
- **Name-hash dictionary attack**: hash plausible names, match against unknown
  DictIDs.

---

## 5. ZoneActor DictIDs (placements — NOT spawns)

A `ZoneActor (0x6000)` is a static world placement. Confirmed layout via Ghidra
`ParseZoneActor` (`FUN_0040ff78`), which reads the body and passes it to the
scene-object instantiation `FUN_00437c50` (creates a static object of **type
`0xC`**):

```
[0:4]   uint32     DictID     — the model/resource to place (== the object DictID)
[4:16]  float32[3] Position   — East, Height, North
[16:28] float32[3] Rotation   — Euler: [0] yaw, [1] pitch, [2] roll
[28:32] float32    Scale      — typically 1.0  (NOT a sprite id)
[32:36] uint8[4]   Color      — RGBA tint
```

Key facts (verified via Ghidra + data on SCENE.ESF, 1101 actors):

- **The DictID is a model reference.** The same resolution path turns 1040/1101
  into rendered models. `body[0:4]` equals the object DictID on **all** actors —
  there is no hidden alternate id in the body.
- **Zones contain no spawn data.** Every placement is the same uniform record;
  there is no spawn/mob/timer/level field and no separate spawn object type.
  Spawn tables are **server-side** (EQOAGameServer). Actors that reference a
  creature model are still just model placements, not spawn tables.
- **"Unresolved" actors** are DictIDs whose model isn't in the files loaded at
  conversion time (a cross-file / shared / dynamically-loaded model), e.g.
  `0xEA22D890` (×7), `0x08E69137` (×3) in SCENE. They are *not* spawns.

---

## 6. Special values & sentinels

- **`0` DictID** — "none"; ignored.
- **`0xDEAD…` prefix** — a developer sentinel / placeholder / reserved slot.
  Example: ZoneActor `0xDEADD156` in SCENE is an unresolved placeholder, not a
  real model. Treat `0xDEAD…` DictIDs as "intentionally unset."
- Unresolved SCENE ZoneActors additionally share `color = 00 00 00 FF` (flat
  black / untinted), consistent with placeholder entries.

---

## 7. How the client reads the files (exhaustive Ghidra)

All CSF/ESF reading goes through one **resource-reader** object (a decompressing
stream + an object-parse stack). Every function below is from the analysed
`slus_280.28` snapshot; the decompiles are reproduced with variable names
simplified but offsets/logic intact.

### 7.1 The reader/parse-stack object

The reader is the object passed as the first argument (`reader`) to every read
function (in `ParseZoneActor` it is `*(actor + 0x24)`). Its relevant fields:

| Offset | Meaning |
|--------|---------|
| `+0xE8` | current object-stack **depth** (index) |
| `+0x100 + depth*0x20` | per-level object record (`0x20` bytes each) |
| &nbsp;&nbsp;`+0xF0` (u16) | ObjectType |
| &nbsp;&nbsp;`+0xF2` (u16) | ObjectVersion |
| &nbsp;&nbsp;`+0xF4` (u32) | ObjectSize |
| &nbsp;&nbsp;`+0xF8` (u32) | NumberOfSubObjects |
| &nbsp;&nbsp;`+0x108` (u32) | **bytes remaining** in the current object's body |
| `+0x238`/`+0x23C`/`+0x240` | stream state / header-read flags |
| `+0x248` | **body-readable flag** — set when the current object is a leaf (NumberOfSubObjects == 0) with body bytes to read |
| `DAT_004af470` | host-endian flag: when `!= 1`, multi-byte reads are byte-swapped |

**On-disc values are big-endian**; the client swaps them to host order at read
time (see `FUN_003cb3b0`). This is why eqonvert detects and honours the file's
byte order.

### 7.2 Read the next object header — `FUN_003cd9a0` (via wrapper `FUN_003cd980`)

Reads a 12-byte object header, byte-swaps its fields, pushes a new stack level,
and returns the type/version to the caller.

```c
undefined4 FUN_003cd9a0(reader, u16 *outType, u16 *outVersion, u64 *out, int *outEnd) {
  ...
  // read the 12-byte header into the new stack level's +0x110 slot
  FUN_003c94f0(reader, reader + depth*0x20 + 0x110, 0xc);   // raw read 12 bytes
  depth = *(reader + 0xE8) + 1;
  if (DAT_004af470 != 1) {                                   // big-endian → swap
    FUN_003cb3b0(2, reader + depth*0x20 + 0xF0);             // ObjectType  (u16)
    FUN_003cb3b0(2, reader + depth*0x20 + 0xF2);             // ObjectVersion (u16)
    FUN_003cb3b0(4, reader + depth*0x20 + 0xF4);             // ObjectSize  (u32)
    FUN_003cb3b0(4, reader + depth*0x20 + 0xF8);             // NumberOfSubObjects (u32)
  }
  *(reader + 0xE8) = depth;
  *outEnd = *(reader + depth*0x20 + 0xF4) + 0xC;             // size + header
  *(reader + depth*0x20 + 0x108) = *(reader + depth*0x20 + 0xF4);  // remaining = size
  *(reader + 0x240) = 1;
  *outType    = *(u16*)(reader + depth*0x20 + 0xF0);
  *outVersion = *(u16*)(reader + depth*0x20 + 0xF2);
  *(reader + 0x248) = (NumberOfSubObjects == 0) ? 1 : 0;     // leaf ⇒ body readable
  return 0;
}
```

So the **12-byte header is exactly** `ObjectType:u16, ObjectVersion:u16,
ObjectSize:u32, NumberOfSubObjects:u32`, matching `pkg/eqoa/esf.go`. A parent
object (NumberOfSubObjects > 0) is not "body-readable" — its body is child
objects, read by recursing with another `FUN_003cd9a0`.

### 7.3 Field readers — consume from the current object body

All three guard on `+0x248` (body readable) and the remaining-bytes counter
`+0x108`, read via the raw reader, byte-swap (except single bytes), and decrement
the counter. **A "DictID" is just the first `u32` field read from a leaf's body.**

```c
// read u32  (also used verbatim for float32 — same 4-byte swapped read)
undefined4 FUN_003ce058(reader, void *out) {          // == FUN_003ce220
  if (*(reader + 0x248) == 0)                    return -1;
  if (*(reader + depth*0x20 + 0x108) < 4)        return -1;   // not enough left
  FUN_003c94f0(reader, out, 4);                               // raw read 4 bytes
  if (DAT_004af470 != 1) FUN_003cb3b0(4, out);                // swap
  *(reader + depth*0x20 + 0x108) -= 4;                        // consume
  return 0;
}

// read 1 byte (no swap)
undefined4 FUN_003cde28(reader, void *out) {
  if (*(reader + 0x248) == 0)                     return -1;
  if (*(reader + depth*0x20 + 0x108) <= 0)        return -1;
  FUN_003c94f0(reader, out, 1);
  *(reader + depth*0x20 + 0x108) -= 1;
  return 0;
}
```

`FUN_003ce058` (u32) and `FUN_003ce220` (float32) are **byte-identical** — a
DictID and a float are both a swapped 4-byte read; only the caller's
interpretation differs. This is why `ZoneActor body[28:32]` reads cleanly either
as a float (scale, `1.0`) or, if misread, as a u32 — see the `SpriteID`→`Scale`
correction.

### 7.4 Raw byte fetch — `FUN_003c94f0`

The bottom of the stack: fetch `n` bytes from whatever backs the reader
(buffered vs. streamed/decompressed), used by all of the above.

```c
u64 FUN_003c94f0(int *reader, void *out, long n) {
  if (*reader < 0) {                     // streaming/compressed source
    if (reader[0xC] == 0)  return (reader[0xE] != 0) ? FUN_003cb7e0(reader,out,n) : -1;
    else                   return FUN_003cb500(reader, out, n);   // buffered
  } else {                               // direct source
    return (FUN_003c9690() == n) ? 0 : -1;
  }
}
```

### 7.5 Endian swap — `FUN_003cb3b0`

`FUN_003cb3b0(width, ptr)` reverses `width` bytes at `ptr` (width 2 or 4). Applied
to every multi-byte header field and every u32/float body field when
`DAT_004af470 != 1`. Net effect: **the on-disc format is big-endian.**

### 7.6 Worked example — `ParseZoneActor` (`FUN_0040ff78`)

Reads one `0x6000` object body using the field readers above, in this exact
order, then hands it to the scene-object constructor:

```c
long FUN_0040ff78(actor) {
  reader = *(actor + 0x24);
  FUN_003cd980(reader, &type, &ver, ...);                 // read header
  if (type != 0x6000) return -1;
  FUN_003ce058(reader, &id);                              // [0:4]   DictID (u32)
  FUN_003ce220(reader, &pos.x); FUN_003ce220(reader, &pos.y); FUN_003ce220(reader, &pos.z); // [4:16]  position
  FUN_003ce220(reader, &rot.x); FUN_003ce220(reader, &rot.y); FUN_003ce220(reader, &rot.z); // [16:28] rotation (Euler)
  FUN_003ce220(reader, &scale);                           // [28:32] scale (float)
  FUN_003cde28(reader, &col.r); FUN_003cde28(reader, &col.g);
  FUN_003cde28(reader, &col.b); FUN_003cde28(reader, &col.a);                                // [32:36] color RGBA
  // instantiate a static scene object (type 0xC) from the fields:
  actorObj = FUN_00437c50(scale, scene, &pos, &rot, id, &col);
  ...
}
```

`FUN_00437c50` stores `pos@+0x44`, `rot@+0x50`, `scale@+0x5C`, **id@+0x64**, color
`@+0x70`, sets the object type `+0x40 = 0xC`, and leaves resource handles
`+0x60/+0x68/+0x6C = 0xFFFFFFFF` to be resolved (DictID → model) when the actor
streams in. **The `id` here is the same DictID as `body[0:4]`** — confirmed on
all 1101 SCENE actors.

### 7.7 DictID hash — `FUN_003ceb10`

The name→DictID hash, verbatim:

```c
int FUN_003ceb10(char *name) {
  int h = 0, c = *name;
  while (*name != '\0') { name++; h = h * 0x83 + c; c = *name; }
  return h;
}
```

i.e. `h = 0; for each byte: h = h*0x83 + byte`.

### 7.8 Armor texture accessor — `FUN_004084e8`

`VICSpriteCust::GetArmorSetTexture` — a thin table accessor confirming that
armor textures are addressed by **index into a runtime table** (base
`DAT_004afa08`), not by DictID hash:

```c
void* FUN_004084e8(int idx) { return &DAT_004afa08 + idx*4; }
```

---

## 8. Address-skew caveat

Three images disagree on addresses — the Ghidra `slus_280.28` snapshot, the
SN-Systems symbol dump in `slus_280.28_snapshot_data.elf`, and retail. **Verify a
function by its decompiled body, not by trusting an address across images.**
Runtime-populated tables (e.g. the `VICSpriteCust` armor/tint tables at
`DAT_004afa08`) are zero in every static image and require a live PINE read.

### Function index (snapshot addresses)

| Symbol / role | Addr |
|---------------|------|
| DictID name-hash (`h*0x83+c`) | `FUN_003ceb10` |
| read next object header | `FUN_003cd9a0` (wrapper `FUN_003cd980`) |
| read u32 field | `FUN_003ce058` |
| read float32 field | `FUN_003ce220` |
| read byte field | `FUN_003cde28` |
| raw byte fetch | `FUN_003c94f0` |
| byte-swap (width, ptr) | `FUN_003cb3b0` |
| host-endian flag | `DAT_004af470` |
| resource lookup by id | `FUN_003c7c18` |
| find loaded resource by name | `FUN_00409090` |
| ParseZoneActor | `FUN_0040ff78` |
| ZoneActor instantiation (type 0xC) | `FUN_00437c50` |
| VICSpriteCust::GetArmorSetTexture | `FUN_004084e8` |
| armor texture table (runtime-filled) | `DAT_004afa08` |

---

*Related docs: `MODEL_NAMES.md`, `ARMOR_TEXTURE_MAPPING.md`, `FORMATS.md`,
`ANIMATION.md`.*
