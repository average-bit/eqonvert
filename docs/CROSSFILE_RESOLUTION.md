# Cross-file DictID resolution (the "unresolved ZoneActor" fix)

**Problem.** eqonvert resolves `ZoneActor (0x6000)` DictIDs by scanning known
sprite object types (`0x2000/0x2200/0x2A00/0x2C00/0x2700`) in the *local* zone
ESF. The client does NOT resolve that way — it uses a **resource directory**
(DictID→file-offset) spanning the zone file *and* four shared files. So eqonvert
misses:
- DictIDs present in the zone's own directory whose object type it doesn't scan
  (the `zone_actor_skip` ~62% — buildings / civil arch), and
- DictIDs that live in the shared `char/item/…` files (creatures, items placed
  in a zone).

## Client mechanism (reverse-engineered from the named beta client)

ZoneActors are **static streaming proxies**, not spawn points:

```
ParseZoneActor (0x0040FF78)
  -> VIScene::CreateStaticProxyActor(pos, scale, DictID, color) (0x00437C50)
        // lightweight proxy: DictID @+0x64, geometry handles unresolved
  ... on approach / stream-in ...
StreamInStaticActor (0x0043D940)
  -> VIZone::Find(DictID, &offset, &size)        (0x00465328)
       // MISS locally? -> VIZone::Load(DictID)  (0x004652B0)
       //                   -> VILoader::Load(offset, size)  (streams the resource in)
```

**`VIZone::Find` directory format** (what to parse): binary search over an array
at `zone+0x88` (count `zone+0x84`), each entry **16 bytes**, sorted by DictID:

| off | type | field |
|-----|------|-------|
| 0x0 | u64  | file offset |
| 0x8 | u32  | size |
| 0xC | u32  | DictID |

On disk this is a **`0xA010` ResourceDir** object → `ParseResourceTable` →
`VIArray<VIResourceElem>` (`ParseResourceDirObj` @0x00411010,
`ParseResourceDir` @0x00410F90). (ARENA's ESF also shows a `0x9000 ResourceTable`
+ `0x5000 RefMap` — confirm which the loader consumes when implementing.)

**Files opened into the loader** (`VIClient_OpenWorldResourceFiles` @0x006188E0):

| slot | file |
|------|------|
| 0 | `data\char.esf`     (creatures) |
| 1 | `data\ambtrack.esf` (ambient)   |
| 2 | `data\item.esf`     (items)     |
| 3 | `data\itemicon.esf`             |
| world | `data\scene.esf` \| `data\tunaria.esf` \| `data\zone%s.esf` |

## The patch

1. **Parse the resource directory** (`0xA010`/ResourceTable) in each ESF into a
   `DictID -> (fileOffset, size)` map (byte-swap via the existing ESF reader).
2. **Build a global directory** across: the zone file + `CHAR`/`AMBTRACK`/`ITEM`/
   `ITEMICON` + the scene/world file (eqonvert already extracts all of these).
3. **In the ZoneActor resolver**, when the current sprite-library scan misses,
   look the DictID up in the global directory → seek to `fileOffset` in the owning
   file → `ParseESF`/LoadAsset the object there → resolve geometry as usual.
4. Result: recovers the `zone_actor_skip` ~62% (streamed static props) and
   cross-file creature/item placements — the "unresolved / SpawnMarker" actors
   become real geometry.

## Notes / verify during implementation

- Confirm the on-disk `VIResourceElem` layout via `ParseResourceTable`
  (`0x00411010` calls it) — the runtime layout above is the target.
- Confirm the offset base (absolute file offset vs relative to a section).
- These are NOT spawns; the SpawnMarker feature can stay (rename `--mark-unresolved`)
  but most "unresolved" become resolvable with this directory. See elfconv
  `re_analysis/ZONE_ACTOR_RE.md` and memory `project_zone_spawn_placeholder`.
